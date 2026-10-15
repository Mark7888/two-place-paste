package quota

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Memory is an in-process Ledger for tests and for running without Redis. It
// makes the same decisions as the Redis ledger, and the shared test suite
// holds both to them.
type Memory struct {
	limits Limits
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]memEntry
}

type memEntry struct {
	Reservation
	pending bool
}

// NewMemory returns an empty in-memory ledger.
func NewMemory(opts Options) *Memory {
	return &Memory{limits: opts.Limits, now: opts.now(), entries: make(map[string]memEntry)}
}

var _ Ledger = (*Memory)(nil)

// Reserve implements Ledger.
func (m *Memory) Reserve(_ context.Context, r Reservation) ([]Victim, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.expireLocked(m.now())
	if _, ok := m.entries[r.EntryID]; ok {
		return nil, fmt.Errorf("reserve %s: %w", r.EntryID, ErrExists)
	}
	if exceeds(r.Size, m.limits.GroupBytes) || exceeds(r.Size, m.limits.TotalBytes) {
		return nil, fmt.Errorf("reserve %s: %w", r.EntryID, ErrTooLarge)
	}

	// Oldest first, by expiry: with a fixed TTL that is creation order.
	ordered := make([]memEntry, 0, len(m.entries))
	var total int64
	groups := make(map[string]int64)
	for _, e := range m.entries {
		ordered = append(ordered, e)
		total += e.Size
		groups[e.GroupID] += e.Size
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].ExpiresAt.Equal(ordered[j].ExpiresAt) {
			return ordered[i].ExpiresAt.Before(ordered[j].ExpiresAt)
		}
		return ordered[i].EntryID < ordered[j].EntryID
	})

	chosen := make(map[string]bool)
	var victims []Victim
	take := func(e memEntry) {
		chosen[e.EntryID] = true
		total -= e.Size
		groups[e.GroupID] -= e.Size
		victims = append(victims, Victim{EntryID: e.EntryID, GroupID: e.GroupID, Size: e.Size})
	}

	if m.limits.GroupBytes > 0 {
		for _, e := range ordered {
			if groups[r.GroupID]+r.Size <= m.limits.GroupBytes {
				break
			}
			if e.GroupID == r.GroupID && !e.pending {
				take(e)
			}
		}
		if groups[r.GroupID]+r.Size > m.limits.GroupBytes {
			return nil, fmt.Errorf("reserve %s: %w", r.EntryID, ErrExceeded)
		}
	}
	if m.limits.TotalBytes > 0 {
		for _, e := range ordered {
			if total+r.Size <= m.limits.TotalBytes {
				break
			}
			if !chosen[e.EntryID] && !e.pending {
				take(e)
			}
		}
		if total+r.Size > m.limits.TotalBytes {
			return nil, fmt.Errorf("reserve %s: %w", r.EntryID, ErrExceeded)
		}
	}

	for _, v := range victims {
		delete(m.entries, v.EntryID)
	}
	m.entries[r.EntryID] = memEntry{Reservation: r, pending: true}
	return victims, nil
}

// Commit implements Ledger.
func (m *Memory) Commit(_ context.Context, entryID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[entryID]; ok {
		e.pending = false
		m.entries[entryID] = e
	}
	return nil
}

// Release implements Ledger.
func (m *Memory) Release(_ context.Context, entryID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, entryID)
	return nil
}

// Expire implements Ledger.
func (m *Memory) Expire(_ context.Context, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.expireLocked(now), nil
}

func (m *Memory) expireLocked(now time.Time) int {
	n := 0
	for id, e := range m.entries {
		if !e.ExpiresAt.After(now) {
			delete(m.entries, id)
			n++
		}
	}
	return n
}

// Usage implements Ledger.
func (m *Memory) Usage(context.Context) (Usage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var u Usage
	for _, e := range m.entries {
		u.Bytes += e.Size
		u.Entries++
	}
	return u, nil
}

// GroupUsage implements Ledger.
func (m *Memory) GroupUsage(_ context.Context, groupID string) (Usage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var u Usage
	for _, e := range m.entries {
		if e.GroupID == groupID {
			u.Bytes += e.Size
			u.Entries++
		}
	}
	return u, nil
}

// Rebuild implements Ledger.
func (m *Memory) Rebuild(_ context.Context, entries []Reservation) error {
	for _, r := range entries {
		if err := r.validate(); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = make(map[string]memEntry, len(entries))
	for _, r := range entries {
		m.entries[r.EntryID] = memEntry{Reservation: r}
	}
	return nil
}

// exceeds reports whether size is over a limit, where zero is unlimited.
func exceeds(size, limit int64) bool { return limit > 0 && size > limit }

func (r Reservation) validate() error {
	switch {
	case r.EntryID == "":
		return fmt.Errorf("quota: reservation has no entry id")
	case r.GroupID == "":
		return fmt.Errorf("quota: reservation for %s has no group id", r.EntryID)
	case r.Size < 0:
		return fmt.Errorf("quota: reservation for %s has negative size %d", r.EntryID, r.Size)
	}
	return nil
}
