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

	// ErrInvalidID is returned when a client-chosen entry id is not in the
	// alphabet this package accepts. The id is a Redis key and a blob
	// filename, so it is validated before it reaches either (SPEC §4.2, §4.5).
	ErrInvalidID = errors.New("entries: entry id is not usable")

	// ErrInvalidHeader is a sealed entry header of the wrong size.
	ErrInvalidHeader = errors.New("entries: entry header is not usable")

	// ErrQuotaExceeded is returned when blob storage is full and evicting
	// older entries cannot make room, because what remains is held by
	// uploads still in progress.
	ErrQuotaExceeded = errors.New("entries: blob storage is full")

	// ErrEntryExists is returned when a client-chosen entry id is already
	// taken. Entry keys are global, so accepting a duplicate would let one
	// group overwrite another group's entry; the write is refused instead.
	ErrEntryExists = errors.New("entries: entry id is already in use")
)

// MaxIDLen bounds a client-chosen entry id.
const MaxIDLen = 64

// HeaderSize and HeaderV2Size are the two lengths a sealed entry header has:
// version 1 (/spec/crypto.md §5.5) and version 2 (§5.5.1). Each is fixed, so
// the server learns only which version wrote a header, and nothing else.
const (
	HeaderSize   = 297
	HeaderV2Size = 553
)

// ValidID reports whether an id may be used as an entry id.
//
// The alphabet is base64url's, which is what a server-generated id already
// uses: an id is a Redis key and, for a large entry, a filename inside a blob
// bucket. Rejecting everything else here is what keeps a client-chosen id from
// reaching into another directory or colliding with the key layout — the whole
// reason ids are validated in one place rather than at each use.
func ValidID(id string) bool {
	if id == "" || len(id) > MaxIDLen {
		return false
	}
	for i := range len(id) {
		c := id[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

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

	// Header is the sealed entry header the writer sent (/spec/crypto.md
	// §5.5), stored and returned as is; nil when it sent none. The server
	// cannot read it.
	Header []byte
}

// GroupStats is what the admin dashboard knows about one group's entries.
type GroupStats struct {
	// Entries is the number of live entries.
	Entries int64

	// Oldest is when the oldest live entry was written; zero when there is
	// none.
	Oldest time.Time
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
	//
	// It creates and never replaces: an id that already exists fails with
	// ErrEntryExists, atomically, so two writers racing on one id cannot both
	// think they won and a client-chosen id cannot clobber an existing entry.
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

	// Exists reports whether an unexpired entry already holds this id, in any
	// group. It is how a client-chosen id is refused before anything is
	// written; Put is what makes the refusal race-free.
	Exists(ctx context.Context, entryID string) (bool, error)

	// InlineBody returns the inline ciphertext of an entry.
	InlineBody(ctx context.Context, entryID string) ([]byte, error)

	// EntryRefs returns every live blob reference keyed by reference and
	// valued by entry id. It backs `tpp gc --verify` only; normal reclamation
	// never queries Redis (SPEC §4.5).
	EntryRefs(ctx context.Context) (map[string]string, error)

	// BlobEntries returns the metadata of every live blob-backed entry. It
	// backs `tpp gc --rebuild-accounting` only, and scans the whole zone.
	BlobEntries(ctx context.Context) ([]Meta, error)

	// GroupStats counts a group's live entries, inline and blob alike, and
	// reports when the oldest of them was written. It backs the admin
	// dashboard, which shows counts and never content.
	GroupStats(ctx context.Context, groupID string) (GroupStats, error)

	// Delete removes an entry before its expiry: its record, its inline body
	// and its place in the group's history. It does not touch the blob, which
	// is the caller's to delete. Deleting an unknown entry is not an error.
	//
	// It is how a storage eviction takes an entry away. That shortens an
	// entry's life, which SPEC §4.5 permits; nothing may lengthen one.
	Delete(ctx context.Context, groupID, entryID string) error

	// DeleteGroupIndex drops what is left of a group's history index once
	// its entries have been deleted one by one: ids of entries that expired
	// on their own, which the index only sheds on writes.
	DeleteGroupIndex(ctx context.Context, groupID string) error
}

// PurgeResult is what PurgeGroup removed.
type PurgeResult struct {
	// Entries is the number of entries deleted.
	Entries int

	// Bytes is their ciphertext, inline and blob alike.
	Bytes int64

	// Uploads is the number of chunked uploads aborted.
	Uploads int
}
