package store

import (
	"context"
	"slices"
	"strings"
	"time"
)

var _ APIKeys = (*MemoryStore)(nil)

// CreateAPIKey implements APIKeys.
func (m *MemoryStore) CreateAPIKey(_ context.Context, k APIKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.apiKeys[k.ID]; ok {
		return ErrAPIKeyExists
	}
	if len(m.apiKeys) >= MaxAPIKeys {
		return ErrAPIKeyLimit
	}
	m.apiKeys[k.ID] = cloneAPIKey(k)
	return nil
}

// GetAPIKey implements APIKeys.
func (m *MemoryStore) GetAPIKey(_ context.Context, id string) (APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	k, ok := m.apiKeys[id]
	if !ok {
		return APIKey{}, ErrNotFound
	}
	return cloneAPIKey(k), nil
}

// ListAPIKeys implements APIKeys.
func (m *MemoryStore) ListAPIKeys(context.Context) ([]APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]APIKey, 0, len(m.apiKeys))
	for _, k := range m.apiKeys {
		out = append(out, cloneAPIKey(k))
	}
	sortAPIKeys(out)
	return out, nil
}

// DeleteAPIKey implements APIKeys.
func (m *MemoryStore) DeleteAPIKey(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.apiKeys[id]; !ok {
		return ErrNotFound
	}
	delete(m.apiKeys, id)
	return nil
}

// TouchAPIKey implements APIKeys.
func (m *MemoryStore) TouchAPIKey(_ context.Context, id string, at time.Time, ip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	k, ok := m.apiKeys[id]
	if !ok {
		return ErrNotFound
	}
	k.LastUsedAt = at.UTC()
	k.LastUsedIP = ip
	m.apiKeys[id] = k
	return nil
}

func cloneAPIKey(k APIKey) APIKey {
	k.Hash = slices.Clone(k.Hash)
	k.Scopes = slices.Clone(k.Scopes)
	return k
}

// sortAPIKeys orders keys newest first, then by id so the order is total.
func sortAPIKeys(keys []APIKey) {
	slices.SortFunc(keys, func(a, b APIKey) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
}
