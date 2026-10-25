package store

import (
	"cmp"
	"context"
	"slices"
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

// memItem is one item's record in the memory store.
type memItem struct {
	groupID   string
	committed bool
}

// committedItemsLocked counts a group's committed items. The caller holds
// m.mu.
func (m *MemoryStore) committedItemsLocked(groupID string) int {
	n := 0
	for _, it := range m.storeItems {
		if it.groupID == groupID && it.committed {
			n++
		}
	}
	return n
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
