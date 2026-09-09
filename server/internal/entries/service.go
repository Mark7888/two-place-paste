package entries

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
)

// Service is the entry write and read path: it decides between inline storage
// and the blob backend, enforces the size caps, and fixes each entry's expiry
// (SPEC §4.3, §4.5, §6).
type Service struct {
	store Store
	blobs blob.Backend

	inlineMax int64
	maxBytes  int64
	ttl       time.Duration

	now func() time.Time
}

// Options configures a Service. Zero fields take the spec's values.
type Options struct {
	// InlineMaxBytes is the largest ciphertext stored inline in Redis
	// (SPEC §4.3: 256 KB).
	InlineMaxBytes int64

	// MaxBytes is the hard per-entry ciphertext cap (SPEC §4.3: 10 MB).
	MaxBytes int64

	// TTL is the entry lifetime. It is not configurable in the deployment and
	// exists here only so tests can write an entry that is about to expire.
	// Nothing may extend it once an entry is written (SPEC §4.5).
	TTL time.Duration

	// Now supplies UTC timestamps; tests replace it.
	Now func() time.Time
}

// NewService returns an entry service over the given store and blob backend.
func NewService(store Store, blobs blob.Backend, opts Options) *Service {
	s := &Service{
		store:     store,
		blobs:     blobs,
		inlineMax: opts.InlineMaxBytes,
		maxBytes:  opts.MaxBytes,
		ttl:       opts.TTL,
		now:       opts.Now,
	}
	if s.inlineMax <= 0 {
		s.inlineMax = 256 << 10
	}
	if s.maxBytes <= 0 {
		s.maxBytes = 10 << 20
	}
	if s.ttl <= 0 {
		s.ttl = 24 * time.Hour
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	return s
}

// MaxBytes is the per-entry ciphertext cap this service enforces. The
// transport uses it to bound a frame before reading it.
func (s *Service) MaxBytes() int64 { return s.maxBytes }

// Put stores one encrypted entry and returns its metadata.
//
// declaredSize is checked before the body is touched, so an oversize entry is
// refused without reading it. The body is then read through a limit anyway:
// the declaration is a courtesy, not a guarantee (SPEC §4.3).
//
// The blob is written **before** the Redis record (SPEC §4.5, "write
// ordering"). A crash in between leaves an orphan in a bucket that expires
// within 24 hours; the reverse order would leave an entry nobody can read.
func (s *Service) Put(ctx context.Context, groupID string, epoch uint64, declaredSize int64, body io.Reader) (Meta, error) {
	if declaredSize < 0 {
		return Meta{}, fmt.Errorf("put entry: %w: negative declared size", ErrSizeMismatch)
	}
	if declaredSize > s.maxBytes {
		return Meta{}, fmt.Errorf("put entry: %w: declared %d bytes, cap is %d", ErrTooLarge, declaredSize, s.maxBytes)
	}

	now := s.now()
	meta := Meta{
		ID:        newEntryID(),
		GroupID:   groupID,
		Epoch:     epoch,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
	}

	var inline []byte
	if declaredSize <= s.inlineMax {
		var err error
		inline, err = readAtMost(body, s.inlineMax)
		if err != nil {
			return Meta{}, fmt.Errorf("put entry %s: %w", meta.ID, err)
		}
		meta.Size = int64(len(inline))
	} else {
		ref, err := s.blobs.Put(ctx, meta.ID, meta.ExpiresAt, io.LimitReader(body, s.maxBytes+1))
		if errors.Is(err, blob.ErrTooLarge) {
			return Meta{}, fmt.Errorf("put entry %s: %w", meta.ID, ErrTooLarge)
		}
		if err != nil {
			return Meta{}, fmt.Errorf("put entry %s: %w", meta.ID, err)
		}
		meta.StorageRef = ref
		meta.Size = declaredSize
	}

	if meta.Size != declaredSize {
		// Best-effort cleanup; the bucket sweep is the backstop either way.
		if meta.StorageRef != "" {
			_ = s.blobs.Delete(ctx, meta.StorageRef)
		}
		return Meta{}, fmt.Errorf("put entry %s: %w: declared %d, received %d", meta.ID, ErrSizeMismatch, declaredSize, meta.Size)
	}

	if err := s.store.Put(ctx, meta, inline); err != nil {
		return Meta{}, fmt.Errorf("put entry %s: %w", meta.ID, err)
	}
	return meta, nil
}

// Latest returns the group's most recent entry (SPEC §6).
//
// The ciphertext comes back only when the entry is stored inline. A larger one
// is pulled with Fetch, so that a sync check costs a metadata read rather than
// up to 10 MB off the blob backend, and so the wire behaviour matches what
// EntryLatestResponse promises.
func (s *Service) Latest(ctx context.Context, groupID string) (Meta, []byte, error) {
	meta, err := s.store.Latest(ctx, groupID)
	if err != nil {
		return Meta{}, nil, fmt.Errorf("read latest entry of group %s: %w", groupID, err)
	}
	if !meta.Inline() {
		return meta, nil, nil
	}
	body, err := s.body(ctx, meta)
	if err != nil {
		return Meta{}, nil, err
	}
	return meta, body, nil
}

// History lists entry metadata newest first and returns the cursor for the
// next page. Bodies are not read: they are pulled one at a time with Fetch,
// when the user taps an entry (SPEC §6).
func (s *Service) History(ctx context.Context, groupID string, limit int, before time.Time) ([]Meta, time.Time, error) {
	metas, next, err := s.store.History(ctx, groupID, limit, before)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read history of group %s: %w", groupID, err)
	}
	return metas, next, nil
}

// Fetch returns one entry with its ciphertext, wherever it is stored.
func (s *Service) Fetch(ctx context.Context, groupID, entryID string) (Meta, []byte, error) {
	meta, err := s.store.Get(ctx, groupID, entryID)
	if err != nil {
		return Meta{}, nil, fmt.Errorf("read entry %s: %w", entryID, err)
	}
	body, err := s.body(ctx, meta)
	if err != nil {
		return Meta{}, nil, err
	}
	return meta, body, nil
}

func (s *Service) body(ctx context.Context, meta Meta) ([]byte, error) {
	if meta.Inline() {
		body, err := s.store.InlineBody(ctx, meta.ID)
		if err != nil {
			return nil, fmt.Errorf("read inline body of entry %s: %w", meta.ID, err)
		}
		return body, nil
	}

	rc, err := s.blobs.Get(ctx, meta.StorageRef)
	if errors.Is(err, blob.ErrNotFound) {
		// The record outlived its blob: to a client the entry is simply gone.
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read entry %s: %w", meta.ID, err)
	}
	defer rc.Close()

	body, err := readAtMost(rc, s.maxBytes)
	if err != nil {
		return nil, fmt.Errorf("read entry %s: %w", meta.ID, err)
	}
	return body, nil
}

// readAtMost reads up to limit bytes and reports ErrTooLarge if the source has
// more. It reads exactly one byte past the limit — never the whole body — so
// the cap is enforced at the reader (SPEC §4.3).
func readAtMost(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, ErrTooLarge
	}
	return body, nil
}

// newEntryID returns a fresh URL-safe identifier. It doubles as a filename in
// the blob layout, which is why the alphabet matters (SPEC §4.5).
func newEntryID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("entries: crypto/rand: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
