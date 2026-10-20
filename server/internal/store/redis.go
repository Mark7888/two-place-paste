package store

import (
	"bytes"
	"context"
	"encoding/json"
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
	// (docs/internal/conventions.md §5, §6).
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
	return s.CreateTokenWith(ctx, NewToken{Name: name})
}

// CreateTokenWith implements Store.
func (s *RedisStore) CreateTokenWith(ctx context.Context, t NewToken) (Token, error) {
	tok := Token{
		Value:     newID(),
		Name:      t.Name,
		Note:      t.Note,
		Tag:       t.Tag,
		ExpiresAt: t.ExpiresAt.UTC(),
		CreatedAt: s.now(),
	}

	// Persistent zone: no TTL, never evictable (SPEC §4.2). An expiring token
	// still carries none: it is refused by the creation script once past its
	// expiry, and kept so the listing can say it expired.
	pipe := s.rdb.TxPipeline()
	pipe.HSet(ctx, tokenKey(tok.Value),
		"name", tok.Name,
		"created_at", msString(tok.CreatedAt),
		"used", "0",
		"group_id", "",
		"note", tok.Note,
		"tag", tok.Tag,
		"expires_at", msString(tok.ExpiresAt),
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
		Value:        value,
		Name:         fields["name"],
		CreatedAt:    msTime(fields["created_at"]),
		Used:         fields["used"] == "1",
		GroupID:      fields["group_id"],
		Note:         fields["note"],
		Tag:          fields["tag"],
		ExpiresAt:    msTime(fields["expires_at"]),
		GroupDeleted: fields["group_deleted"] == "1",
	}, nil
}

// RevokeToken implements Store.
func (s *RedisStore) RevokeToken(ctx context.Context, value string) error {
	code, err := revokeTokenScript.Run(ctx, s.rdb, []string{tokenKey(value), tokenIndexKey}, value).Int64()
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	switch code {
	case 0:
		return nil
	case 1:
		return ErrNotFound
	case 2:
		return ErrTokenConsumed
	default:
		return fmt.Errorf("revoke token: unexpected script result %d", code)
	}
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
		token, msString(now), deviceID, name, pubkey, wrapped, hasWrapped, tagPrefix).Int64()
	if err != nil {
		return GroupCreation{}, fmt.Errorf("create group from token: %w", err)
	}
	switch code {
	case 1:
		return GroupCreation{}, ErrNotFound
	case 2:
		return GroupCreation{}, ErrTokenConsumed
	}

	group, err := s.GetGroup(ctx, token)
	if err != nil {
		return GroupCreation{}, fmt.Errorf("create group from token: read it back: %w", err)
	}
	created := GroupCreation{Group: group}
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
	g := Group{
		ID:        groupID,
		Epoch:     epoch,
		CreatedAt: msTime(fields["created_at"]),
		Name:      fields["name"],
		Tag:       fields["tag"],
		Limit:     limitFromFields(fields),
	}
	if g.Name == "" {
		// A group from before names existed: it reads as the token that
		// created it, whose value is the group id (SPEC §3.1 step 5).
		name, err := s.rdb.HGet(ctx, tokenKey(groupID), "name").Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return Group{}, fmt.Errorf("get group %s: read token name: %w", groupID, err)
		}
		g.Name = name
	}
	return g, nil
}

func limitFromFields(fields map[string]string) LimitOverride {
	n, _ := strconv.ParseInt(fields["limit_bytes"], 10, 64)
	return LimitOverride{Bytes: max(n, 0), Enabled: fields["limit_on"] == "1"}
}

func limitArgs(l LimitOverride) []any {
	on := "0"
	if l.Enabled {
		on = "1"
	}
	return []any{"limit_bytes", strconv.FormatInt(max(l.Bytes, 0), 10), "limit_on", on}
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
// (docs/internal/conventions.md §6).
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

// ListGroups implements Store.
func (s *RedisStore) ListGroups(ctx context.Context) ([]Group, error) {
	tokens, err := s.ListTokens(ctx)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	groups := make([]Group, 0, len(tokens))
	for _, t := range tokens {
		if !t.Used || t.GroupDeleted {
			continue
		}
		g, err := s.GetGroup(ctx, t.GroupID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	sortGroups(groups)
	return groups, nil
}

func sortGroups(groups []Group) {
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].CreatedAt.Equal(groups[j].CreatedAt) {
			return groups[i].ID < groups[j].ID
		}
		return groups[i].CreatedAt.Before(groups[j].CreatedAt)
	})
}

// setIfExists runs setIfExistsScript and maps a missing hash to ErrNotFound.
func (s *RedisStore) setIfExists(ctx context.Context, key string, fields ...any) error {
	code, err := setIfExistsScript.Run(ctx, s.rdb, []string{key}, fields...).Int64()
	if err != nil {
		return err
	}
	if code == 1 {
		return ErrNotFound
	}
	return nil
}

// RenameGroup implements Store.
func (s *RedisStore) RenameGroup(ctx context.Context, groupID, name string) error {
	if err := s.setIfExists(ctx, groupKey(groupID), "name", name); err != nil {
		return fmt.Errorf("rename group %s: %w", groupID, err)
	}
	return nil
}

// SetGroupTag implements Store.
func (s *RedisStore) SetGroupTag(ctx context.Context, groupID, tag string) error {
	code, err := setGroupTagScript.Run(ctx, s.rdb, []string{groupKey(groupID), tagKey(tag)}, tag).Int64()
	if err != nil {
		return fmt.Errorf("set tag of group %s: %w", groupID, err)
	}
	switch code {
	case 1:
		return fmt.Errorf("set tag of group %s: %w", groupID, ErrNotFound)
	case 2:
		return fmt.Errorf("set tag of group %s: tag: %w", groupID, ErrNotFound)
	}
	return nil
}

// SetGroupLimit implements Store.
func (s *RedisStore) SetGroupLimit(ctx context.Context, groupID string, limit LimitOverride) error {
	if err := s.setIfExists(ctx, groupKey(groupID), limitArgs(limit)...); err != nil {
		return fmt.Errorf("set storage limit of group %s: %w", groupID, err)
	}
	return nil
}

// DeleteGroup implements Store.
func (s *RedisStore) DeleteGroup(ctx context.Context, groupID string) (DeletedGroup, error) {
	group, err := s.GetGroup(ctx, groupID)
	if err != nil {
		return DeletedGroup{}, fmt.Errorf("delete group %s: %w", groupID, err)
	}
	reply, err := deleteGroupScript.Run(ctx, s.rdb,
		[]string{groupKey(groupID), groupDevicesKey(groupID), tokenKey(groupID)},
		devicePrefix, wrappedKeySuffix, tombstonePrefix,
		strconv.FormatInt(TombstoneTTL.Milliseconds(), 10), msString(s.now()),
	).Slice()
	if err != nil {
		return DeletedGroup{}, fmt.Errorf("delete group %s: %w", groupID, err)
	}
	if len(reply) == 0 {
		return DeletedGroup{}, fmt.Errorf("delete group %s: empty script reply", groupID)
	}
	if code, _ := reply[0].(int64); code == 1 {
		return DeletedGroup{}, fmt.Errorf("delete group %s: %w", groupID, ErrNotFound)
	}
	out := DeletedGroup{Group: group, DeviceIDs: make([]string, 0, len(reply)-1)}
	for _, v := range reply[1:] {
		if id, ok := v.(string); ok {
			out.DeviceIDs = append(out.DeviceIDs, id)
		}
	}
	return out, nil
}

// DeviceTombstoned implements Store.
func (s *RedisStore) DeviceTombstoned(ctx context.Context, deviceID string) (bool, error) {
	if deviceID == "" {
		return false, nil
	}
	n, err := s.rdb.Exists(ctx, tombstoneKey(deviceID)).Result()
	if err != nil {
		return false, fmt.Errorf("read tombstone of device %s: %w", deviceID, err)
	}
	return n == 1, nil
}

// ListTags implements Store.
func (s *RedisStore) ListTags(ctx context.Context) ([]Tag, error) {
	names, err := s.rdb.SMembers(ctx, tagIndexKey).Result()
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	tags := make([]Tag, 0, len(names))
	for _, name := range names {
		t, err := s.GetTag(ctx, name)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Name < tags[j].Name })
	return tags, nil
}

// GetTag implements Store.
func (s *RedisStore) GetTag(ctx context.Context, name string) (Tag, error) {
	if name == "" {
		return Tag{}, ErrNotFound
	}
	fields, err := s.rdb.HGetAll(ctx, tagKey(name)).Result()
	if err != nil {
		return Tag{}, fmt.Errorf("get tag: %w", err)
	}
	if len(fields) == 0 {
		return Tag{}, ErrNotFound
	}
	return Tag{Name: name, Limit: limitFromFields(fields), CreatedAt: msTime(fields["created_at"])}, nil
}

// CreateTag implements Store.
func (s *RedisStore) CreateTag(ctx context.Context, name string) (Tag, error) {
	t := Tag{Name: name, CreatedAt: s.now()}
	// HSETNX on created_at is the existence check and the claim in one step.
	ok, err := s.rdb.HSetNX(ctx, tagKey(name), "created_at", msString(t.CreatedAt)).Result()
	if err != nil {
		return Tag{}, fmt.Errorf("create tag: %w", err)
	}
	if !ok {
		return Tag{}, ErrTagExists
	}
	// Persistent zone: no TTL (SPEC §4.2).
	pipe := s.rdb.TxPipeline()
	pipe.HSet(ctx, tagKey(name), limitArgs(t.Limit)...)
	pipe.SAdd(ctx, tagIndexKey, name)
	if _, err := pipe.Exec(ctx); err != nil {
		return Tag{}, fmt.Errorf("create tag: %w", err)
	}
	return t, nil
}

// SetTagLimit implements Store.
func (s *RedisStore) SetTagLimit(ctx context.Context, name string, limit LimitOverride) error {
	if err := s.setIfExists(ctx, tagKey(name), limitArgs(limit)...); err != nil {
		return fmt.Errorf("set storage limit of tag: %w", err)
	}
	return nil
}

// DeleteTag implements Store.
func (s *RedisStore) DeleteTag(ctx context.Context, name string) error {
	n, err := s.rdb.Del(ctx, tagKey(name)).Result()
	if err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	if err := s.rdb.SRem(ctx, tagIndexKey, name).Err(); err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	// Every group was a token first and shares its id, so one walk of the
	// token index reaches both the groups under the tag and the unused tokens
	// that would hand it out.
	values, err := s.rdb.SMembers(ctx, tokenIndexKey).Result()
	if err != nil {
		return fmt.Errorf("delete tag: list tokens: %w", err)
	}
	for _, v := range values {
		for _, key := range []string{tokenKey(v), groupKey(v)} {
			if err := clearTagScript.Run(ctx, s.rdb, []string{key}, name).Err(); err != nil {
				return fmt.Errorf("delete tag: untag %s: %w", key, err)
			}
		}
	}
	return nil
}

// GetSettings implements Store.
func (s *RedisStore) GetSettings(ctx context.Context) (Settings, error) {
	fields, err := s.rdb.HGetAll(ctx, settingsKey).Result()
	if err != nil {
		return Settings{}, fmt.Errorf("get settings: %w", err)
	}
	return settingsFromFields(fields), nil
}

func settingsFromFields(fields map[string]string) Settings {
	st := Settings{StaleAfter: DefaultStaleAfter}
	if ms, err := strconv.ParseInt(fields["stale_after_ms"], 10, 64); err == nil && ms > 0 {
		st.StaleAfter = time.Duration(ms) * time.Millisecond
	}
	return st
}

// PutSettings implements Store.
func (s *RedisStore) PutSettings(ctx context.Context, st Settings) error {
	// Persistent zone: no TTL (SPEC §4.2).
	err := s.rdb.HSet(ctx, settingsKey, "stale_after_ms", strconv.FormatInt(st.StaleAfter.Milliseconds(), 10)).Err()
	if err != nil {
		return fmt.Errorf("put settings: %w", err)
	}
	return nil
}

// AppendAudit implements Store.
func (s *RedisStore) AppendAudit(ctx context.Context, e AuditEntry) error {
	if e.At.IsZero() {
		e.At = s.now()
	}
	e.At = e.At.UTC()
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("append audit entry: %w", err)
	}
	// No TTL: the list is bounded by length instead (SPEC §4.2).
	pipe := s.rdb.TxPipeline()
	pipe.LPush(ctx, auditKey, raw)
	pipe.LTrim(ctx, auditKey, 0, AuditLimit-1)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("append audit entry: %w", err)
	}
	return nil
}

// ListAudit implements Store.
func (s *RedisStore) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > AuditLimit {
		limit = AuditLimit
	}
	raws, err := s.rdb.LRange(ctx, auditKey, 0, int64(limit-1)).Result()
	if err != nil {
		return nil, fmt.Errorf("list audit entries: %w", err)
	}
	out := make([]AuditEntry, 0, len(raws))
	for _, raw := range raws {
		var e AuditEntry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			continue // a malformed entry is not worth hiding the rest for
		}
		out = append(out, e)
	}
	return out, nil
}
