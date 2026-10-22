package entries

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/quota"
)

// Service is the entry write and read path: it decides between inline storage
// and the blob backend, enforces the size caps, and fixes each entry's expiry
// (SPEC §4.3, §4.5, §6).
type Service struct {
	store Store
	blobs blob.Backend
	quota quota.Ledger

	onEvict func(ctx context.Context, groupID, entryID string)
	logger  *slog.Logger

	inlineMax int64
	maxBytes  int64
	ttl       time.Duration

	frameMax            int64
	chunkBytes          int64
	uploadTTL           time.Duration
	maxUploadsPerDevice int
	uploads             uploads

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

	// Quota accounts for blob storage and enforces its limits by evicting
	// the oldest entries. Nil disables accounting: every blob is accepted
	// and nothing is ever evicted.
	Quota quota.Ledger

	// OnEvict is told about every entry evicted to make room, after it is
	// gone, so that the transport can tell the group. Optional.
	OnEvict func(ctx context.Context, groupID, entryID string)

	// Logger receives eviction and cleanup logs. Defaults to slog.Default().
	Logger *slog.Logger

	// SingleFrameMaxBytes is the most ciphertext one frame carries, in a put
	// or a fetch; a larger entry goes through the chunked path. Defaults to
	// 10 MiB, the protocol's value.
	SingleFrameMaxBytes int64

	// ChunkBytes is the chunked path's chunk size. Defaults to 8 MiB.
	ChunkBytes int64

	// UploadTTL is how long a chunked upload may sit idle. Defaults to 10
	// minutes.
	UploadTTL time.Duration

	// MaxUploadsPerDevice bounds the uploads one device may have in progress.
	// Defaults to 4.
	MaxUploadsPerDevice int
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
		quota:     opts.Quota,
		onEvict:   opts.OnEvict,
		logger:    opts.Logger,

		frameMax:            opts.SingleFrameMaxBytes,
		chunkBytes:          opts.ChunkBytes,
		uploadTTL:           opts.UploadTTL,
		maxUploadsPerDevice: opts.MaxUploadsPerDevice,
		uploads:             uploads{byHandle: make(map[string]*uploadSession)},
	}
	if s.frameMax <= 0 {
		s.frameMax = 10 << 20
	}
	if s.chunkBytes <= 0 {
		s.chunkBytes = 8 << 20
	}
	if s.uploadTTL <= 0 {
		s.uploadTTL = 10 * time.Minute
	}
	if s.maxUploadsPerDevice <= 0 {
		s.maxUploadsPerDevice = 4
	}
	if s.logger == nil {
		s.logger = slog.Default()
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

// MaxBytes is the per-entry ciphertext cap this service enforces.
func (s *Service) MaxBytes() int64 { return s.maxBytes }

// FrameMaxBytes is the most ciphertext one frame carries. The transport bounds
// every frame it reads by it.
func (s *Service) FrameMaxBytes() int64 { return max(s.frameMax, s.chunkBytes) }

// Put stores one encrypted entry and returns its metadata.
//
// declaredSize is checked before the body is touched, so an oversize entry is
// refused without reading it. The body is then read through a limit anyway:
// the declaration is a courtesy, not a guarantee (SPEC §4.3).
//
// The blob is written **before** the Redis record (SPEC §4.5, "write
// ordering"). A crash in between leaves an orphan in a bucket that expires
// within 24 hours; the reverse order would leave an entry nobody can read.
// entryID is the identifier the writing client chose and bound into its
// ciphertext's associated data (/spec/crypto.md §5.3). An empty one is
// assigned here instead, for a client that binds no id; anything else is
// validated before it is used, because it becomes a Redis key and a blob
// filename.
func (s *Service) Put(ctx context.Context, groupID string, epoch uint64, entryID string, declaredSize int64, body io.Reader) (Meta, error) {
	return s.PutWithHeader(ctx, groupID, epoch, entryID, nil, declaredSize, body)
}

// PutWithHeader is Put with the writer's sealed entry header (/spec/crypto.md
// §5.5), which is stored with the entry and returned in its metadata. A nil
// header is the same as Put.
func (s *Service) PutWithHeader(ctx context.Context, groupID string, epoch uint64, entryID string, header []byte, declaredSize int64, body io.Reader) (Meta, error) {
	if err := checkHeader(header); err != nil {
		return Meta{}, fmt.Errorf("put entry: %w", err)
	}
	if declaredSize < 0 {
		return Meta{}, fmt.Errorf("put entry: %w: negative declared size", ErrSizeMismatch)
	}
	if declaredSize > s.maxBytes {
		return Meta{}, fmt.Errorf("put entry: %w: declared %d bytes, cap is %d", ErrTooLarge, declaredSize, s.maxBytes)
	}
	if declaredSize > s.frameMax {
		return Meta{}, fmt.Errorf("put entry: %w: declared %d bytes, more than one frame carries; upload it in chunks", ErrTooLarge, declaredSize)
	}
	if entryID == "" {
		entryID = newEntryID()
	} else {
		if !ValidID(entryID) {
			// Checked before the id reaches a key or a path, and without
			// echoing it: an id that failed validation is not something to put
			// in a log line (docs/internal/conventions.md §2).
			return Meta{}, fmt.Errorf("put entry: %w: %d bytes outside [A-Za-z0-9_-]{1,%d}",
				ErrInvalidID, len(entryID), MaxIDLen)
		}
		// Refused before the blob is written, so a client cannot even attempt
		// to write over the ciphertext of an entry that already holds this id.
		// The check is not the guarantee — Put and the blob backend both
		// create-only, and that is what closes the race — it is what keeps the
		// common case from touching storage at all.
		taken, err := s.store.Exists(ctx, entryID)
		if err != nil {
			return Meta{}, fmt.Errorf("put entry %s: %w", entryID, err)
		}
		if taken {
			return Meta{}, fmt.Errorf("put entry %s: %w", entryID, ErrEntryExists)
		}
	}

	now := s.now()
	meta := Meta{
		ID:        entryID,
		GroupID:   groupID,
		Epoch:     epoch,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
		Header:    header,
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
		if err := s.reserve(ctx, meta, declaredSize); err != nil {
			return Meta{}, err
		}
		// From here every failure gives the room back. Released without the
		// request's cancellation: a client that hangs up mid-upload must not
		// leave its reservation holding bytes for a day.
		committed := false
		defer func() {
			if committed {
				s.commit(context.WithoutCancel(ctx), meta.ID)
			} else {
				s.release(context.WithoutCancel(ctx), meta.ID)
			}
		}()

		counted := &countingReader{r: io.LimitReader(body, s.frameMax+1)}
		ref, err := s.blobs.Put(ctx, meta.ID, meta.ExpiresAt, counted)
		if errors.Is(err, blob.ErrTooLarge) {
			return Meta{}, fmt.Errorf("put entry %s: %w", meta.ID, ErrTooLarge)
		}
		if errors.Is(err, blob.ErrStorageFull) {
			return Meta{}, fmt.Errorf("put entry %s: %w", meta.ID, ErrQuotaExceeded)
		}
		if errors.Is(err, blob.ErrExists) {
			// The id is taken by an entry whose blob is already on disk. The
			// backend refused to replace it, which is what keeps a chosen id
			// from destroying someone else's ciphertext.
			return Meta{}, fmt.Errorf("put entry %s: %w", meta.ID, ErrEntryExists)
		}
		if err != nil {
			return Meta{}, fmt.Errorf("put entry %s: %w", meta.ID, err)
		}
		meta.StorageRef = ref
		meta.Size = counted.n
		if meta.Size != declaredSize {
			// The reservation was for the declared size, so a body that is
			// shorter must not be stored under it: a client could otherwise
			// hold room it never used. Best-effort; the sweep is the backstop.
			_ = s.blobs.Delete(context.WithoutCancel(ctx), ref)
			return Meta{}, fmt.Errorf("put entry %s: %w: declared %d, received %d", meta.ID, ErrSizeMismatch, declaredSize, meta.Size)
		}

		if err := s.store.Put(ctx, meta, nil); err != nil {
			if errors.Is(err, ErrEntryExists) {
				// Lost a race for the id. The blob is this call's own — the
				// backend creates and never replaces — so removing it takes
				// nothing but its own bytes. Best-effort; the bucket sweep is
				// the backstop (SPEC §4.5).
				_ = s.blobs.Delete(context.WithoutCancel(ctx), meta.StorageRef)
			}
			return Meta{}, fmt.Errorf("put entry %s: %w", meta.ID, err)
		}
		committed = true
		return meta, nil
	}

	if meta.Size != declaredSize {
		return Meta{}, fmt.Errorf("put entry %s: %w: declared %d, received %d", meta.ID, ErrSizeMismatch, declaredSize, meta.Size)
	}
	if err := s.store.Put(ctx, meta, inline); err != nil {
		return Meta{}, fmt.Errorf("put entry %s: %w", meta.ID, err)
	}
	return meta, nil
}

// checkHeader accepts no header or a sealed one of the one size every header
// has. The server cannot check anything more: the header is ciphertext.
func checkHeader(header []byte) error {
	if len(header) != 0 && len(header) != HeaderSize {
		return fmt.Errorf("%w: header is %d bytes, want %d", ErrInvalidHeader, len(header), HeaderSize)
	}
	return nil
}

// reserve claims room for a blob-backed entry and evicts whatever the ledger
// chose to make that room. Without a ledger it does nothing.
//
// The reservation is the declared size, which the blob backend then enforces:
// a body that turns out longer fails ErrTooLarge, shorter fails the size
// check, and either way the reservation is released.
func (s *Service) reserve(ctx context.Context, meta Meta, size int64) error {
	if s.quota == nil {
		return nil
	}
	victims, err := s.quota.Reserve(ctx, quota.Reservation{
		EntryID:   meta.ID,
		GroupID:   meta.GroupID,
		Size:      size,
		ExpiresAt: meta.ExpiresAt,
	})
	switch {
	case errors.Is(err, quota.ErrTooLarge):
		return fmt.Errorf("put entry %s: %w: larger than the storage limit", meta.ID, ErrTooLarge)
	case errors.Is(err, quota.ErrExceeded):
		return fmt.Errorf("put entry %s: %w", meta.ID, ErrQuotaExceeded)
	case errors.Is(err, quota.ErrExists):
		return fmt.Errorf("put entry %s: %w", meta.ID, ErrEntryExists)
	case err != nil:
		return fmt.Errorf("put entry %s: reserve storage: %w", meta.ID, err)
	}
	s.evict(context.WithoutCancel(ctx), victims)
	return nil
}

// evict removes entries the ledger has already given up, oldest first: the
// record goes before the blob, so a reader racing an eviction finds the entry
// gone (ErrNotFound) rather than a record whose blob is missing.
//
// Failures are logged, not returned. The ledger has already freed the room —
// that decision was atomic — and a blob left behind is reclaimed by the bucket
// sweep when its hour expires (SPEC §4.5).
func (s *Service) evict(ctx context.Context, victims []quota.Victim) {
	for _, v := range victims {
		meta, err := s.store.Get(ctx, v.GroupID, v.EntryID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			s.logger.ErrorContext(ctx, "eviction: read entry failed",
				slog.String("entry_id", v.EntryID), slog.Any("error", err))
			continue
		}
		if err := s.store.Delete(ctx, v.GroupID, v.EntryID); err != nil {
			s.logger.ErrorContext(ctx, "eviction: delete entry failed",
				slog.String("entry_id", v.EntryID), slog.Any("error", err))
			continue
		}
		if meta.StorageRef != "" {
			if err := s.blobs.Delete(ctx, meta.StorageRef); err != nil {
				s.logger.WarnContext(ctx, "eviction: delete blob failed; the bucket sweep will reclaim it",
					slog.String("entry_id", v.EntryID), slog.Any("error", err))
			}
		}
		s.logger.InfoContext(ctx, "entry evicted to stay within the storage limit",
			slog.String("group_id", v.GroupID),
			slog.String("entry_id", v.EntryID),
			slog.Int64("size", v.Size))
		if s.onEvict != nil {
			s.onEvict(ctx, v.GroupID, v.EntryID)
		}
	}
}

// purgePage is how many entries PurgeGroup reads at a time.
const purgePage = 200

// PurgeGroup deletes every entry of a group now, ahead of its expiry: the
// admin freeing a group's storage, or deleting the group
// (docs/internal/plans/admin-group-management.md). Uploads in progress for the
// group are aborted first, so nothing lands after the purge.
//
// Each entry goes the way an eviction does — record, then blob — and the
// OnEvict hook tells the group's open history lists. Deleting an entry only
// shortens its life, which SPEC §4.5 permits. A blob whose delete fails is
// reclaimed by the bucket sweep when its hour expires.
func (s *Service) PurgeGroup(ctx context.Context, groupID string) (PurgeResult, error) {
	ctx = context.WithoutCancel(ctx)
	var res PurgeResult
	res.Uploads = s.abortGroupUploads(ctx, groupID)

	for {
		// Always the newest page: every entry read is deleted before the
		// next read, so the index shrinks until a read comes back empty.
		metas, _, err := s.store.History(ctx, groupID, purgePage, time.Time{})
		if err != nil {
			return res, fmt.Errorf("purge group %s: %w", groupID, err)
		}
		if len(metas) == 0 {
			break
		}
		for _, m := range metas {
			if err := s.store.Delete(ctx, groupID, m.ID); err != nil {
				return res, fmt.Errorf("purge group %s: %w", groupID, err)
			}
			if m.StorageRef != "" {
				if err := s.blobs.Delete(ctx, m.StorageRef); err != nil {
					s.logger.WarnContext(ctx, "purge: delete blob failed; the bucket sweep will reclaim it",
						slog.String("entry_id", m.ID), slog.Any("error", err))
				}
			}
			res.Entries++
			res.Bytes += m.Size
			if s.onEvict != nil {
				s.onEvict(ctx, groupID, m.ID)
			}
		}
	}
	if err := s.store.DeleteGroupIndex(ctx, groupID); err != nil {
		return res, fmt.Errorf("purge group %s: %w", groupID, err)
	}
	if s.quota != nil {
		if _, err := s.quota.ForgetGroup(ctx, groupID); err != nil {
			return res, fmt.Errorf("purge group %s: %w", groupID, err)
		}
	}
	s.logger.InfoContext(ctx, "group entries purged",
		slog.String("group_id", groupID),
		slog.Int("entries", res.Entries),
		slog.Int64("bytes", res.Bytes),
		slog.Int("uploads", res.Uploads))
	return res, nil
}

// abortGroupUploads abandons every upload in progress for one group and
// reports how many.
func (s *Service) abortGroupUploads(ctx context.Context, groupID string) int {
	s.uploads.mu.Lock()
	var mine []*uploadSession
	for _, u := range s.uploads.byHandle {
		if u.groupID == groupID {
			mine = append(mine, u)
		}
	}
	s.uploads.mu.Unlock()

	n := 0
	for _, u := range mine {
		u.mu.Lock()
		if !u.done {
			s.endSession(ctx, u, "group purged")
			n++
		}
		u.mu.Unlock()
	}
	return n
}

func (s *Service) release(ctx context.Context, entryID string) {
	if s.quota == nil {
		return
	}
	if err := s.quota.Release(ctx, entryID); err != nil {
		// Not fatal: the reservation carries the entry's expiry and is
		// forgotten then, so the cost of a lost release is bounded.
		s.logger.WarnContext(ctx, "release storage reservation failed",
			slog.String("entry_id", entryID), slog.Any("error", err))
	}
}

func (s *Service) commit(ctx context.Context, entryID string) {
	if s.quota == nil {
		return
	}
	if err := s.quota.Commit(ctx, entryID); err != nil {
		// The entry is stored and readable; it is only not evictable until it
		// expires. Worth an operator's attention, not the client's.
		s.logger.WarnContext(ctx, "commit storage reservation failed",
			slog.String("entry_id", entryID), slog.Any("error", err))
	}
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
	if meta.Size > s.frameMax {
		return Meta{}, nil, fmt.Errorf("read entry %s: %w: %d bytes do not fit one frame; fetch it in ranges", entryID, ErrTooLarge, meta.Size)
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
		s.forgetMissingBlob(ctx, meta)
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

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err //nolint:wrapcheck // io.Reader's contract: io.EOF must pass through as is
}

// forgetMissingBlob removes an entry whose blob has gone missing while its
// record still stands: deleted from the bucket or the volume by hand, lost
// with a restore, or removed by an operator's lifecycle rule ahead of the
// relay's own sweep.
//
// Left alone, such a record would keep failing every reader for the rest of
// its 24 hours — listed in history, served as the group's latest, counted
// against the storage limit. So it is retired the moment the store answers
// "not found": the record is deleted, its room released, and the group told
// exactly as for an eviction, so open history lists drop it. Only a definite
// not-found does this; a store that is unreachable or erroring changes
// nothing.
func (s *Service) forgetMissingBlob(ctx context.Context, meta Meta) {
	bg := context.WithoutCancel(ctx)
	if err := s.store.Delete(bg, meta.GroupID, meta.ID); err != nil {
		s.logger.ErrorContext(bg, "removing an entry whose blob is missing failed",
			slog.String("entry_id", meta.ID), slog.Any("error", err))
		return
	}
	s.release(bg, meta.ID)
	s.logger.WarnContext(bg, "entry removed: its blob is missing from the store",
		slog.String("group_id", meta.GroupID),
		slog.String("entry_id", meta.ID),
		slog.String("storage_ref", meta.StorageRef))
	if s.onEvict != nil {
		s.onEvict(bg, meta.GroupID, meta.ID)
	}
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
