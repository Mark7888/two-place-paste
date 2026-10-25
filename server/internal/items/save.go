package items

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// Saving an item (SPEC §4.6, proto/tpp/v1/store.proto). It mirrors an entry's
// upload: room is reserved first, the blob is written, and the record is
// committed last, so that a crash leaves a pending record the startup and the
// sweep release, never a listed item without its blob.

// maxUploadLifetime bounds an upload from begin to commit, as for entries.
const maxUploadLifetime = time.Hour

// maxUploadsPerDevice bounds the store uploads one device may have going.
const maxUploadsPerDevice = 4

// maxIDLen bounds a client-chosen item id.
const maxIDLen = 64

// validID reports whether an id may be used as an item id: the alphabet of
// base64url, 1 to 64 bytes. The blob is filed under it, so nothing else is
// allowed in.
func validID(id string) bool {
	if id == "" || len(id) > maxIDLen {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// PutRequest is a save: an item's blob in one frame, or the commit of its
// chunked upload.
type PutRequest struct {
	ItemID      string
	Epoch       uint64
	Size        int64
	WrappedKeys []byte
	SealedMeta  []byte

	// Exactly one of Ciphertext and UploadHandle is set.
	Ciphertext   []byte
	UploadHandle string
}

// UploadInfo is what BeginUpload hands the client.
type UploadInfo struct {
	Handle     string
	ChunkBytes int64
}

type uploadSession struct {
	mu sync.Mutex

	handle   string
	groupID  string
	deviceID string
	itemID   string
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

// checkSize refuses an item larger than the per-item cap, or than the
// group's store on its own: no amount of deleting could make it fit there.
func (s *Service) checkSize(ctx context.Context, g store.Group, size int64) (store.ItemLimits, error) {
	if size <= 0 {
		return store.ItemLimits{}, fmt.Errorf("item size %d: %w", size, ErrInvalid)
	}
	if size > s.cfg.MaxItemBytes {
		return store.ItemLimits{}, fmt.Errorf("item of %d bytes, cap is %d: %w", size, s.cfg.MaxItemBytes, ErrTooLarge)
	}
	limits, err := s.storeLimits(ctx, g)
	if err != nil {
		return store.ItemLimits{}, err
	}
	for _, l := range []int64{limits.GroupBytes, limits.TotalBytes} {
		if l > 0 && size+RecordBytes > l {
			return store.ItemLimits{}, fmt.Errorf("item of %d bytes, store limit is %d: %w", size, l, ErrTooLarge)
		}
	}
	return limits, nil
}

// reserve claims room for an item of g.
func (s *Service) reserve(ctx context.Context, g store.Group, itemID string, epoch uint64, size int64) error {
	limits, err := s.checkSize(ctx, g, size)
	if err != nil {
		return err
	}
	err = s.reg.ReserveItem(ctx, store.ItemReservation{
		GroupID:   g.ID,
		ItemID:    itemID,
		Epoch:     epoch,
		Size:      size,
		Accounted: size + RecordBytes,
		StartedAt: s.now(),
	}, limits)
	if err != nil {
		return fmt.Errorf("reserve item %s: %w", itemID, err)
	}
	return nil
}

// release gives back the room of an item that will not be committed, and
// deletes whatever blob it left. Neither is fatal: the startup release and
// the sweep catch what this misses.
func (s *Service) release(ctx context.Context, groupID, itemID string, blobWritten bool) {
	if _, err := s.reg.ReleaseItem(ctx, groupID, itemID); err != nil {
		s.logger.WarnContext(ctx, "release store reservation failed",
			slog.String("item_id", itemID), slog.Any("error", err))
	}
	if blobWritten {
		s.deleteBlob(ctx, groupID, itemID)
	}
}

// BeginUpload starts a chunked upload of an item. Room is reserved here, so
// a full store refuses before any chunk is sent.
func (s *Service) BeginUpload(ctx context.Context, groupID, deviceID, itemID string, epoch uint64, size int64) (UploadInfo, error) {
	if !validID(itemID) {
		return UploadInfo{}, fmt.Errorf("begin upload: item id: %w", ErrInvalid)
	}
	g, err := s.enabledGroup(ctx, groupID)
	if err != nil {
		return UploadInfo{}, err
	}
	now := s.now()
	s.ReapUploads(ctx, now)

	s.uploads.mu.Lock()
	active := 0
	for _, u := range s.uploads.byHandle {
		if u.groupID == groupID && u.itemID == itemID {
			s.uploads.mu.Unlock()
			return UploadInfo{}, fmt.Errorf("begin upload of %s: %w", itemID, store.ErrItemExists)
		}
		if u.deviceID == deviceID {
			active++
		}
	}
	s.uploads.mu.Unlock()
	if active >= maxUploadsPerDevice {
		return UploadInfo{}, fmt.Errorf("begin upload: %w: %d per device", ErrTooManyUploads, maxUploadsPerDevice)
	}

	if err := s.reserve(ctx, g, itemID, epoch, size); err != nil {
		return UploadInfo{}, err
	}
	up, err := s.blobs.BeginItemUpload(ctx, groupID, itemID, size)
	if err != nil {
		s.release(context.WithoutCancel(ctx), groupID, itemID, false)
		if errors.Is(err, blob.ErrStorageFull) {
			return UploadInfo{}, fmt.Errorf("begin upload of %s: %w", itemID, ErrStorageFull)
		}
		return UploadInfo{}, fmt.Errorf("begin upload of %s: %w", itemID, err)
	}
	sess := &uploadSession{
		handle:     newUploadHandle(),
		groupID:    groupID,
		deviceID:   deviceID,
		itemID:     itemID,
		epoch:      epoch,
		size:       size,
		started:    now,
		lastActive: now,
		up:         up,
	}
	s.uploads.mu.Lock()
	s.uploads.byHandle[sess.handle] = sess
	s.uploads.mu.Unlock()
	return UploadInfo{Handle: sess.handle, ChunkBytes: s.cfg.ChunkBytes}, nil
}

// OwnsUpload reports whether a handle is one of the store's uploads, so the
// transport can route the shared chunk and abort messages.
func (s *Service) OwnsUpload(handle string) bool {
	s.uploads.mu.Lock()
	defer s.uploads.mu.Unlock()
	_, ok := s.uploads.byHandle[handle]
	return ok
}

// WriteChunk stores one chunk and returns the bytes received so far.
func (s *Service) WriteChunk(ctx context.Context, groupID, deviceID, handle string, index uint32, data []byte) (int64, error) {
	sess, err := s.session(ctx, groupID, deviceID, handle)
	if err != nil {
		return 0, err
	}
	defer sess.mu.Unlock()

	if index != sess.next {
		return sess.received, fmt.Errorf("upload chunk: %w: got index %d, expected %d", ErrInvalidChunk, index, sess.next)
	}
	want := min(s.cfg.ChunkBytes, sess.size-sess.received)
	if int64(len(data)) != want {
		return sess.received, fmt.Errorf("upload chunk %d: %w: %d bytes, expected %d", index, ErrInvalidChunk, len(data), want)
	}
	if err := sess.up.Write(ctx, data); err != nil {
		s.endSession(context.WithoutCancel(ctx), sess, "chunk write failed")
		return 0, fmt.Errorf("upload chunk %d of %s: %w", index, sess.itemID, err)
	}
	sess.next++
	sess.received += int64(len(data))
	sess.lastActive = s.now()
	return sess.received, nil
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

// Put saves an item, in one frame or as the commit of its chunked upload.
// The item is listed, and the group's other devices told, only once this
// succeeds.
func (s *Service) Put(ctx context.Context, groupID, deviceID string, req PutRequest) (store.Item, error) {
	if !validID(req.ItemID) {
		return store.Item{}, fmt.Errorf("save item: item id: %w", ErrInvalid)
	}
	if len(req.WrappedKeys) != WrappedItemKeysSize || len(req.SealedMeta) != SealedMetaSize {
		return store.Item{}, fmt.Errorf("save item %s: keys or metadata of the wrong length: %w", req.ItemID, ErrInvalid)
	}
	if (req.UploadHandle == "") == (req.Ciphertext == nil) {
		return store.Item{}, fmt.Errorf("save item %s: give the blob or an upload handle: %w", req.ItemID, ErrInvalid)
	}

	var (
		it  store.Item
		err error
	)
	if req.UploadHandle != "" {
		it, err = s.commitUpload(ctx, groupID, deviceID, req)
	} else {
		it, err = s.putOne(ctx, groupID, req)
	}
	if err != nil {
		return store.Item{}, err
	}
	s.logger.InfoContext(ctx, "store item saved",
		slog.String("group_id", groupID), slog.String("item_id", it.ID), slog.Int64("size", it.Size))
	if s.hooks.OnSaved != nil {
		s.hooks.OnSaved(ctx, groupID, deviceID, it)
	}
	return it, nil
}

// putOne saves an item whose blob came in one frame.
func (s *Service) putOne(ctx context.Context, groupID string, req PutRequest) (store.Item, error) {
	if int64(len(req.Ciphertext)) != req.Size {
		return store.Item{}, fmt.Errorf("save item %s: %d bytes, declared %d: %w", req.ItemID, len(req.Ciphertext), req.Size, ErrInvalid)
	}
	if req.Size > s.cfg.SingleFrameMaxBytes {
		return store.Item{}, fmt.Errorf("save item %s: %d bytes in one frame: %w", req.ItemID, req.Size, ErrTooLarge)
	}
	g, err := s.enabledGroup(ctx, groupID)
	if err != nil {
		return store.Item{}, err
	}
	if err := s.reserve(ctx, g, req.ItemID, req.Epoch, req.Size); err != nil {
		return store.Item{}, err
	}
	bg := context.WithoutCancel(ctx)
	if err := s.blobs.PutItem(ctx, groupID, req.ItemID, req.Size, bytes.NewReader(req.Ciphertext)); err != nil {
		s.release(bg, groupID, req.ItemID, false)
		switch {
		case errors.Is(err, blob.ErrStorageFull):
			return store.Item{}, fmt.Errorf("save item %s: %w", req.ItemID, ErrStorageFull)
		case errors.Is(err, blob.ErrExists):
			return store.Item{}, fmt.Errorf("save item %s: %w", req.ItemID, store.ErrItemExists)
		}
		return store.Item{}, fmt.Errorf("save item %s: %w", req.ItemID, err)
	}
	return s.commit(ctx, groupID, req)
}

// commitUpload completes a chunked upload and commits its item.
func (s *Service) commitUpload(ctx context.Context, groupID, deviceID string, req PutRequest) (store.Item, error) {
	sess, err := s.session(ctx, groupID, deviceID, req.UploadHandle)
	if err != nil {
		return store.Item{}, err
	}
	defer sess.mu.Unlock()
	if req.ItemID != sess.itemID || req.Epoch != sess.epoch || req.Size != sess.size {
		return store.Item{}, fmt.Errorf("commit upload: %w", ErrUploadMismatch)
	}
	if sess.received != sess.size {
		return store.Item{}, fmt.Errorf("commit upload of %s: %w: %d of %d bytes received", sess.itemID, ErrInvalidChunk, sess.received, sess.size)
	}
	bg := context.WithoutCancel(ctx)
	if _, err := sess.up.Complete(ctx); err != nil {
		s.endSession(bg, sess, "")
		if errors.Is(err, blob.ErrExists) {
			return store.Item{}, fmt.Errorf("commit upload of %s: %w", sess.itemID, store.ErrItemExists)
		}
		return store.Item{}, fmt.Errorf("commit upload of %s: %w", sess.itemID, err)
	}
	s.forget(sess)
	return s.commit(ctx, groupID, req)
}

// commit writes a reserved item's keys and metadata once its blob is in
// place. A store turned off, being emptied or re-keyed in the meantime ends
// the save: its room and its blob are given back.
func (s *Service) commit(ctx context.Context, groupID string, req PutRequest) (store.Item, error) {
	it, err := s.reg.CommitItem(ctx, store.ItemCommit{
		GroupID:     groupID,
		ItemID:      req.ItemID,
		Epoch:       req.Epoch,
		WrappedKeys: req.WrappedKeys,
		SealedMeta:  req.SealedMeta,
		SavedAt:     s.now(),
	})
	if err != nil {
		s.release(context.WithoutCancel(ctx), groupID, req.ItemID, true)
		return store.Item{}, fmt.Errorf("save item %s: %w", req.ItemID, err)
	}
	return it, nil
}

// ReapUploads abandons every upload idle for longer than the upload timeout
// or older than an hour, and reports how many it took.
func (s *Service) ReapUploads(ctx context.Context, now time.Time) int {
	n := 0
	for _, u := range s.allSessions() {
		u.mu.Lock()
		if !u.done && s.expired(u, now) {
			s.endSession(context.WithoutCancel(ctx), u, "timed out")
			n++
		}
		u.mu.Unlock()
	}
	return n
}

// AbortAllUploads abandons every upload in progress. The server calls it on
// shutdown, so their room is given back now rather than at the next start.
func (s *Service) AbortAllUploads(ctx context.Context) {
	for _, u := range s.allSessions() {
		u.mu.Lock()
		if !u.done {
			s.endSession(ctx, u, "server shutting down")
		}
		u.mu.Unlock()
	}
}

// abortGroupUploads abandons every upload of one group.
func (s *Service) abortGroupUploads(ctx context.Context, groupID string) {
	for _, u := range s.allSessions() {
		if u.groupID != groupID {
			continue
		}
		u.mu.Lock()
		if !u.done {
			s.endSession(ctx, u, "store emptied")
		}
		u.mu.Unlock()
	}
}

func (s *Service) allSessions() []*uploadSession {
	s.uploads.mu.Lock()
	defer s.uploads.mu.Unlock()
	all := make([]*uploadSession, 0, len(s.uploads.byHandle))
	for _, u := range s.uploads.byHandle {
		all = append(all, u)
	}
	return all
}

// ReleaseOrphans gives back the room of every item a previous process left
// pending, and deletes whatever blob it left. The server calls it once at
// startup, before it takes traffic.
func (s *Service) ReleaseOrphans(ctx context.Context) (int, error) {
	orphans, err := s.reg.ReleaseAllPending(ctx)
	for _, it := range orphans {
		s.deleteBlob(ctx, it.GroupID, it.ID)
	}
	if err != nil {
		return len(orphans), fmt.Errorf("release orphaned store items: %w", err)
	}
	return len(orphans), nil
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
	return now.Sub(u.lastActive) > s.cfg.UploadTTL || now.Sub(u.started) > maxUploadLifetime
}

// endSession aborts an upload and releases its room. The caller holds u.mu.
func (s *Service) endSession(ctx context.Context, u *uploadSession, why string) {
	if err := u.up.Abort(ctx); err != nil {
		s.logger.WarnContext(ctx, "abort store upload failed; the sweep will clean up after it",
			slog.String("item_id", u.itemID), slog.Any("error", err))
	}
	s.release(ctx, u.groupID, u.itemID, false)
	if why != "" {
		s.logger.InfoContext(ctx, "store upload abandoned",
			slog.String("group_id", u.groupID), slog.String("item_id", u.itemID), slog.String("reason", why))
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
		panic("items: crypto/rand: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
