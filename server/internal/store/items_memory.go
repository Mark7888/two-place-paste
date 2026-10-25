package store

import (
	"cmp"
	"context"
	"slices"
	"time"
)

var _ Items = (*MemoryStore)(nil)

// StoreUsage implements Items.
func (m *MemoryStore) StoreUsage(_ context.Context, groupID string) (ItemUsage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.storeUsage[groupID], nil
}

// TotalStoreUsage implements Items.
func (m *MemoryStore) TotalStoreUsage(_ context.Context) (ItemUsage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.storeTotal, nil
}

// committedItemsLocked counts a group's committed items. The caller holds
// m.mu.
func (m *MemoryStore) committedItemsLocked(groupID string) int {
	n := 0
	for _, it := range m.storeItems {
		if it.GroupID == groupID && !it.Pending {
			n++
		}
	}
	return n
}

// addUsageLocked adds bytes and items to a group's and the total usage. The
// caller holds m.mu.
func (m *MemoryStore) addUsageLocked(groupID string, bytes, items int64) {
	u := m.storeUsage[groupID]
	u.Bytes += bytes
	u.Items += items
	m.storeUsage[groupID] = u
	m.storeTotal.Bytes += bytes
	m.storeTotal.Items += items
}

// ReserveItem implements Items.
func (m *MemoryStore) ReserveItem(_ context.Context, r ItemReservation, limits ItemLimits) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	g, ok := m.groups[r.GroupID]
	switch {
	case !ok:
		return ErrNotFound
	case !g.StoreEnabled:
		return ErrStoreDisabled
	case m.storeReclaim[r.GroupID]:
		return ErrStoreBusy
	case g.Epoch != r.Epoch:
		return ErrEpochConflict
	}
	if _, taken := m.storeItems[r.ItemID]; taken {
		return ErrItemExists
	}
	u := m.storeUsage[r.GroupID]
	if (limits.GroupBytes > 0 && u.Bytes+r.Accounted > limits.GroupBytes) ||
		(limits.TotalBytes > 0 && m.storeTotal.Bytes+r.Accounted > limits.TotalBytes) ||
		u.Items+1 > limits.GroupItems {
		return ErrStoreFull
	}
	m.storeItems[r.ItemID] = Item{
		ID:           r.ItemID,
		GroupID:      r.GroupID,
		Size:         r.Size,
		Accounted:    r.Accounted,
		WrapEpoch:    r.Epoch,
		ContentID:    r.ItemID,
		ContentEpoch: r.Epoch,
		Pending:      true,
		StartedAt:    r.StartedAt.UTC().Truncate(time.Millisecond),
	}
	m.addUsageLocked(r.GroupID, r.Accounted, 1)
	return nil
}

// CommitItem implements Items.
func (m *MemoryStore) CommitItem(_ context.Context, c ItemCommit) (Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	it, ok := m.storeItems[c.ItemID]
	if !ok || it.GroupID != c.GroupID || !it.Pending {
		return Item{}, ErrNotFound
	}
	g, ok := m.groups[c.GroupID]
	switch {
	case !ok:
		return Item{}, ErrNotFound
	case !g.StoreEnabled:
		return Item{}, ErrStoreDisabled
	case m.storeReclaim[c.GroupID]:
		return Item{}, ErrStoreBusy
	case g.Epoch != c.Epoch || it.WrapEpoch != c.Epoch:
		return Item{}, ErrEpochConflict
	}
	it.WrappedKeys = append([]byte(nil), c.WrappedKeys...)
	it.SealedMeta = append([]byte(nil), c.SealedMeta...)
	it.SavedAt = c.SavedAt.UTC().Truncate(time.Millisecond)
	it.Pending = false
	m.storeItems[c.ItemID] = it
	return it, nil
}

// ReleaseItem implements Items.
func (m *MemoryStore) ReleaseItem(_ context.Context, groupID, itemID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.releaseLocked(groupID, itemID), nil
}

func (m *MemoryStore) releaseLocked(groupID, itemID string) bool {
	it, ok := m.storeItems[itemID]
	if !ok || it.GroupID != groupID || !it.Pending {
		return false
	}
	delete(m.storeItems, itemID)
	m.addUsageLocked(groupID, -it.Accounted, -1)
	return true
}

// GetItem implements Items.
func (m *MemoryStore) GetItem(_ context.Context, groupID, itemID string) (Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.storeItems[itemID]
	if !ok || it.GroupID != groupID || it.Pending {
		return Item{}, ErrNotFound
	}
	return it, nil
}

// ItemRefs implements Items.
func (m *MemoryStore) ItemRefs(_ context.Context, groupID string) ([]ItemRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ItemRef
	for _, it := range m.storeItems {
		if it.GroupID == groupID && !it.Pending {
			out = append(out, ItemRef{ID: it.ID, SavedAt: it.SavedAt})
		}
	}
	slices.SortFunc(out, func(a, b ItemRef) int {
		if c := b.SavedAt.Compare(a.SavedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	return out, nil
}

// GetItems implements Items.
func (m *MemoryStore) GetItems(_ context.Context, groupID string, ids []string) ([]Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Item, 0, len(ids))
	for _, id := range ids {
		if it, ok := m.storeItems[id]; ok && it.GroupID == groupID && !it.Pending {
			out = append(out, it)
		}
	}
	return out, nil
}

// UpdateItemMeta implements Items.
func (m *MemoryStore) UpdateItemMeta(_ context.Context, groupID, itemID string, expectedRev uint64, sealedMeta []byte) (Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.storeItems[itemID]
	if !ok || it.GroupID != groupID || it.Pending {
		return Item{}, ErrNotFound
	}
	if it.MetaRev != expectedRev {
		return Item{}, ErrRevisionConflict
	}
	it.SealedMeta = append([]byte(nil), sealedMeta...)
	it.MetaRev++
	m.storeItems[itemID] = it
	return it, nil
}

// DeleteItem implements Items.
func (m *MemoryStore) DeleteItem(_ context.Context, groupID, itemID string) (Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.storeItems[itemID]
	if !ok || it.GroupID != groupID || it.Pending {
		return Item{}, ErrNotFound
	}
	delete(m.storeItems, itemID)
	m.addUsageLocked(groupID, -it.Accounted, -1)
	return it, nil
}

// ReleaseAllPending implements Items.
func (m *MemoryStore) ReleaseAllPending(_ context.Context) ([]Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Item
	for _, it := range m.storeItems {
		if it.Pending && m.releaseLocked(it.GroupID, it.ID) {
			out = append(out, it)
		}
	}
	return out, nil
}

// StoreLinks implements Items.
func (m *MemoryStore) StoreLinks(_ context.Context, groupID string) ([]ItemLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ItemLink, 0, len(m.storeLinks[groupID]))
	for epoch, link := range m.storeLinks[groupID] {
		out = append(out, ItemLink{Epoch: epoch, Link: append([]byte(nil), link...)})
	}
	slices.SortFunc(out, func(a, b ItemLink) int { return cmp.Compare(a.Epoch, b.Epoch) })
	return out, nil
}
