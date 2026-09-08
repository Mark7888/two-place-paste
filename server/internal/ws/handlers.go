package ws

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/server/internal/entries"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// dispatch routes one decoded envelope to its handler and returns the frame to
// send back, if any.
//
// Only two message types are legal without a device identity: they are the two
// ways a device acquires one (SPEC §3.1, §3.2). Everything else needs an
// authenticated connection.
func (s *Server) dispatch(ctx context.Context, c *conn, env *tppv1.Envelope) ([]byte, error) {
	switch env.GetType() {
	case tppv1.MessageType_MESSAGE_TYPE_CREATE_GROUP_REQUEST:
		return s.handleCreateGroup(ctx, c, env)
	case tppv1.MessageType_MESSAGE_TYPE_PAIRING_JOIN_REQUEST:
		return s.handlePairingJoin(ctx, c, env)
	}

	deviceID, groupID := c.identity()
	if deviceID == "" {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED,
			"this connection has no device identity")
	}

	switch env.GetType() {
	case tppv1.MessageType_MESSAGE_TYPE_PAIRING_START_REQUEST:
		return s.handlePairingStart(ctx, env, deviceID, groupID)
	case tppv1.MessageType_MESSAGE_TYPE_PAIRING_WRAPPED_KEY_UPLOAD:
		return s.handlePairingWrappedKey(ctx, env, deviceID, groupID)
	case tppv1.MessageType_MESSAGE_TYPE_DEVICE_LIST_REQUEST:
		return s.handleDeviceList(ctx, env, groupID)
	case tppv1.MessageType_MESSAGE_TYPE_REKEY_REQUEST:
		return s.handleRekey(ctx, env, deviceID, groupID)
	case tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST:
		return s.handleEntryPut(ctx, env, groupID)
	case tppv1.MessageType_MESSAGE_TYPE_ENTRY_LATEST_REQUEST:
		return s.handleEntryLatest(ctx, env, groupID)
	case tppv1.MessageType_MESSAGE_TYPE_ENTRY_HISTORY_REQUEST:
		return s.handleEntryHistory(ctx, env, groupID)
	case tppv1.MessageType_MESSAGE_TYPE_ENTRY_FETCH_REQUEST:
		return s.handleEntryFetch(ctx, env, groupID)
	default:
		// An unknown type is answered, never met with a closed socket
		// (SPEC §5.1).
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_UNSUPPORTED_MESSAGE,
			"message type %s is not handled by the server", env.GetType())
	}
}

// handleCreateGroup turns a creation token into a group and its first device
// (SPEC §3.1 steps 4-5).
func (s *Server) handleCreateGroup(ctx context.Context, c *conn, env *tppv1.Envelope) ([]byte, error) {
	var req tppv1.CreateGroupRequest
	if err := decode(env, &req); err != nil {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "payload could not be decoded")
	}
	if req.GetToken() == "" || len(req.GetDevicePublicKey()) == 0 || len(req.GetWrappedGroupKey()) == 0 {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"token, device_public_key and wrapped_group_key are all required")
	}

	created, err := s.store.CreateGroup(ctx, req.GetToken(), store.NewDevice{
		Name:            req.GetDeviceName(),
		PublicKey:       req.GetDevicePublicKey(),
		WrappedGroupKey: req.GetWrappedGroupKey(),
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_NOT_FOUND, "no such creation token")
	case errors.Is(err, store.ErrTokenConsumed):
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_TOKEN_CONSUMED, "this token has already created a group")
	case err != nil:
		return nil, fmt.Errorf("create group from token: %w", err)
	}

	c.setIdentity(created.Device.ID, created.Group.ID)
	s.hub.register(c)
	s.logger.InfoContext(ctx, "group created",
		slog.String("group_id", created.Group.ID),
		slog.String("device_id", created.Device.ID))

	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_CREATE_GROUP_RESPONSE, &tppv1.CreateGroupResponse{
		GroupId:  created.Group.ID,
		DeviceId: created.Device.ID,
		Epoch:    created.Group.Epoch,
	})
}

// handlePairingStart mints a pairing token for an already-paired device
// (SPEC §3.2 step 1).
func (s *Server) handlePairingStart(ctx context.Context, env *tppv1.Envelope, deviceID, groupID string) ([]byte, error) {
	p, err := s.store.CreatePairing(ctx, groupID, deviceID, s.pairingTTL)
	if err != nil {
		return nil, fmt.Errorf("start pairing: %w", err)
	}
	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_PAIRING_START_RESPONSE, &tppv1.PairingStartResponse{
		PairingToken:    p.Token,
		ExpiresAtUnixMs: p.ExpiresAt.UnixMilli(),
	})
}

// handlePairingJoin registers a joining device and notifies the inviter
// (SPEC §3.2 steps 2-3).
//
// The joiner gets no response here: it is waiting for PairingComplete, which
// arrives once the inviter has wrapped the group key for it. Only a failure is
// answered on this envelope.
func (s *Server) handlePairingJoin(ctx context.Context, c *conn, env *tppv1.Envelope) ([]byte, error) {
	var req tppv1.PairingJoinRequest
	if err := decode(env, &req); err != nil {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "payload could not be decoded")
	}
	if req.GetPairingToken() == "" || len(req.GetDevicePublicKey()) == 0 {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"pairing_token and device_public_key are both required")
	}

	pairing, err := s.store.GetPairing(ctx, req.GetPairingToken())
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Expired and never-existed are the same answer; the token is short
		// lived by design (SPEC §3.2).
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_TOKEN_EXPIRED, "pairing token is expired or unknown")
	case err != nil:
		return nil, fmt.Errorf("look up pairing: %w", err)
	}
	if pairing.JoinerDeviceID != "" {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_TOKEN_CONSUMED, "pairing token has already been used")
	}

	device, err := s.store.AddDevice(ctx, pairing.GroupID, store.NewDevice{
		Name:      req.GetDeviceName(),
		PublicKey: req.GetDevicePublicKey(),
	})
	if err != nil {
		return nil, fmt.Errorf("add joining device: %w", err)
	}

	// The claim on the token is what makes it single-use, and it is taken
	// after the device exists so the notice can name it. A loser of the race
	// leaves nothing behind.
	if _, err := s.store.ConsumePairing(ctx, req.GetPairingToken(), device.ID); err != nil {
		s.removeDevice(ctx, pairing.GroupID, device.ID)
		if errors.Is(err, store.ErrPairingConsumed) {
			return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_TOKEN_CONSUMED, "pairing token has already been used")
		}
		if errors.Is(err, store.ErrNotFound) {
			return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_TOKEN_EXPIRED, "pairing token is expired or unknown")
		}
		return nil, fmt.Errorf("claim pairing token: %w", err)
	}

	notice, err := encode(newCorrelationID(), tppv1.MessageType_MESSAGE_TYPE_PAIRING_JOIN_NOTICE, &tppv1.PairingJoinNotice{
		PairingToken:    pairing.Token,
		DeviceId:        device.ID,
		DeviceName:      device.Name,
		DevicePublicKey: device.PublicKey,
	})
	if err != nil {
		s.removeDevice(ctx, pairing.GroupID, device.ID)
		return nil, err
	}
	if !s.hub.sendTo(pairing.InviterDeviceID, notice) {
		// Step 4 needs the inviter online: only it holds the group key. The
		// join is undone so the user can simply try again.
		s.removeDevice(ctx, pairing.GroupID, device.ID)
		_ = s.store.DeletePairing(ctx, pairing.Token)
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_NOT_FOUND, "the inviting device is not connected")
	}

	c.setIdentity(device.ID, pairing.GroupID)
	s.hub.register(c)
	s.logger.InfoContext(ctx, "device joined pairing",
		slog.String("group_id", pairing.GroupID),
		slog.String("device_id", device.ID))
	return nil, nil
}

// handlePairingWrappedKey relays the inviter's wrapped group key to the joiner
// (SPEC §3.2 steps 4-5). The server cannot unwrap it.
func (s *Server) handlePairingWrappedKey(ctx context.Context, env *tppv1.Envelope, deviceID, groupID string) ([]byte, error) {
	var req tppv1.PairingWrappedKeyUpload
	if err := decode(env, &req); err != nil {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "payload could not be decoded")
	}
	if len(req.GetWrappedGroupKey()) == 0 {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "wrapped_group_key is required")
	}

	pairing, err := s.store.GetPairing(ctx, req.GetPairingToken())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_TOKEN_EXPIRED, "pairing token is expired or unknown")
	case err != nil:
		return nil, fmt.Errorf("look up pairing: %w", err)
	}
	if pairing.InviterDeviceID != deviceID {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_PERMISSION_DENIED, "only the inviting device may complete this pairing")
	}
	if pairing.JoinerDeviceID == "" || pairing.JoinerDeviceID != req.GetDeviceId() {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_NOT_FOUND, "no device has joined with this pairing token")
	}

	group, err := s.store.GetGroup(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("read group: %w", err)
	}
	if err := s.store.SetWrappedKey(ctx, pairing.JoinerDeviceID, store.WrappedKey{
		Epoch: group.Epoch,
		Key:   req.GetWrappedGroupKey(),
	}); err != nil {
		return nil, fmt.Errorf("store wrapped key for the joining device: %w", err)
	}
	if err := s.store.DeletePairing(ctx, pairing.Token); err != nil {
		s.logger.WarnContext(ctx, "deleting completed pairing failed", slog.Any("error", err))
	}

	complete := &tppv1.PairingComplete{
		GroupId:         groupID,
		DeviceId:        pairing.JoinerDeviceID,
		Epoch:           group.Epoch,
		WrappedGroupKey: req.GetWrappedGroupKey(),
	}
	joinerFrame, err := encode(newCorrelationID(), tppv1.MessageType_MESSAGE_TYPE_PAIRING_COMPLETE, complete)
	if err != nil {
		return nil, err
	}
	// A joiner that has already gone away picks its key up on next connect,
	// exactly like a device that missed a rekey (SPEC §3.3).
	s.hub.sendTo(pairing.JoinerDeviceID, joinerFrame)

	s.logger.InfoContext(ctx, "device paired",
		slog.String("group_id", groupID),
		slog.String("device_id", pairing.JoinerDeviceID))

	// The same message acknowledges the inviter's upload, echoing its id: the
	// wire contract has no separate ack and the inviter authored every field.
	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_PAIRING_COMPLETE, complete)
}

// handleDeviceList returns the roster a client must render before it may offer
// to revoke anything (SPEC §3.3 steps 1-2).
func (s *Server) handleDeviceList(ctx context.Context, env *tppv1.Envelope, groupID string) ([]byte, error) {
	group, err := s.store.GetGroup(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("read group: %w", err)
	}
	devices, err := s.store.ListDevices(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}

	resp := &tppv1.DeviceListResponse{Epoch: group.Epoch, Devices: make([]*tppv1.Device, 0, len(devices))}
	for _, d := range devices {
		resp.Devices = append(resp.Devices, &tppv1.Device{
			DeviceId:        d.ID,
			Name:            d.Name,
			PublicKey:       d.PublicKey,
			CreatedAtUnixMs: d.CreatedAt.UnixMilli(),
			LastSeenUnixMs:  unixMilliOrZero(d.LastSeen),
		})
	}
	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_DEVICE_LIST_RESPONSE, resp)
}

// handleRekey revokes one device and re-keys every remaining one atomically
// (SPEC §3.3 steps 4-5).
func (s *Server) handleRekey(ctx context.Context, env *tppv1.Envelope, deviceID, groupID string) ([]byte, error) {
	var req tppv1.RekeyRequest
	if err := decode(env, &req); err != nil {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "payload could not be decoded")
	}
	if req.GetRevokedDeviceId() == "" {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "revoked_device_id is required")
	}
	if req.GetRevokedDeviceId() == deviceID {
		// A device revoking itself would leave the group with no way to hand
		// the new key to anyone, including the caller.
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "a device may not revoke itself")
	}

	keys := make([]store.RekeyKey, 0, len(req.GetWrappedKeys()))
	for _, k := range req.GetWrappedKeys() {
		if k.GetDeviceId() == "" || len(k.GetWrappedGroupKey()) == 0 {
			return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "every wrapped key needs a device id and a key")
		}
		keys = append(keys, store.RekeyKey{DeviceID: k.GetDeviceId(), Key: k.GetWrappedGroupKey()})
	}

	group, err := s.store.ApplyRekey(ctx, groupID, req.GetRevokedDeviceId(), req.GetExpectedEpoch(), keys)
	switch {
	case errors.Is(err, store.ErrEpochConflict):
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT,
			"the group has moved past epoch %d; refetch the roster and retry", req.GetExpectedEpoch())
	case errors.Is(err, store.ErrInvalidRekey):
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"the request must carry exactly one wrapped key per remaining device")
	case errors.Is(err, store.ErrNotFound):
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_NOT_FOUND, "no such device in this group")
	case err != nil:
		return nil, fmt.Errorf("apply rekey: %w", err)
	}

	s.logger.InfoContext(ctx, "device revoked",
		slog.String("group_id", groupID),
		slog.String("device_id", req.GetRevokedDeviceId()),
		slog.Uint64("epoch", group.Epoch))

	// The revoked device's socket goes down immediately after the store has
	// deleted its record, so the credential and the connection die together
	// (SPEC §3.3 step 5).
	s.hub.closeDevice(req.GetRevokedDeviceId(), s.logger)

	s.fanOutRekey(ctx, groupID, req.GetRevokedDeviceId(), group.Epoch, keys)

	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_REKEY_RESPONSE, &tppv1.RekeyResponse{Epoch: group.Epoch})
}

// fanOutRekey tells the group what happened and hands each remaining device
// its own copy of the new key. A device that is offline receives nothing here
// and picks its key up on connect (SPEC §3.3).
func (s *Server) fanOutRekey(ctx context.Context, groupID, revokedID string, epoch uint64, keys []store.RekeyKey) {
	events := []struct {
		typ tppv1.MessageType
		msg proto.Message
	}{
		{tppv1.MessageType_MESSAGE_TYPE_EPOCH_CHANGED, &tppv1.EpochChanged{Epoch: epoch, RevokedDeviceId: revokedID}},
		{tppv1.MessageType_MESSAGE_TYPE_DEVICE_REVOKED, &tppv1.DeviceRevoked{DeviceId: revokedID, Epoch: epoch}},
	}
	for _, e := range events {
		frame, err := encode(newCorrelationID(), e.typ, e.msg)
		if err != nil {
			s.logger.ErrorContext(ctx, "encoding rekey event failed", slog.Any("error", err))
			continue
		}
		s.hub.broadcast(groupID, frame, revokedID)
	}

	for _, k := range keys {
		frame, err := encode(newCorrelationID(), tppv1.MessageType_MESSAGE_TYPE_WRAPPED_KEY_AVAILABLE,
			&tppv1.WrappedKeyAvailable{Epoch: epoch, WrappedGroupKey: k.Key})
		if err != nil {
			s.logger.ErrorContext(ctx, "encoding wrapped key event failed", slog.Any("error", err))
			continue
		}
		s.hub.sendTo(k.DeviceID, frame)
	}
}

// handleEntryPut stores one encrypted clipboard item (SPEC §4.3, §6).
func (s *Server) handleEntryPut(ctx context.Context, env *tppv1.Envelope, groupID string) ([]byte, error) {
	var req tppv1.EntryPutRequest
	if err := decode(env, &req); err != nil {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "payload could not be decoded")
	}
	if req.GetUploadHandle() != "" {
		// The handle is reserved in the wire contract for an out-of-band
		// upload path that the MVP does not need: a 10 MB ciphertext fits in
		// one binary frame, and a second path would be a second place to get
		// the size cap wrong.
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_UNSUPPORTED_MESSAGE,
			"upload_handle is reserved; send the ciphertext inline")
	}

	group, err := s.store.GetGroup(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("read group: %w", err)
	}
	if req.GetEpoch() != group.Epoch {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT,
			"entry is encrypted under epoch %d but the group runs at %d", req.GetEpoch(), group.Epoch)
	}

	ciphertext := req.GetCiphertext()
	declared := int64(req.GetSize())
	if declared == 0 {
		declared = int64(len(ciphertext))
	}

	meta, err := s.entries.Put(ctx, groupID, req.GetEpoch(), declared, bytes.NewReader(ciphertext))
	switch {
	case errors.Is(err, entries.ErrTooLarge):
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_TOO_LARGE,
			"ciphertext of %d bytes exceeds the per-entry cap of %d", declared, s.entries.MaxBytes())
	case errors.Is(err, entries.ErrSizeMismatch):
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"declared size %d does not match the %d bytes received", declared, len(ciphertext))
	case err != nil:
		return nil, fmt.Errorf("put entry: %w", err)
	}

	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_RESPONSE, &tppv1.EntryPutResponse{
		Meta: entryMeta(meta),
	})
}

// handleEntryLatest returns the group's newest entry. The ciphertext travels
// with it only when it is stored inline; otherwise the client fetches it
// (SPEC §4.3, §6).
func (s *Server) handleEntryLatest(ctx context.Context, env *tppv1.Envelope, groupID string) ([]byte, error) {
	meta, ciphertext, err := s.entries.Latest(ctx, groupID)
	if errors.Is(err, entries.ErrNotFound) {
		// An empty group is not an error: a new device starts empty and must
		// be told so plainly (SPEC §3.2).
		return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_ENTRY_LATEST_RESPONSE, &tppv1.EntryLatestResponse{})
	}
	if err != nil {
		return nil, fmt.Errorf("read latest entry: %w", err)
	}
	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_ENTRY_LATEST_RESPONSE, &tppv1.EntryLatestResponse{
		Meta:       entryMeta(meta),
		Ciphertext: ciphertext,
	})
}

// handleEntryHistory lists entry metadata. History is only ever pulled; the
// server never pushes it (SPEC §6).
func (s *Server) handleEntryHistory(ctx context.Context, env *tppv1.Envelope, groupID string) ([]byte, error) {
	var req tppv1.EntryHistoryRequest
	if err := decode(env, &req); err != nil {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "payload could not be decoded")
	}

	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = s.defaultHistoryLimit
	}
	if limit > s.maxHistoryLimit {
		limit = s.maxHistoryLimit
	}

	var before time.Time
	if ms := req.GetBeforeUnixMs(); ms > 0 {
		before = time.UnixMilli(ms).UTC()
	}

	metas, next, err := s.entries.History(ctx, groupID, limit, before)
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}

	resp := &tppv1.EntryHistoryResponse{Entries: make([]*tppv1.EntryMeta, 0, len(metas))}
	for _, m := range metas {
		resp.Entries = append(resp.Entries, entryMeta(m))
	}
	resp.NextBeforeUnixMs = unixMilliOrZero(next)
	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_ENTRY_HISTORY_RESPONSE, resp)
}

// handleEntryFetch pulls one entry's ciphertext by id.
func (s *Server) handleEntryFetch(ctx context.Context, env *tppv1.Envelope, groupID string) ([]byte, error) {
	var req tppv1.EntryFetchRequest
	if err := decode(env, &req); err != nil {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "payload could not be decoded")
	}

	meta, ciphertext, err := s.entries.Fetch(ctx, groupID, req.GetEntryId())
	if errors.Is(err, entries.ErrNotFound) {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_NOT_FOUND, "no such entry")
	}
	if err != nil {
		return nil, fmt.Errorf("fetch entry: %w", err)
	}
	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_ENTRY_FETCH_RESPONSE, &tppv1.EntryFetchResponse{
		Meta:       entryMeta(meta),
		Ciphertext: ciphertext,
	})
}

// removeDevice undoes a half-finished pairing. A failure is logged and not
// propagated: the caller is already returning an error to the client.
func (s *Server) removeDevice(ctx context.Context, groupID, deviceID string) {
	if err := s.store.RemoveDevice(ctx, groupID, deviceID); err != nil {
		s.logger.ErrorContext(ctx, "rolling back a failed pairing failed",
			slog.String("group_id", groupID),
			slog.String("device_id", deviceID),
			slog.Any("error", err))
	}
}

// entryMeta converts internal metadata to the wire form. Content type,
// filename and plaintext size are inside the ciphertext and appear nowhere
// here (SPEC §2.3).
func entryMeta(m entries.Meta) *tppv1.EntryMeta {
	return &tppv1.EntryMeta{
		EntryId:         m.ID,
		Epoch:           m.Epoch,
		Size:            uint64(m.Size),
		CreatedAtUnixMs: m.CreatedAt.UnixMilli(),
		ExpiresAtUnixMs: m.ExpiresAt.UnixMilli(),
		Inline:          m.Inline(),
	}
}

func unixMilliOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// newCorrelationID returns the id of a server-pushed event. It correlates with
// nothing; it exists so that every frame on the wire has one (SPEC §5.1).
func newCorrelationID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ws: crypto/rand: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
