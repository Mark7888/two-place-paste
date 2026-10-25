package store

import "context"

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
