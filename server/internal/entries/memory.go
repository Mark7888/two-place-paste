package entries

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore is an in-process Store for tests and for running the server
// without a Redis to hand. It expires entries on read rather than on a timer,
// which is enough to reproduce the semantics the ephemeral zone promises.
type MemoryStore struct {
	mu      sync.Mutex
	now     func() time.Time
	metas   map[string]Meta
	bodies  map[string][]byte
	byGroup map[string][]string
}

// NewMemoryStore returns an empty in-memory Store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		now:     func() time.Time { return time.Now().UTC() },
		metas:   make(map[string]Meta),
		bodies:  make(map[string][]byte),
		byGroup: make(map[string][]string),
	}
}

var _ Store = (*MemoryStore)(nil)

// Put implements Store.
func (m *MemoryStore) Put(_ context.Context, meta Meta, inline []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.metas[meta.ID] = meta
	if meta.Inline() {
		m.bodies[meta.ID] = inline
	}
	m.byGroup[meta.GroupID] = append(m.byGroup[meta.GroupID], meta.ID)
	return nil
}

// live returns the group's unexpired entries, newest first.
func (m *MemoryStore) live(groupID string) []Meta {
	now := m.now()
	metas := make([]Meta, 0, len(m.byGroup[groupID]))
	for _, id := range m.byGroup[groupID] {
		meta, ok := m.metas[id]
		if !ok || !now.Before(meta.ExpiresAt) {
			continue
		}
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].CreatedAt.After(metas[j].CreatedAt) })
	return metas
}

// Latest implements Store.
func (m *MemoryStore) Latest(_ context.Context, groupID string) (Meta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	metas := m.live(groupID)
	if len(metas) == 0 {
		return Meta{}, ErrNotFound
	}
	return metas[0], nil
}

// History implements Store.
func (m *MemoryStore) History(_ context.Context, groupID string, limit int, before time.Time) ([]Meta, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var page []Meta
	for _, meta := range m.live(groupID) {
		if !before.IsZero() && !meta.CreatedAt.Before(before) {
			continue
		}
		if len(page) == limit {
			return page, page[len(page)-1].CreatedAt, nil
		}
		page = append(page, meta)
	}
	return page, time.Time{}, nil
}

// Get implements Store.
func (m *MemoryStore) Get(_ context.Context, groupID, entryID string) (Meta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	meta, ok := m.metas[entryID]
	if !ok || meta.GroupID != groupID || !m.now().Before(meta.ExpiresAt) {
		return Meta{}, ErrNotFound
	}
	return meta, nil
}

// InlineBody implements Store.
func (m *MemoryStore) InlineBody(_ context.Context, entryID string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	body, ok := m.bodies[entryID]
	if !ok {
		return nil, ErrNotFound
	}
	return body, nil
}

// EntryRefs implements Store.
func (m *MemoryStore) EntryRefs(context.Context) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	refs := make(map[string]string)
	for id, meta := range m.metas {
		if !meta.Inline() {
			refs[meta.StorageRef] = id
		}
	}
	return refs, nil
}
