package tppclient

import (
	"context"
	"fmt"
	"time"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/tppcrypto"
)

// Device is one member of the group, as the relay reports it. It is what a
// revocation dialog renders: a name the user recognises, not an identifier
// (SPEC §3.3 step 2).
type Device struct {
	ID        string
	Name      string
	PublicKey []byte
	CreatedAt time.Time
	LastSeen  time.Time // zero when the device has never connected
	This      bool      // true for the device this client runs on
}

// Roster is the group's device list at one epoch.
type Roster struct {
	Devices []Device
	Epoch   uint64
}

// Find returns the named device.
func (r Roster) Find(deviceID string) (Device, bool) {
	for _, d := range r.Devices {
		if d.ID == deviceID {
			return d, true
		}
	}
	return Device{}, false
}

// CreateGroup turns a creation URL — https://<host>/<token>, the string behind
// the QR code on the admin screen — into a group with this device as its first
// member (SPEC §3.1 steps 4-5).
//
// The device generates the group key locally at epoch 1 and wraps it to
// itself; the relay only ever receives the wrapped form. On success the client
// is connected and ready.
func (c *Client) CreateGroup(ctx context.Context, creationURL string) error {
	base, token, err := splitCreationURL(creationURL)
	if err != nil {
		return err
	}

	c.mu.Lock()
	priv, name := c.state.DevicePrivateKey, c.state.DeviceName
	c.mu.Unlock()

	pub, err := tppcrypto.PublicKey(priv)
	if err != nil {
		return fmt.Errorf("tppclient: derive this device's public key: %w", err)
	}
	groupKey, err := tppcrypto.GenerateGroupKey()
	if err != nil {
		return fmt.Errorf("tppclient: generate the first group key: %w", err)
	}
	// Epoch 1 is stated by the wire contract rather than assumed, but the
	// wrap has to be built before the response exists, so the client wraps at
	// 1 and checks the answer below.
	wrapped, err := tppcrypto.Wrap(groupKey, pub, 1)
	if err != nil {
		return fmt.Errorf("tppclient: wrap the group key to this device: %w", err)
	}

	endpoint, err := websocketURL(base, "")
	if err != nil {
		return err
	}
	// Group creation is one of the two frames an unauthenticated connection
	// may send: it is how this device acquires an identity. The socket is
	// dropped afterwards and the supervisor dials again with the credential.
	conn, err := c.dial(ctx, endpoint)
	if err != nil {
		return err
	}
	defer conn.close("group created")
	go func() { _ = conn.readLoop(ctx, nil) }()

	var resp tppv1.CreateGroupResponse
	err = conn.call(ctx, tppv1.MessageType_MESSAGE_TYPE_CREATE_GROUP_REQUEST, &tppv1.CreateGroupRequest{
		Token:           token,
		DevicePublicKey: pub,
		WrappedGroupKey: wrapped,
		DeviceName:      name,
	}, &resp)
	if err != nil {
		return err
	}
	if resp.GetEpoch() != 1 {
		return fmt.Errorf("tppclient: relay created the group at epoch %d, want 1", resp.GetEpoch())
	}

	c.mu.Lock()
	c.state.ServerURL = base
	c.state.GroupID = resp.GetGroupId()
	c.state.DeviceID = resp.GetDeviceId()
	c.state.Epoch = resp.GetEpoch()
	c.state.GroupKey = groupKey
	c.mu.Unlock()
	if err := c.persist(); err != nil {
		return err
	}
	return c.Connect(ctx)
}

// Devices returns the group roster and its epoch (SPEC §3.3 step 1).
//
// A UI must render this — by name — before it offers to revoke anything, and
// a revocation dialog must not be confirmable until it has.
func (c *Client) Devices(ctx context.Context) (Roster, error) {
	if !c.InGroup() {
		return Roster{}, ErrNoGroup
	}
	var resp tppv1.DeviceListResponse
	if err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_DEVICE_LIST_REQUEST, &tppv1.DeviceListRequest{}, &resp); err != nil {
		return Roster{}, err
	}

	self := c.State().DeviceID
	roster := Roster{Epoch: resp.GetEpoch(), Devices: make([]Device, 0, len(resp.GetDevices()))}
	for _, d := range resp.GetDevices() {
		roster.Devices = append(roster.Devices, Device{
			ID:        d.GetDeviceId(),
			Name:      d.GetName(),
			PublicKey: d.GetPublicKey(),
			CreatedAt: time.UnixMilli(d.GetCreatedAtUnixMs()).UTC(),
			LastSeen:  msToTime(d.GetLastSeenUnixMs()),
			This:      d.GetDeviceId() == self,
		})
	}
	return roster, nil
}

// Revocation is a prepared revocation: the roster the user must be shown, and
// the one method that carries it out.
//
// This split is the library's half of SPEC §3.3 step 2. Revoke gathers what a
// confirmation dialog needs and stops; nothing is revoked and no key is
// generated until the caller calls Confirm. The library never assumes consent,
// and a UI that has not rendered Roster has nothing to confirm with.
type Revocation struct {
	// Roster is the group as it stands, at the epoch the rekey will replace.
	Roster Roster

	// Target is the device that will be removed.
	Target Device

	// Remaining is every device that keeps access, including this one. Each
	// gets its own copy of the new group key.
	Remaining []Device

	client *Client
	done   bool
}

// Revoke prepares the removal of one device and returns the roster the caller
// must show first (SPEC §3.3 steps 1-2). It changes nothing on the relay.
func (c *Client) Revoke(ctx context.Context, deviceID string) (*Revocation, error) {
	state := c.State()
	if !state.InGroup() {
		return nil, ErrNoGroup
	}
	if deviceID == state.DeviceID {
		// The relay refuses this too. A device revoking itself would leave the
		// group with no way to hand the new key to anyone.
		return nil, fmt.Errorf("tppclient: a device may not revoke itself")
	}

	roster, err := c.Devices(ctx)
	if err != nil {
		return nil, err
	}
	target, ok := roster.Find(deviceID)
	if !ok {
		return nil, fmt.Errorf("tppclient: device %s is not in this group: %w", deviceID, ErrNotFound)
	}

	rev := &Revocation{Roster: roster, Target: target, client: c}
	for _, d := range roster.Devices {
		if d.ID != deviceID {
			rev.Remaining = append(rev.Remaining, d)
		}
	}
	return rev, nil
}

// Confirm carries out the revocation the user agreed to (SPEC §3.3 steps 3-5).
//
// It generates a brand-new group key at epoch+1 — never a ratchet from the old
// one, because revocation must be a hard break — wraps it once per remaining
// device with a fresh ephemeral key each time, and sends the whole set in one
// frame. The relay applies all of it or none of it, so a client that dies
// mid-call leaves the group untouched at the old epoch.
//
// The wraps are built against the public keys in Roster: a relay that lied
// about the roster is caught by the user reading the names, not by this code
// (/spec/crypto.md §11.4).
func (r *Revocation) Confirm(ctx context.Context) (Roster, error) {
	if r.done {
		return Roster{}, fmt.Errorf("tppclient: this revocation has already been carried out")
	}
	c := r.client

	newKey, err := tppcrypto.GenerateGroupKey()
	if err != nil {
		return Roster{}, fmt.Errorf("tppclient: generate the group key for the new epoch: %w", err)
	}
	epoch := r.Roster.Epoch + 1

	keys := make([]*tppv1.WrappedKey, 0, len(r.Remaining))
	for _, d := range r.Remaining {
		wrapped, err := tppcrypto.Wrap(newKey, d.PublicKey, epoch)
		if err != nil {
			return Roster{}, fmt.Errorf("tppclient: wrap the new group key for device %s: %w", d.ID, err)
		}
		keys = append(keys, &tppv1.WrappedKey{DeviceId: d.ID, WrappedGroupKey: wrapped})
	}

	var resp tppv1.RekeyResponse
	err = c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_REKEY_REQUEST, &tppv1.RekeyRequest{
		RevokedDeviceId: r.Target.ID,
		ExpectedEpoch:   r.Roster.Epoch,
		WrappedKeys:     keys,
	}, &resp)
	if err != nil {
		// ErrEpochConflict here means another device rekeyed first: the caller
		// refetches the roster and starts again (SPEC §3.3 step 4).
		return Roster{}, err
	}
	if resp.GetEpoch() != epoch {
		return Roster{}, fmt.Errorf("tppclient: relay reports epoch %d after the rekey, expected %d",
			resp.GetEpoch(), epoch)
	}
	r.done = true

	// The revoking device installs its own new key directly rather than
	// waiting for the copy the relay pushes back to it.
	c.setGroupKey(epoch, newKey)
	if err := c.persist(); err != nil {
		return Roster{}, err
	}

	remaining := Roster{Epoch: epoch, Devices: r.Remaining}
	return remaining, nil
}

func msToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
