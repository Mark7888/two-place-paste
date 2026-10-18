// Package quota accounts for blob storage and enforces the global and
// per-group limits on it, evicting the oldest entries when a new one would not
// fit (docs/internal/plans/s3-blob-storage.md §4.4).
//
// Only blob-backed entries are accounted. Inline entries live in Redis and are
// bounded by its maxmemory; counting them here would double-charge them.
//
// Eviction shortens an entry's life; nothing here ever lengthens one. That is
// what keeps the bucket sweep of SPEC §4.5 correct: an evicted blob is deleted
// early, and a blob whose delete fails is still reclaimed when its hour bucket
// expires.
//
// The ledger is self-healing by construction. Every accounted entry carries the
// expiry it was written with, and Expire forgets everything past it, so an
// entry whose upload crashed between Reserve and Commit costs its bytes for at
// most one TTL — never forever.
package quota

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors.
var (
	// ErrTooLarge is returned when one entry is larger than a limit on its
	// own: no amount of eviction could make it fit.
	ErrTooLarge = errors.New("quota: entry is larger than the storage limit")

	// ErrExceeded is returned when the entry would fit an empty store but
	// eviction cannot free enough room, because what is left is held by
	// uploads still in flight.
	ErrExceeded = errors.New("quota: storage limit reached")

	// ErrExists is returned when the entry id is already accounted.
	ErrExists = errors.New("quota: entry is already accounted")
)

// Limits are the storage caps, in ciphertext bytes. Zero means unlimited.
//
// A group limit larger than the total limit is legal: the total is then the
// effective cap for every group.
type Limits struct {
	TotalBytes int64
	GroupBytes int64
}

// Options configures a ledger.
type Options struct {
	Limits Limits

	// Now supplies the time a reservation forgets expired entries at; tests
	// replace it. Defaults to the wall clock, in UTC.
	Now func() time.Time
}

func (o Options) now() func() time.Time {
	if o.Now != nil {
		return o.Now
	}
	return func() time.Time { return time.Now().UTC() }
}

// Reservation is one entry asking for room.
type Reservation struct {
	EntryID   string
	GroupID   string
	Size      int64
	ExpiresAt time.Time
}

// Victim is an entry the ledger has evicted to make room. It is already gone
// from the ledger when it is returned; the caller deletes its record and blob.
type Victim struct {
	EntryID string
	GroupID string
	Size    int64
}

// Usage is the ledger's view of what is stored.
type Usage struct {
	// Bytes is the accounted size of every live blob-backed entry, including
	// ones whose upload is still in flight.
	Bytes int64

	// Entries is the number of accounted entries.
	Entries int64
}

// Evictions counts entries evicted over the reporting window.
type Evictions struct {
	Count int64
	Bytes int64
}

// evictionWindowHours is the reporting window of Ledger.Evictions.
const evictionWindowHours = 24

// Ledger is the accounting store. Implementations must be safe for
// concurrent use, and Reserve must be atomic: two uploads racing for the last
// free bytes cannot both win.
type Ledger interface {
	// Reserve accounts for a new entry, evicting the oldest committed entries
	// first from its group (to its group limit) and then from anywhere (to the
	// total limit). The new entry is pending until Commit: a pending entry is
	// never chosen as a victim.
	//
	// It returns ErrTooLarge when the entry alone exceeds a limit, ErrExceeded
	// when eviction cannot free enough, and ErrExists for an id that is
	// already accounted. On any error nothing has changed.
	Reserve(ctx context.Context, r Reservation) ([]Victim, error)

	// Commit marks a reserved entry as stored, which makes it evictable.
	Commit(ctx context.Context, entryID string) error

	// Release forgets an entry: an upload that failed, or an entry deleted
	// by other means. Releasing an unknown id is not an error.
	Release(ctx context.Context, entryID string) error

	// Expire forgets every entry whose expiry is at or before now, and
	// reports how many it forgot. The bucket sweep calls it.
	Expire(ctx context.Context, now time.Time) (int, error)

	// Usage reports the totals across every group.
	Usage(ctx context.Context) (Usage, error)

	// GroupUsage reports one group's totals.
	GroupUsage(ctx context.Context, groupID string) (Usage, error)

	// ReleasePending forgets every entry still pending — reserved, never
	// committed — and reports how many. The server calls it once at startup,
	// before it takes traffic: uploads live in its memory, so any pending
	// entry then was orphaned by the previous process and would otherwise
	// hold its room, unevictable, until it expired.
	ReleasePending(ctx context.Context) (int, error)

	// Evictions reports how many entries were evicted in about the last
	// day, and their bytes. It is for the admin dashboard: frequent evictions
	// mean the limits are too tight for how the relay is used.
	Evictions(ctx context.Context) (Evictions, error)

	// Rebuild replaces the whole ledger with the given entries, all
	// committed. It backs `tpp gc --rebuild-accounting`, for use after Redis
	// data loss or a restore; it is never part of normal operation.
	Rebuild(ctx context.Context, entries []Reservation) error
}
