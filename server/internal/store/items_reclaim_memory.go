package store

import (
	"context"
	"slices"
	"time"
)

// PendingBefore implements Items.
func (m *MemoryStore) PendingBefore(_ context.Context, t time.Time) ([]Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Item
	for _, it := range m.storeItems {
		if it.Pending && it.StartedAt.Before(t) {
			out = append(out, it)
		}
	}
	slices.SortFunc(out, func(a, b Item) int { return a.StartedAt.Compare(b.StartedAt) })
	return out, nil
}

// QueueReclaim implements ItemReclaim.
func (m *MemoryStore) QueueReclaim(_ context.Context, groupID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.storeReclaim[groupID] = true
	return nil
}

// Reclaiming implements ItemReclaim.
func (m *MemoryStore) Reclaiming(_ context.Context, groupID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.storeReclaim[groupID], nil
}

// ReclaimQueue implements ItemReclaim.
func (m *MemoryStore) ReclaimQueue(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.storeReclaim))
	for id := range m.storeReclaim {
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}

// ReclaimItems implements ItemReclaim.
func (m *MemoryStore) ReclaimItems(_ context.Context, groupID string, n int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	got := 0
	for id, it := range m.storeItems {
		if got == n {
			break
		}
		if it.GroupID == groupID && !it.Pending {
			delete(m.storeItems, id)
			got++
		}
	}
	return got, nil
}

// FinishReclaim implements ItemReclaim.
func (m *MemoryStore) FinishReclaim(_ context.Context, groupID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.committedItemsLocked(groupID) > 0 {
		return ErrStoreBusy
	}
	for id, it := range m.storeItems {
		if it.GroupID == groupID {
			delete(m.storeItems, id)
		}
	}
	u := m.storeUsage[groupID]
	m.storeTotal.Bytes -= u.Bytes
	m.storeTotal.Items -= u.Items
	delete(m.storeUsage, groupID)
	return nil
}

// EndReclaim implements ItemReclaim.
func (m *MemoryStore) EndReclaim(_ context.Context, groupID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.storeReclaim, groupID)
	return nil
}

// AddBlobRetry implements ItemReclaim.
func (m *MemoryStore) AddBlobRetry(_ context.Context, r BlobRetry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.storeBlobRetry[r] = true
	return nil
}

// BlobRetries implements ItemReclaim.
func (m *MemoryStore) BlobRetries(_ context.Context) ([]BlobRetry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]BlobRetry, 0, len(m.storeBlobRetry))
	for r := range m.storeBlobRetry {
		out = append(out, r)
	}
	return out, nil
}

// RemoveBlobRetry implements ItemReclaim.
func (m *MemoryStore) RemoveBlobRetry(_ context.Context, r BlobRetry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.storeBlobRetry, r)
	return nil
}
