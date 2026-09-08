package store

import (
	"context"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"
)

// MemoryStore is an in-process Store for tests and for `go run` without a
// Redis to hand. It is not a deployment option: nothing here survives a
// restart, which is precisely what the persistent zone must do (SPEC §4.2).
//
// It exists so that the WebSocket protocol tests exercise real handlers
// without a service container. Its semantics mirror RedisStore's, including
// the all-or-nothing rekey; RedisStore is the implementation the integration
// tests hold to the same behaviour.
type MemoryStore struct {
	mu       sync.Mutex
	now      func() time.Time
	tokens   map[string]Token
	groups   map[string]Group
	members  map[string]map[string]struct{}
	devices  map[string]Device
	wrapped  map[string]WrappedKey
	pairings map[string]Pairing
}

// NewMemoryStore returns an empty in-memory Store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		now:      func() time.Time { return time.Now().UTC() },
		tokens:   make(map[string]Token),
		groups:   make(map[string]Group),
		members:  make(map[string]map[string]struct{}),
		devices:  make(map[string]Device),
		wrapped:  make(map[string]WrappedKey),
		pairings: make(map[string]Pairing),
	}
}

var _ Store = (*MemoryStore)(nil)

// CreateToken implements Store.
func (m *MemoryStore) CreateToken(_ context.Context, name string) (Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tok := Token{Value: newID(), Name: name, CreatedAt: m.now()}
	m.tokens[tok.Value] = tok
	return tok, nil
}

// ListTokens implements Store.
func (m *MemoryStore) ListTokens(context.Context) ([]Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tokens := slices.Collect(maps.Values(m.tokens))
	sort.Slice(tokens, func(i, j int) bool { return tokens[i].CreatedAt.After(tokens[j].CreatedAt) })
	return tokens, nil
}

// GetToken implements Store.
func (m *MemoryStore) GetToken(_ context.Context, value string) (Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tok, ok := m.tokens[value]
	if !ok {
		return Token{}, ErrNotFound
	}
	return tok, nil
}

// ConsumeToken implements Store.
func (m *MemoryStore) ConsumeToken(_ context.Context, token string) (Group, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	created, err := m.createGroupLocked(token, nil)
	return created.Group, err
}

// CreateGroup implements Store.
func (m *MemoryStore) CreateGroup(_ context.Context, token string, dev NewDevice) (GroupCreation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.createGroupLocked(token, &dev)
}

func (m *MemoryStore) createGroupLocked(token string, dev *NewDevice) (GroupCreation, error) {
	tok, ok := m.tokens[token]
	if !ok {
		return GroupCreation{}, ErrNotFound
	}
	if tok.Used {
		return GroupCreation{}, ErrTokenConsumed
	}

	now := m.now()
	tok.Used = true
	tok.GroupID = token
	m.tokens[token] = tok

	group := Group{ID: token, Epoch: 1, CreatedAt: now}
	m.groups[token] = group
	m.members[token] = make(map[string]struct{})

	created := GroupCreation{Group: group}
	if dev != nil {
		device := Device{
			ID:        newID(),
			GroupID:   token,
			Name:      dev.Name,
			PublicKey: dev.PublicKey,
			CreatedAt: now,
		}
		m.devices[device.ID] = device
		m.members[token][device.ID] = struct{}{}
		if dev.WrappedGroupKey != nil {
			m.wrapped[device.ID] = WrappedKey{Epoch: 1, Key: dev.WrappedGroupKey}
		}
		created.Device = device
	}
	return created, nil
}

// GetGroup implements Store.
func (m *MemoryStore) GetGroup(_ context.Context, groupID string) (Group, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	g, ok := m.groups[groupID]
	if !ok {
		return Group{}, ErrNotFound
	}
	return g, nil
}

// AddDevice implements Store.
func (m *MemoryStore) AddDevice(_ context.Context, groupID string, dev NewDevice) (Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	group, ok := m.groups[groupID]
	if !ok {
		return Device{}, ErrNotFound
	}
	device := Device{
		ID:        newID(),
		GroupID:   groupID,
		Name:      dev.Name,
		PublicKey: dev.PublicKey,
		CreatedAt: m.now(),
	}
	m.devices[device.ID] = device
	m.members[groupID][device.ID] = struct{}{}
	if dev.WrappedGroupKey != nil {
		m.wrapped[device.ID] = WrappedKey{Epoch: group.Epoch, Key: dev.WrappedGroupKey}
	}
	return device, nil
}

// ListDevices implements Store.
func (m *MemoryStore) ListDevices(_ context.Context, groupID string) ([]Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	devices := make([]Device, 0, len(m.members[groupID]))
	for id := range m.members[groupID] {
		devices = append(devices, m.devices[id])
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].CreatedAt.Equal(devices[j].CreatedAt) {
			return devices[i].ID < devices[j].ID
		}
		return devices[i].CreatedAt.Before(devices[j].CreatedAt)
	})
	return devices, nil
}

// GetDevice implements Store.
func (m *MemoryStore) GetDevice(_ context.Context, deviceID string) (Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	dev, ok := m.devices[deviceID]
	if !ok {
		return Device{}, ErrNotFound
	}
	return dev, nil
}

// GetWrappedKey implements Store.
func (m *MemoryStore) GetWrappedKey(_ context.Context, deviceID string) (WrappedKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	k, ok := m.wrapped[deviceID]
	if !ok {
		return WrappedKey{}, ErrNotFound
	}
	return k, nil
}

// SetWrappedKey implements Store.
func (m *MemoryStore) SetWrappedKey(_ context.Context, deviceID string, key WrappedKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.wrapped[deviceID] = key
	return nil
}

// RemoveDevice implements Store.
func (m *MemoryStore) RemoveDevice(_ context.Context, groupID, deviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.members[groupID], deviceID)
	delete(m.devices, deviceID)
	delete(m.wrapped, deviceID)
	return nil
}

// ApplyRekey implements Store. Like the Lua script it mirrors, it validates
// everything before it writes anything.
func (m *MemoryStore) ApplyRekey(_ context.Context, groupID, revokedDeviceID string, expectedEpoch uint64, keys []RekeyKey) (Group, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	group, ok := m.groups[groupID]
	if !ok {
		return Group{}, ErrNotFound
	}
	if group.Epoch != expectedEpoch {
		return Group{}, ErrEpochConflict
	}
	if _, ok := m.members[groupID][revokedDeviceID]; !ok {
		return Group{}, ErrNotFound
	}

	remaining := make(map[string]bool, len(m.members[groupID]))
	for id := range m.members[groupID] {
		if id != revokedDeviceID {
			remaining[id] = true
		}
	}
	for _, k := range keys {
		if !remaining[k.DeviceID] {
			return Group{}, ErrInvalidRekey
		}
		remaining[k.DeviceID] = false
	}
	for _, covered := range remaining {
		if covered {
			return Group{}, ErrInvalidRekey
		}
	}

	next := expectedEpoch + 1
	for _, k := range keys {
		m.wrapped[k.DeviceID] = WrappedKey{Epoch: next, Key: k.Key}
	}
	delete(m.members[groupID], revokedDeviceID)
	delete(m.devices, revokedDeviceID)
	delete(m.wrapped, revokedDeviceID)
	group.Epoch = next
	m.groups[groupID] = group
	return group, nil
}

// CreatePairing implements Store.
func (m *MemoryStore) CreatePairing(_ context.Context, groupID, inviterDeviceID string, ttl time.Duration) (Pairing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	p := Pairing{
		Token:           newID(),
		GroupID:         groupID,
		InviterDeviceID: inviterDeviceID,
		CreatedAt:       now,
		ExpiresAt:       now.Add(ttl),
	}
	m.pairings[p.Token] = p
	return p, nil
}

// GetPairing implements Store.
func (m *MemoryStore) GetPairing(_ context.Context, token string) (Pairing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.getPairingLocked(token)
}

func (m *MemoryStore) getPairingLocked(token string) (Pairing, error) {
	p, ok := m.pairings[token]
	if !ok {
		return Pairing{}, ErrNotFound
	}
	// Redis expires the key; here the read enforces the deadline.
	if !m.now().Before(p.ExpiresAt) {
		delete(m.pairings, token)
		return Pairing{}, ErrNotFound
	}
	return p, nil
}

// ConsumePairing implements Store.
func (m *MemoryStore) ConsumePairing(_ context.Context, token, joinerDeviceID string) (Pairing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, err := m.getPairingLocked(token)
	if err != nil {
		return Pairing{}, err
	}
	if p.JoinerDeviceID != "" {
		return Pairing{}, ErrPairingConsumed
	}
	p.JoinerDeviceID = joinerDeviceID
	m.pairings[token] = p
	return p, nil
}

// DeletePairing implements Store.
func (m *MemoryStore) DeletePairing(_ context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.pairings, token)
	return nil
}

// TouchLastSeen implements Store.
func (m *MemoryStore) TouchLastSeen(_ context.Context, deviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	dev, ok := m.devices[deviceID]
	if !ok {
		return ErrNotFound
	}
	dev.LastSeen = m.now()
	m.devices[deviceID] = dev
	return nil
}
