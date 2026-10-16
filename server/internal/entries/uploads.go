package entries

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
)

// Chunked transfer: an entry larger than one frame is uploaded in chunks and
// read back in ranges (SPEC §4.3). The crypto is untouched — the client seals
// the whole entry and only the transport splits it.
//
// Upload sessions live in this process's memory. The relay is a single
// instance (SPEC §4.1), and a session only ever holds a handle to a staging
// blob, never the bytes, so there is nothing worth surviving a restart: the
// client starts over, and the server releases the room orphaned sessions had
// reserved when it starts (quota.Ledger.ReleasePending).

// Sentinel errors of the chunked path.
var (
	// ErrUploadNotFound is returned for an upload handle that does not exist,
	// has finished, has timed out, or belongs to another device.
	ErrUploadNotFound = errors.New("entries: no such upload")

	// ErrInvalidChunk is returned for a chunk out of order or of the wrong
	// size, and for a commit before every chunk has arrived. It changes
	// nothing: the upload can carry on from where it was.
	ErrInvalidChunk = errors.New("entries: chunk does not fit the upload")

	// ErrUploadMismatch is returned when a commit names an entry id, epoch or
	// size other than the ones the upload was begun with.
	ErrUploadMismatch = errors.New("entries: commit does not match the upload")

	// ErrNotChunked is returned when a chunked upload is begun for an entry
	// small enough to be stored inline; it is sent in one frame instead.
	ErrNotChunked = errors.New("entries: entry is small enough for one frame")

	// ErrTooManyUploads is returned when a device already has the maximum
	// number of uploads in progress.
	ErrTooManyUploads = errors.New("entries: too many uploads in progress")

	// ErrInvalidRange is returned for a ranged fetch that starts past the end
	// of the entry.
	ErrInvalidRange = errors.New("entries: range is outside the entry")
)

// MaxUploadLifetime bounds an upload from begin to commit, however active it
// stays. It is what lets the blob's expiry be fixed at begin while the entry's
// is fixed at commit: the blob is filed this much later than the entry could
// ever expire, so the bucket sweep never takes it first (SPEC §4.5).
const MaxUploadLifetime = time.Hour

// UploadInfo is what BeginUpload hands the client.
type UploadInfo struct {
	Handle     string
	EntryID    string
	ChunkBytes int64
}

// UploadLimits are the numbers a client needs before it uploads.
type UploadLimits struct {
	MaxEntryBytes       int64
	SingleFrameMaxBytes int64
	ChunkBytes          int64
	InlineMaxBytes      int64
}

type uploadSession struct {
	mu sync.Mutex

	handle   string
	groupID  string
	deviceID string
	entryID  string
	epoch    uint64
	size     int64

	started    time.Time
	lastActive time.Time

	up       blob.Upload
	next     uint32
	received int64
	done     bool
}

type uploads struct {
	mu       sync.Mutex
	byHandle map[string]*uploadSession
}

// Limits reports the entry limits a client uploads against.
func (s *Service) Limits() UploadLimits {
	return UploadLimits{
		MaxEntryBytes:       s.maxBytes,
		SingleFrameMaxBytes: s.frameMax,
		ChunkBytes:          s.chunkBytes,
		InlineMaxBytes:      s.inlineMax,
	}
}

// BeginUpload starts a chunked upload of size bytes for the device. The room
// is reserved — and older entries evicted to make it — here, so a full relay
// refuses before a single chunk is sent.
func (s *Service) BeginUpload(ctx context.Context, groupID, deviceID string, epoch uint64, entryID string, size int64) (UploadInfo, error) {
	switch {
	case size <= s.inlineMax:
		return UploadInfo{}, fmt.Errorf("begin upload: %w: %d bytes; send entries of at most %d bytes in one frame", ErrNotChunked, size, s.inlineMax)
	case size > s.maxBytes:
		return UploadInfo{}, fmt.Errorf("begin upload: %w: %d bytes, cap is %d", ErrTooLarge, size, s.maxBytes)
	}
	if entryID == "" {
		entryID = newEntryID()
	} else if !ValidID(entryID) {
		return UploadInfo{}, fmt.Errorf("begin upload: %w: %d bytes outside [A-Za-z0-9_-]{1,%d}", ErrInvalidID, len(entryID), MaxIDLen)
	}

	now := s.now()
	s.ReapUploads(ctx, now)

	s.uploads.mu.Lock()
	active := 0
	for _, u := range s.uploads.byHandle {
		if u.entryID == entryID {
			s.uploads.mu.Unlock()
			return UploadInfo{}, fmt.Errorf("begin upload %s: %w", entryID, ErrEntryExists)
		}
		if u.deviceID == deviceID {
			active++
		}
	}
	s.uploads.mu.Unlock()
	if active >= s.maxUploadsPerDevice {
		return UploadInfo{}, fmt.Errorf("begin upload: %w: %d per device", ErrTooManyUploads, s.maxUploadsPerDevice)
	}

	taken, err := s.store.Exists(ctx, entryID)
	if err != nil {
		return UploadInfo{}, fmt.Errorf("begin upload %s: %w", entryID, err)
	}
	if taken {
		return UploadInfo{}, fmt.Errorf("begin upload %s: %w", entryID, ErrEntryExists)
	}

	// The blob is filed for the latest the entry could expire: committed at
	// the end of the longest upload allowed, then kept for the TTL.
	blobExpiry := now.Add(MaxUploadLifetime + s.ttl)
	if err := s.reserve(ctx, Meta{ID: entryID, GroupID: groupID, ExpiresAt: blobExpiry}, size); err != nil {
		return UploadInfo{}, err
	}
	up, err := s.blobs.BeginUpload(ctx, entryID, blobExpiry, size)
	if err != nil {
		s.release(context.WithoutCancel(ctx), entryID)
		if errors.Is(err, blob.ErrStorageFull) {
			return UploadInfo{}, fmt.Errorf("begin upload %s: %w", entryID, ErrQuotaExceeded)
		}
		return UploadInfo{}, fmt.Errorf("begin upload %s: %w", entryID, err)
	}

	sess := &uploadSession{
		handle:     newUploadHandle(),
		groupID:    groupID,
		deviceID:   deviceID,
		entryID:    entryID,
		epoch:      epoch,
		size:       size,
		started:    now,
		lastActive: now,
		up:         up,
	}
	s.uploads.mu.Lock()
	s.uploads.byHandle[sess.handle] = sess
	s.uploads.mu.Unlock()

	return UploadInfo{Handle: sess.handle, EntryID: entryID, ChunkBytes: s.chunkBytes}, nil
}

// WriteChunk stores one chunk and returns the bytes received so far. Chunks
// arrive in order, each exactly ChunkBytes long but the last.
func (s *Service) WriteChunk(ctx context.Context, groupID, deviceID, handle string, index uint32, data []byte) (int64, error) {
	sess, err := s.session(ctx, groupID, deviceID, handle)
	if err != nil {
		return 0, err
	}
	defer sess.mu.Unlock()

	if index != sess.next {
		return sess.received, fmt.Errorf("upload chunk: %w: got index %d, expected %d", ErrInvalidChunk, index, sess.next)
	}
	want := min(s.chunkBytes, sess.size-sess.received)
	if int64(len(data)) != want {
		return sess.received, fmt.Errorf("upload chunk %d: %w: %d bytes, expected %d", index, ErrInvalidChunk, len(data), want)
	}
	if err := sess.up.Write(ctx, data); err != nil {
		// The staging blob is in an unknown state now; the upload cannot
		// continue from it.
		s.endSession(context.WithoutCancel(ctx), sess, "chunk write failed")
		return 0, fmt.Errorf("upload chunk %d of %s: %w", index, sess.entryID, err)
	}
	sess.next++
	sess.received += int64(len(data))
	sess.lastActive = s.now()
	return sess.received, nil
}

// CommitUpload completes an upload and records its entry, which becomes the
// group's latest now. entryID may be empty; epoch and size must match the
// upload's.
func (s *Service) CommitUpload(ctx context.Context, groupID, deviceID, handle, entryID string, epoch uint64, size int64) (Meta, error) {
	return s.CommitUploadWithHeader(ctx, groupID, deviceID, handle, entryID, epoch, size, nil)
}

// CommitUploadWithHeader is CommitUpload with the writer's sealed entry
// header, stored with the entry.
func (s *Service) CommitUploadWithHeader(ctx context.Context, groupID, deviceID, handle, entryID string, epoch uint64, size int64, header []byte) (Meta, error) {
	if err := checkHeader(header); err != nil {
		return Meta{}, fmt.Errorf("commit upload: %w", err)
	}
	sess, err := s.session(ctx, groupID, deviceID, handle)
	if err != nil {
		return Meta{}, err
	}
	defer sess.mu.Unlock()

	if (entryID != "" && entryID != sess.entryID) || epoch != sess.epoch || size != sess.size {
		return Meta{}, fmt.Errorf("commit upload: %w", ErrUploadMismatch)
	}
	if sess.received != sess.size {
		return Meta{}, fmt.Errorf("commit upload %s: %w: %d of %d bytes received", sess.entryID, ErrInvalidChunk, sess.received, sess.size)
	}

	bg := context.WithoutCancel(ctx)
	ref, err := sess.up.Complete(ctx)
	if err != nil {
		s.endSession(bg, sess, "")
		if errors.Is(err, blob.ErrExists) {
			return Meta{}, fmt.Errorf("commit upload %s: %w", sess.entryID, ErrEntryExists)
		}
		return Meta{}, fmt.Errorf("commit upload %s: %w", sess.entryID, err)
	}

	now := s.now()
	meta := Meta{
		ID:         sess.entryID,
		GroupID:    sess.groupID,
		Epoch:      sess.epoch,
		Size:       sess.size,
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.ttl),
		StorageRef: ref,
		Header:     header,
	}
	if err := s.store.Put(ctx, meta, nil); err != nil {
		_ = s.blobs.Delete(bg, ref)
		s.endSession(bg, sess, "")
		return Meta{}, fmt.Errorf("commit upload %s: %w", sess.entryID, err)
	}
	s.commit(bg, sess.entryID)
	s.forget(sess)
	return meta, nil
}

// AbortUpload abandons an upload and releases its room. An unknown handle is
// not an error: the upload may have timed out or finished already.
func (s *Service) AbortUpload(ctx context.Context, groupID, deviceID, handle string) error {
	sess, err := s.session(ctx, groupID, deviceID, handle)
	if errors.Is(err, ErrUploadNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	defer sess.mu.Unlock()
	s.endSession(context.WithoutCancel(ctx), sess, "")
	return nil
}

// ReapUploads abandons every upload idle for longer than the upload timeout or
// older than MaxUploadLifetime, and reports how many it took. The sweep runs
// it, and so does every BeginUpload.
func (s *Service) ReapUploads(ctx context.Context, now time.Time) int {
	s.uploads.mu.Lock()
	all := make([]*uploadSession, 0, len(s.uploads.byHandle))
	for _, u := range s.uploads.byHandle {
		all = append(all, u)
	}
	s.uploads.mu.Unlock()

	n := 0
	for _, u := range all {
		u.mu.Lock()
		if !u.done && s.expired(u, now) {
			s.endSession(context.WithoutCancel(ctx), u, "timed out")
			n++
		}
		u.mu.Unlock()
	}
	return n
}

// UploadsInProgress reports how many chunked uploads are in progress and how
// many bytes of storage they hold reserved. It is for the admin dashboard.
func (s *Service) UploadsInProgress() (count int, reserved int64) {
	s.uploads.mu.Lock()
	defer s.uploads.mu.Unlock()
	for _, u := range s.uploads.byHandle {
		count++
		reserved += u.size // fixed at begin; safe to read without u.mu
	}
	return count, reserved
}

// AbortAllUploads abandons every upload in progress. The server calls it on
// shutdown, so that the room they reserved is released now rather than at the
// next start.
func (s *Service) AbortAllUploads(ctx context.Context) {
	s.uploads.mu.Lock()
	all := make([]*uploadSession, 0, len(s.uploads.byHandle))
	for _, u := range s.uploads.byHandle {
		all = append(all, u)
	}
	s.uploads.mu.Unlock()
	for _, u := range all {
		u.mu.Lock()
		if !u.done {
			s.endSession(ctx, u, "server shutting down")
		}
		u.mu.Unlock()
	}
}

// FetchRange returns length bytes of an entry's ciphertext from offset, with
// its metadata. A length of zero or above ChunkBytes means ChunkBytes; the
// range is cut at the end of the entry.
func (s *Service) FetchRange(ctx context.Context, groupID, entryID string, offset, length int64) (Meta, []byte, error) {
	meta, err := s.store.Get(ctx, groupID, entryID)
	if err != nil {
		return Meta{}, nil, fmt.Errorf("read entry %s: %w", entryID, err)
	}
	if offset < 0 || offset >= meta.Size {
		return Meta{}, nil, fmt.Errorf("read entry %s: %w: offset %d of %d bytes", entryID, ErrInvalidRange, offset, meta.Size)
	}
	if length <= 0 || length > s.chunkBytes {
		length = s.chunkBytes
	}
	length = min(length, meta.Size-offset)

	if meta.Inline() {
		body, err := s.store.InlineBody(ctx, meta.ID)
		if err != nil {
			return Meta{}, nil, fmt.Errorf("read inline body of entry %s: %w", meta.ID, err)
		}
		if int64(len(body)) < offset+length {
			return Meta{}, nil, fmt.Errorf("read entry %s: inline body is %d bytes, record says %d", meta.ID, len(body), meta.Size)
		}
		return meta, body[offset : offset+length], nil
	}

	rc, err := s.blobs.GetRange(ctx, meta.StorageRef, offset, length)
	if errors.Is(err, blob.ErrNotFound) {
		s.forgetMissingBlob(ctx, meta)
		return Meta{}, nil, ErrNotFound
	}
	if err != nil {
		return Meta{}, nil, fmt.Errorf("read entry %s: %w", meta.ID, err)
	}
	defer rc.Close()
	// Read through a limit rather than into a buffer sized up front: length
	// is clamped to the chunk size above, and this way no allocation is ever
	// sized from a request, whatever happens to that clamp later.
	data, err := io.ReadAll(io.LimitReader(rc, length))
	if err != nil {
		return Meta{}, nil, fmt.Errorf("read entry %s: %w", meta.ID, err)
	}
	if int64(len(data)) != length {
		return Meta{}, nil, fmt.Errorf("read entry %s: %w", meta.ID, io.ErrUnexpectedEOF)
	}
	return meta, data, nil
}

// session finds a live upload of this device and returns it locked. Another
// device's handle is reported as not found: handles are not capabilities.
func (s *Service) session(ctx context.Context, groupID, deviceID, handle string) (*uploadSession, error) {
	s.uploads.mu.Lock()
	sess, ok := s.uploads.byHandle[handle]
	s.uploads.mu.Unlock()
	if !ok || sess.deviceID != deviceID || sess.groupID != groupID {
		return nil, fmt.Errorf("upload: %w", ErrUploadNotFound)
	}
	sess.mu.Lock()
	if sess.done {
		sess.mu.Unlock()
		return nil, fmt.Errorf("upload: %w", ErrUploadNotFound)
	}
	if s.expired(sess, s.now()) {
		s.endSession(context.WithoutCancel(ctx), sess, "timed out")
		sess.mu.Unlock()
		return nil, fmt.Errorf("upload: %w", ErrUploadNotFound)
	}
	return sess, nil
}

func (s *Service) expired(u *uploadSession, now time.Time) bool {
	return now.Sub(u.lastActive) > s.uploadTTL || now.Sub(u.started) > MaxUploadLifetime
}

// endSession aborts an upload and releases its room. The caller holds u.mu.
func (s *Service) endSession(ctx context.Context, u *uploadSession, why string) {
	if err := u.up.Abort(ctx); err != nil {
		s.logger.WarnContext(ctx, "abort upload failed; the blob sweep will clean up after it",
			slog.String("entry_id", u.entryID), slog.Any("error", err))
	}
	s.release(ctx, u.entryID)
	if why != "" {
		s.logger.InfoContext(ctx, "upload abandoned",
			slog.String("group_id", u.groupID),
			slog.String("entry_id", u.entryID),
			slog.String("reason", why))
	}
	s.forget(u)
}

// forget marks an upload finished and drops it. The caller holds u.mu.
func (s *Service) forget(u *uploadSession) {
	u.done = true
	s.uploads.mu.Lock()
	delete(s.uploads.byHandle, u.handle)
	s.uploads.mu.Unlock()
}

func newUploadHandle() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("entries: crypto/rand: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
