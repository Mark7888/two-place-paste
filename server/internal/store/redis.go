package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore is the Store implementation backed by Redis (SPEC §4.2).
//
// Every method here writes to the persistent zone: no TTL, must survive a
// restart, and must not be evictable. The deployment guarantees that with AOF
// persistence and `maxmemory-policy volatile-lru`; `allkeys-lru` would let
// Redis drop a group hash under memory pressure and take the whole group with
// it.
type RedisStore struct {
	rdb redis.Cmdable

	// now supplies UTC timestamps. Tests replace it; production never does
	// (docs/conventions.md §5, §6).
	now func() time.Time

	// applyRekey is the rekey script. It is a field so that a test can install
	// a copy with the failpoint armed and prove the epoch survives a partial
	// apply.
	applyRekey *redis.Script
}

// NewRedisStore returns a Store backed by rdb.
func NewRedisStore(rdb redis.Cmdable) *RedisStore {
	return &RedisStore{
		rdb:        rdb,
		now:        func() time.Time { return time.Now().UTC() },
		applyRekey: applyRekeyScript,
	}
}

var _ Store = (*RedisStore)(nil)

// CreateToken implements Store.
func (s *RedisStore) CreateToken(ctx context.Context, name string) (Token, error) {
	tok := Token{Value: newID(), Name: name, CreatedAt: s.now()}

	// Persistent zone: no TTL, never evictable (SPEC §4.2).
	pipe := s.rdb.TxPipeline()
	pipe.HSet(ctx, tokenKey(tok.Value),
		"name", tok.Name,
		"created_at", msString(tok.CreatedAt),
		"used", "0",
		"group_id", "",
	)
	pipe.SAdd(ctx, tokenIndexKey, tok.Value)
	if _, err := pipe.Exec(ctx); err != nil {
		return Token{}, fmt.Errorf("create token: %w", err)
	}
	return tok, nil
}

// ListTokens implements Store.
func (s *RedisStore) ListTokens(ctx context.Context) ([]Token, error) {
	values, err := s.rdb.SMembers(ctx, tokenIndexKey).Result()
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}

	tokens := make([]Token, 0, len(values))
	for _, v := range values {
		tok, err := s.GetToken(ctx, v)
		if errors.Is(err, ErrNotFound) {
			continue // index entry outlived its hash; nothing to report
		}
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, tok)
	}
	sort.Slice(tokens, func(i, j int) bool {
		if tokens[i].CreatedAt.Equal(tokens[j].CreatedAt) {
			return tokens[i].Value < tokens[j].Value
		}
		return tokens[i].CreatedAt.After(tokens[j].CreatedAt)
	})
	return tokens, nil
}

// GetToken implements Store.
func (s *RedisStore) GetToken(ctx context.Context, value string) (Token, error) {
	fields, err := s.rdb.HGetAll(ctx, tokenKey(value)).Result()
	if err != nil {
		return Token{}, fmt.Errorf("get token: %w", err)
	}
	if len(fields) == 0 {
		return Token{}, ErrNotFound
	}
	return Token{
		Value:     value,
		Name:      fields["name"],
		CreatedAt: msTime(fields["created_at"]),
		Used:      fields["used"] == "1",
		GroupID:   fields["group_id"],
	}, nil
}

// ConsumeToken implements Store.
func (s *RedisStore) ConsumeToken(ctx context.Context, token string) (Group, error) {
	created, err := s.createGroup(ctx, token, nil)
	if err != nil {
		return Group{}, err
	}
	return created.Group, nil
}

// CreateGroup implements Store.
func (s *RedisStore) CreateGroup(ctx context.Context, token string, dev NewDevice) (GroupCreation, error) {
	return s.createGroup(ctx, token, &dev)
}

func (s *RedisStore) createGroup(ctx context.Context, token string, dev *NewDevice) (GroupCreation, error) {
	now := s.now()
	deviceID, name, pubkey, wrapped, hasWrapped := "", "", "", "", "0"
	if dev != nil {
		deviceID = newID()
		name = dev.Name
		pubkey = string(dev.PublicKey)
		if dev.WrappedGroupKey != nil {
			wrapped = string(dev.WrappedGroupKey)
			hasWrapped = "1"
		}
	}

	keys := []string{
		tokenKey(token),
		groupKey(token),
		groupDevicesKey(token),
		deviceKey(deviceID),
		wrappedKeyKey(deviceID),
	}
	code, err := createGroupScript.Run(ctx, s.rdb, keys,
		token, msString(now), deviceID, name, pubkey, wrapped, hasWrapped).Int64()
	if err != nil {
		return GroupCreation{}, fmt.Errorf("create group from token: %w", err)
	}
	switch code {
	case 1:
		return GroupCreation{}, ErrNotFound
	case 2:
		return GroupCreation{}, ErrTokenConsumed
	}

	created := GroupCreation{Group: Group{ID: token, Epoch: 1, CreatedAt: now}}
	if dev != nil {
		created.Device = Device{
			ID:        deviceID,
			GroupID:   token,
			Name:      dev.Name,
			PublicKey: dev.PublicKey,
			CreatedAt: now,
		}
	}
	return created, nil
}

// GetGroup implements Store.
func (s *RedisStore) GetGroup(ctx context.Context, groupID string) (Group, error) {
	fields, err := s.rdb.HGetAll(ctx, groupKey(groupID)).Result()
	if err != nil {
		return Group{}, fmt.Errorf("get group %s: %w", groupID, err)
	}
	if len(fields) == 0 {
		return Group{}, ErrNotFound
	}
	epoch, err := strconv.ParseUint(fields["epoch"], 10, 64)
	if err != nil {
		return Group{}, fmt.Errorf("get group %s: parse epoch: %w", groupID, err)
	}
	return Group{ID: groupID, Epoch: epoch, CreatedAt: msTime(fields["created_at"])}, nil
}

// AddDevice implements Store.
func (s *RedisStore) AddDevice(ctx context.Context, groupID string, dev NewDevice) (Device, error) {
	if _, err := s.GetGroup(ctx, groupID); err != nil {
		return Device{}, err
	}

	device := Device{
		ID:        newID(),
		GroupID:   groupID,
		Name:      dev.Name,
		PublicKey: dev.PublicKey,
		CreatedAt: s.now(),
	}

	// Persistent zone: no TTL (SPEC §4.2).
	pipe := s.rdb.TxPipeline()
	pipe.HSet(ctx, deviceKey(device.ID),
		"group_id", groupID,
		"name", device.Name,
		"pubkey", string(device.PublicKey),
		"created_at", msString(device.CreatedAt),
		"last_seen", "0",
	)
	pipe.SAdd(ctx, groupDevicesKey(groupID), device.ID)
	if dev.WrappedGroupKey != nil {
		group, err := s.GetGroup(ctx, groupID)
		if err != nil {
			return Device{}, err
		}
		pipe.HSet(ctx, wrappedKeyKey(device.ID),
			"epoch", strconv.FormatUint(group.Epoch, 10),
			"key", string(dev.WrappedGroupKey),
		)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return Device{}, fmt.Errorf("add device to group %s: %w", groupID, err)
	}
	return device, nil
}

// ListDevices implements Store.
func (s *RedisStore) ListDevices(ctx context.Context, groupID string) ([]Device, error) {
	ids, err := s.rdb.SMembers(ctx, groupDevicesKey(groupID)).Result()
	if err != nil {
		return nil, fmt.Errorf("list devices of group %s: %w", groupID, err)
	}

	devices := make([]Device, 0, len(ids))
	for _, id := range ids {
		dev, err := s.GetDevice(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue // membership set outlived the hash; not a caller's problem
		}
		if err != nil {
			return nil, err
		}
		devices = append(devices, dev)
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].CreatedAt.Equal(devices[j].CreatedAt) {
			return devices[i].ID < devices[j].ID
		}
		return devices[i].CreatedAt.Before(devices[j].CreatedAt)
	})
	return devices, nil
}

// GetDevice implements Store.
func (s *RedisStore) GetDevice(ctx context.Context, deviceID string) (Device, error) {
	if deviceID == "" {
		return Device{}, ErrNotFound
	}
	fields, err := s.rdb.HGetAll(ctx, deviceKey(deviceID)).Result()
	if err != nil {
		return Device{}, fmt.Errorf("get device %s: %w", deviceID, err)
	}
	if len(fields) == 0 {
		return Device{}, ErrNotFound
	}
	return Device{
		ID:        deviceID,
		GroupID:   fields["group_id"],
		Name:      fields["name"],
		PublicKey: []byte(fields["pubkey"]),
		CreatedAt: msTime(fields["created_at"]),
		LastSeen:  msTime(fields["last_seen"]),
	}, nil
}

// GetWrappedKey implements Store.
func (s *RedisStore) GetWrappedKey(ctx context.Context, deviceID string) (WrappedKey, error) {
	fields, err := s.rdb.HGetAll(ctx, wrappedKeyKey(deviceID)).Result()
	if err != nil {
		return WrappedKey{}, fmt.Errorf("get wrapped key of device %s: %w", deviceID, err)
	}
	if len(fields) == 0 {
		return WrappedKey{}, ErrNotFound
	}
	epoch, err := strconv.ParseUint(fields["epoch"], 10, 64)
	if err != nil {
		return WrappedKey{}, fmt.Errorf("get wrapped key of device %s: parse epoch: %w", deviceID, err)
	}
	return WrappedKey{Epoch: epoch, Key: []byte(fields["key"])}, nil
}

// SetWrappedKey implements Store.
func (s *RedisStore) SetWrappedKey(ctx context.Context, deviceID string, key WrappedKey) error {
	// Persistent zone: no TTL. A wrapped key waits for a device that is
	// offline for as long as it takes (SPEC §3.3).
	err := s.rdb.HSet(ctx, wrappedKeyKey(deviceID),
		"epoch", strconv.FormatUint(key.Epoch, 10),
		"key", string(key.Key),
	).Err()
	if err != nil {
		return fmt.Errorf("set wrapped key of device %s: %w", deviceID, err)
	}
	return nil
}

// RemoveDevice implements Store.
func (s *RedisStore) RemoveDevice(ctx context.Context, groupID, deviceID string) error {
	pipe := s.rdb.TxPipeline()
	pipe.SRem(ctx, groupDevicesKey(groupID), deviceID)
	pipe.Del(ctx, deviceKey(deviceID))
	pipe.Del(ctx, wrappedKeyKey(deviceID))
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("remove device %s from group %s: %w", deviceID, groupID, err)
	}
	return nil
}

// ApplyRekey implements Store.
func (s *RedisStore) ApplyRekey(ctx context.Context, groupID, revokedDeviceID string, expectedEpoch uint64, keys []RekeyKey) (Group, error) {
	argv := make([]any, 0, 4+2*len(keys))
	argv = append(argv,
		revokedDeviceID,
		strconv.FormatUint(expectedEpoch, 10),
		devicePrefix,
		wrappedKeySuffix,
	)
	for _, k := range keys {
		argv = append(argv, k.DeviceID, string(k.Key))
	}

	code, err := s.applyRekey.Run(ctx, s.rdb,
		[]string{groupKey(groupID), groupDevicesKey(groupID)}, argv...).Int64()
	if err != nil {
		return Group{}, fmt.Errorf("apply rekey for group %s: %w", groupID, err)
	}
	switch code {
	case 1:
		return Group{}, ErrNotFound
	case 2:
		return Group{}, ErrEpochConflict
	case 3:
		return Group{}, fmt.Errorf("apply rekey for group %s: %w: revoked device %s is not a member", groupID, ErrNotFound, revokedDeviceID)
	case 4:
		return Group{}, ErrInvalidRekey
	}
	return s.GetGroup(ctx, groupID)
}

// CreatePairing implements Store.
func (s *RedisStore) CreatePairing(ctx context.Context, groupID, inviterDeviceID string, ttl time.Duration) (Pairing, error) {
	now := s.now()
	p := Pairing{
		Token:           newID(),
		GroupID:         groupID,
		InviterDeviceID: inviterDeviceID,
		CreatedAt:       now,
		ExpiresAt:       now.Add(ttl),
	}

	// The one key in this package that carries a TTL, and therefore the only
	// one volatile-lru may evict early. Losing a pairing costs a retry.
	pipe := s.rdb.TxPipeline()
	pipe.HSet(ctx, pairingKey(p.Token),
		"group_id", p.GroupID,
		"inviter_device_id", p.InviterDeviceID,
		"created_at", msString(p.CreatedAt),
		"expires_at", msString(p.ExpiresAt),
	)
	pipe.PExpireAt(ctx, pairingKey(p.Token), p.ExpiresAt)
	if _, err := pipe.Exec(ctx); err != nil {
		return Pairing{}, fmt.Errorf("create pairing for group %s: %w", groupID, err)
	}
	return p, nil
}

// GetPairing implements Store.
func (s *RedisStore) GetPairing(ctx context.Context, token string) (Pairing, error) {
	fields, err := s.rdb.HGetAll(ctx, pairingKey(token)).Result()
	if err != nil {
		return Pairing{}, fmt.Errorf("get pairing: %w", err)
	}
	if len(fields) == 0 {
		return Pairing{}, ErrNotFound
	}
	return Pairing{
		Token:           token,
		GroupID:         fields["group_id"],
		InviterDeviceID: fields["inviter_device_id"],
		JoinerDeviceID:  fields["joiner_device_id"],
		CreatedAt:       msTime(fields["created_at"]),
		ExpiresAt:       msTime(fields["expires_at"]),
	}, nil
}

// ConsumePairing implements Store.
func (s *RedisStore) ConsumePairing(ctx context.Context, token, joinerDeviceID string) (Pairing, error) {
	code, err := consumePairingScript.Run(ctx, s.rdb, []string{pairingKey(token)}, joinerDeviceID).Int64()
	if err != nil {
		return Pairing{}, fmt.Errorf("consume pairing: %w", err)
	}
	switch code {
	case 1:
		return Pairing{}, ErrNotFound
	case 2:
		return Pairing{}, ErrPairingConsumed
	}
	return s.GetPairing(ctx, token)
}

// DeletePairing implements Store.
func (s *RedisStore) DeletePairing(ctx context.Context, token string) error {
	if err := s.rdb.Del(ctx, pairingKey(token)).Err(); err != nil {
		return fmt.Errorf("delete pairing: %w", err)
	}
	return nil
}

// CreateOffer implements Store.
func (s *RedisStore) CreateOffer(ctx context.Context, name string, publicKey []byte, ttl time.Duration) (PairingOffer, error) {
	now := s.now()
	o := PairingOffer{
		Code:       newID(),
		DeviceName: name,
		PublicKey:  bytes.Clone(publicKey),
		CreatedAt:  now,
		ExpiresAt:  now.Add(ttl),
	}

	// The second key in this package that carries a TTL, and so the second one
	// volatile-lru may evict early. Losing an offer costs a retry; the device
	// showing it holds nothing yet.
	pipe := s.rdb.TxPipeline()
	pipe.HSet(ctx, offerKey(o.Code),
		"device_name", o.DeviceName,
		"pubkey", string(o.PublicKey),
		"created_at", msString(o.CreatedAt),
		"expires_at", msString(o.ExpiresAt),
	)
	pipe.PExpireAt(ctx, offerKey(o.Code), o.ExpiresAt)
	if _, err := pipe.Exec(ctx); err != nil {
		return PairingOffer{}, fmt.Errorf("create pairing offer: %w", err)
	}
	return o, nil
}

// GetOffer implements Store.
func (s *RedisStore) GetOffer(ctx context.Context, code string) (PairingOffer, error) {
	fields, err := s.rdb.HGetAll(ctx, offerKey(code)).Result()
	if err != nil {
		return PairingOffer{}, fmt.Errorf("get pairing offer: %w", err)
	}
	if len(fields) == 0 {
		return PairingOffer{}, ErrNotFound
	}
	return PairingOffer{
		Code:       code,
		DeviceName: fields["device_name"],
		PublicKey:  []byte(fields["pubkey"]),
		GroupID:    fields["group_id"],
		DeviceID:   fields["device_id"],
		CreatedAt:  msTime(fields["created_at"]),
		ExpiresAt:  msTime(fields["expires_at"]),
	}, nil
}

// AcceptOffer implements Store.
func (s *RedisStore) AcceptOffer(ctx context.Context, code, groupID string, publicKey, wrappedGroupKey []byte) (PairingOffer, Device, error) {
	deviceID := newID()
	now := s.now()

	keys := []string{
		offerKey(code),
		groupKey(groupID),
		groupDevicesKey(groupID),
		deviceKey(deviceID),
		wrappedKeyKey(deviceID),
	}
	result, err := acceptOfferScript.Run(ctx, s.rdb, keys,
		groupID, deviceID, string(publicKey), string(wrappedGroupKey), msString(now)).Int64()
	if err != nil {
		return PairingOffer{}, Device{}, fmt.Errorf("accept pairing offer: %w", err)
	}
	switch result {
	case 1:
		return PairingOffer{}, Device{}, ErrNotFound
	case 2:
		return PairingOffer{}, Device{}, ErrOfferConsumed
	case 3:
		return PairingOffer{}, Device{}, ErrOfferKeyMismatch
	case 4:
		return PairingOffer{}, Device{}, fmt.Errorf("accept pairing offer into group %s: %w", groupID, ErrNotFound)
	}

	offer, err := s.GetOffer(ctx, code)
	if err != nil {
		return PairingOffer{}, Device{}, err
	}
	return offer, Device{
		ID:        deviceID,
		GroupID:   groupID,
		Name:      offer.DeviceName,
		PublicKey: bytes.Clone(offer.PublicKey),
		CreatedAt: now,
	}, nil
}

// DeleteOffer implements Store.
func (s *RedisStore) DeleteOffer(ctx context.Context, code string) error {
	if err := s.rdb.Del(ctx, offerKey(code)).Err(); err != nil {
		return fmt.Errorf("delete pairing offer: %w", err)
	}
	return nil
}

// TouchLastSeen implements Store.
func (s *RedisStore) TouchLastSeen(ctx context.Context, deviceID string) error {
	// HSet on a missing hash would create a device record with only this
	// field, so the write is conditional on the record still existing: a
	// revoked device must not be resurrected by its own reconnect.
	n, err := s.rdb.Exists(ctx, deviceKey(deviceID)).Result()
	if err != nil {
		return fmt.Errorf("touch last seen of device %s: %w", deviceID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := s.rdb.HSet(ctx, deviceKey(deviceID), "last_seen", msString(s.now())).Err(); err != nil {
		return fmt.Errorf("touch last seen of device %s: %w", deviceID, err)
	}
	return nil
}

// msString renders a timestamp as unix milliseconds, UTC
// (docs/conventions.md §6).
func msString(t time.Time) string {
	if t.IsZero() {
		return "0"
	}
	return strconv.FormatInt(t.UTC().UnixMilli(), 10)
}

// msTime parses a unix-millisecond field. An unparsable or zero value yields
// the zero time, which every caller renders as "never".
func msTime(s string) time.Time {
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil || ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
