// Package blob stores ciphertext too large to sit inline in Redis, and
// reclaims it by expiry bucket rather than by reference counting
// (SPEC §4.3, §4.5).
//
// The whole scheme rests on one invariant: an entry's lifetime is fixed at 24
// hours when it is written and is never extended. That makes a blob's expiry
// knowable at write time, so it is encoded into the storage path and the
// sweeper never has to ask Redis whether anything still points at a file. A
// pin, favourite or extend-TTL feature would invalidate this outright — see
// the standing prohibition in ROADMAP §5.
package blob

import (
	"context"
	"errors"
	"io"
	"time"
)

// Sentinel errors.
var (
	// ErrTooLarge is returned when a body exceeds the per-entry ciphertext cap
	// (SPEC §4.3). It is raised at the reader, before the body is buffered.
	ErrTooLarge = errors.New("blob: ciphertext exceeds the per-entry cap")

	// ErrNotFound is returned when a reference names no stored blob.
	ErrNotFound = errors.New("blob: not found")

	// ErrInvalidRef is returned for a reference that is not the layout this
	// package writes. It is a guard against path traversal, not a diagnostic.
	ErrInvalidRef = errors.New("blob: invalid reference")

	// ErrExists is returned when a blob already exists for an entry id.
	//
	// A backend creates and never replaces. Entry ids are chosen by the
	// writing client, because the id is bound into the ciphertext before it is
	// sent (/spec/crypto.md §5.3), so a client that picks an id already in use
	// must not be able to overwrite the ciphertext filed under it — including
	// another group's, since a blob's filename is the entry id and nothing
	// else.
	ErrExists = errors.New("blob: a blob already exists for this entry")

	// ErrStorageFull is returned when the backend itself is out of room —
	// the disk backend's free-space floor — regardless of what the storage
	// limits would allow. Nothing is left behind.
	ErrStorageFull = errors.New("blob: storage is full")

	// ErrIncomplete is returned when a chunked upload is completed before it
	// has received the size it was begun with.
	ErrIncomplete = errors.New("blob: upload is incomplete")
)

// StaleUploadAge is how old an unfinished chunked upload must be before the
// sweep removes what it left behind. It is well above any upload session
// timeout the server allows, so the sweep never takes an upload that is still
// in use; it exists for uploads abandoned by a crash, which no session
// remembers.
const StaleUploadAge = 2 * time.Hour

// BucketLayout is the time format of an hour bucket directory: the UTC hour in
// which everything inside it expires.
const BucketLayout = "2006010215"

// Backend stores and reclaims oversized ciphertext.
//
// Implementations must be safe for concurrent use. Unlike the interface
// sketched in SPEC §4.3, every method takes a context first
// (docs/internal/conventions.md §3).
type Backend interface {
	// Put stores r under entryID and returns the reference to record in the
	// Redis entry. expiresAt is passed in rather than derived because
	// reclamation depends on it (SPEC §4.5). A body larger than the backend's
	// cap fails with ErrTooLarge and leaves nothing behind.
	//
	// Put creates and never replaces: an entry id that already has a blob
	// fails with ErrExists.
	Put(ctx context.Context, entryID string, expiresAt time.Time, r io.Reader) (ref string, err error)

	// Get opens a stored blob. The caller closes it.
	Get(ctx context.Context, ref string) (io.ReadCloser, error)

	// Delete removes one blob. It is an optimisation, never a correctness
	// requirement: the bucket sweep is the backstop, so a failure here is
	// bounded by the 24-hour TTL (SPEC §4.5).
	Delete(ctx context.Context, ref string) error

	// Sweep reclaims every bucket that has fully passed at now, and anything
	// an unfinished upload left behind more than StaleUploadAge ago.
	Sweep(ctx context.Context, now time.Time) error

	// BeginUpload starts a blob that arrives in chunks, for an entry larger
	// than one frame. size is the exact final length; more is refused with
	// ErrTooLarge. Like Put, nothing is visible under the entry's reference
	// until Complete, and Complete creates and never replaces.
	BeginUpload(ctx context.Context, entryID string, expiresAt time.Time, size int64) (Upload, error)

	// GetRange opens length bytes of a stored blob from offset. The caller
	// keeps the range inside the blob and closes the reader.
	GetRange(ctx context.Context, ref string, offset, length int64) (io.ReadCloser, error)
}

// Upload is a blob being written in chunks. Its methods are called from one
// goroutine at a time, in order: Write any number of times, then exactly one
// of Complete or Abort.
type Upload interface {
	// Write appends one chunk. Data past the size the upload was begun with
	// fails with ErrTooLarge and is not written.
	Write(ctx context.Context, chunk []byte) error

	// Complete makes the blob visible and returns its reference. It fails
	// with ErrIncomplete short of the declared size, and with ErrExists when
	// the entry id already has a blob; either way nothing is left behind.
	Complete(ctx context.Context) (ref string, err error)

	// Abort discards what was written. It is safe after a failed Complete.
	Abort(ctx context.Context) error
}

// Bucket returns the hour-bucket directory name for a blob expiring at
// expiresAt: the UTC hour rounded **up**, so nothing is reclaimed before it
// has expired.
//
// All arithmetic is UTC. A bucket that shifted with the host timezone would
// delete live blobs every spring and leak dead ones every autumn (SPEC §4.5,
// docs/internal/conventions.md §6).
func Bucket(expiresAt time.Time) string {
	utc := expiresAt.UTC()
	rounded := utc.Truncate(time.Hour)
	if rounded.Before(utc) {
		rounded = rounded.Add(time.Hour)
	}
	return rounded.Format(BucketLayout)
}

// BucketEnd returns the instant at which everything in the named bucket has
// expired: the end of that UTC hour. It is the second return value that says
// whether the name was a bucket at all.
func BucketEnd(bucket string) (time.Time, bool) {
	t, err := time.ParseInLocation(BucketLayout, bucket, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t.Add(time.Hour), true
}

// Expired reports whether a bucket's hour has fully passed at now.
func Expired(bucket string, now time.Time) bool {
	end, ok := BucketEnd(bucket)
	if !ok {
		return false
	}
	return !now.UTC().Before(end)
}
