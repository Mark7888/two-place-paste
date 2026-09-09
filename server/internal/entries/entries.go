// Package entries is the Redis ephemeral zone: clipboard entries and their
// inline ciphertext, every one of them carrying a 24-hour TTL fixed at write
// time (SPEC §4.2, §4.3, §4.5, §6).
//
// This is the zone `maxmemory-policy volatile-lru` is allowed to evict. Losing
// an entry costs one re-copy; losing a group record would cost the group,
// which is why groups live in internal/store and not here.
//
// An entry's lifetime is immutable. There is no pin, no favourite and no
// extend-TTL, because the blob garbage collector derives a blob's storage path
// from its expiry and reclaims whole hour buckets without consulting Redis
// (SPEC §4.5, ROADMAP §5 standing prohibition).
package entries

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors.
var (
	// ErrNotFound is returned when an entry does not exist, or has expired,
	// which from a client's point of view is the same thing.
	ErrNotFound = errors.New("entries: not found")

	// ErrTooLarge is returned when a body exceeds the per-entry ciphertext cap
	// (SPEC §4.3).
	ErrTooLarge = errors.New("entries: ciphertext exceeds the per-entry cap")

	// ErrSizeMismatch is returned when the declared size and the body length
	// disagree. The declared size is what lets the server refuse an oversize
	// entry before reading it, so it is not allowed to lie.
	ErrSizeMismatch = errors.New("entries: declared size does not match the body")
)

// Meta is everything the server knows about an entry. Content type, filename
// and plaintext size are inside the ciphertext and never appear here
// (SPEC §2.3).
type Meta struct {
	ID      string
	GroupID string

	// Epoch is the group key generation the ciphertext was encrypted under.
	// A client silently skips anything below its own (SPEC §3.3).
	Epoch uint64

	// Size is the ciphertext length in bytes as received.
	Size int64

	CreatedAt time.Time

	// ExpiresAt is CreatedAt + 24h, fixed here and never extended.
	ExpiresAt time.Time

	// StorageRef is the blob backend reference, empty for an inline entry.
	StorageRef string
}

// Inline reports whether the ciphertext is stored in Redis rather than in the
// blob backend.
func (m Meta) Inline() bool { return m.StorageRef == "" }

// Store is the ephemeral zone behind an interface. Implementations must be
// safe for concurrent use.
type Store interface {
	// Put writes the entry record, and its ciphertext when inline, both with
	// the entry's TTL. It is always called after the blob has been written
	// (SPEC §4.5, "write ordering").
	Put(ctx context.Context, m Meta, inline []byte) error

	// Latest returns the group's newest unexpired entry, or ErrNotFound.
	// Sync operates on this entry alone (SPEC §6).
	Latest(ctx context.Context, groupID string) (Meta, error)

	// History lists entry metadata newest first, starting strictly before the
	// given time (zero means from the newest), and returns the cursor for the
	// next page. History is only ever pulled; nothing here is pushed
	// (SPEC §6).
	History(ctx context.Context, groupID string, limit int, before time.Time) ([]Meta, time.Time, error)

	// Get returns one entry's metadata. It fails with ErrNotFound if the entry
	// belongs to another group: entry ids are not capabilities.
	Get(ctx context.Context, groupID, entryID string) (Meta, error)

	// InlineBody returns the inline ciphertext of an entry.
	InlineBody(ctx context.Context, entryID string) ([]byte, error)

	// EntryRefs returns every live blob reference keyed by reference and
	// valued by entry id. It backs `tpp gc --verify` only; normal reclamation
	// never queries Redis (SPEC §4.5).
	EntryRefs(ctx context.Context) (map[string]string, error)
}
