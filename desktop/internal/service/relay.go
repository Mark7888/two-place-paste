// Package service wires the desktop shell together: the client core from
// pkg/tppclient, the OS clipboard, the settings file and the login item.
//
// It holds the parts that are genuinely this application's decisions — which
// direction a sync goes (SPEC §6), when a clipboard change is the user's and
// when it is this service's own write, and what the UI is allowed to confirm
// (SPEC §3.3 step 2) — and none of the parts that are the protocol's. Every
// key and every frame stays inside pkg/tppclient.
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// Relay is the slice of pkg/tppclient this service uses.
//
// It is an interface for one reason: the sync direction rules of SPEC §6 and
// the revocation flow of SPEC §3.3 are this package's logic, and they deserve
// tests that do not need a running relay, a Redis and a keystore to say
// whether they are right.
type Relay interface {
	// State returns the client's persisted state. It carries secrets and is
	// used here only for the identifiers a UI shows.
	State() tppclient.State

	// InGroup reports whether this device can talk to a group.
	InGroup() bool

	// Epoch is the group key generation this device holds.
	Epoch() uint64

	// Connect starts the supervised connection.
	Connect(ctx context.Context) error

	// Reconnect drops the connection and dials again at once, for a socket
	// known to be stale before the keepalive can tell: after a sleep.
	Reconnect()

	// CreateGroup turns an admin creation URL into a group (SPEC §3.1).
	CreateGroup(ctx context.Context, creationURL string) error

	// Forget discards this device's group, group key and keypair, leaving the
	// client as a first launch would. It is local: the relay still lists this
	// device until another one revokes it.
	Forget() error

	// StartPairing mints a pairing token (SPEC §3.2 step 1).
	StartPairing(ctx context.Context) (*tppclient.Invitation, error)

	// JoinPairing joins with a payload from another device.
	JoinPairing(ctx context.Context, payload string) error

	// StartOffer shows a code from a device that is in no group yet, for a
	// member to accept (docs/plans/joiner-emitted-pairing.md).
	StartOffer(ctx context.Context, serverURL string) (Offer, error)

	// PrepareAcceptOffer decodes an offer and changes nothing.
	PrepareAcceptOffer(ctx context.Context, code string) (OfferAcceptance, error)

	// Devices returns the group roster (SPEC §3.3 step 1).
	Devices(ctx context.Context) (tppclient.Roster, error)

	// Revoke prepares a revocation and changes nothing.
	Revoke(ctx context.Context, deviceID string) (Revocation, error)

	// PutEntry stores an entry as the group's latest.
	PutEntry(ctx context.Context, item tppclient.Item) (tppclient.EntryMeta, error)

	// GetLatest returns the newest entry, decrypted.
	GetLatest(ctx context.Context) (tppclient.Item, error)

	// GetHistory lists entry metadata, newest first.
	GetHistory(ctx context.Context, q tppclient.HistoryQuery) (tppclient.History, error)

	// GetEntry returns one entry by id, decrypted.
	GetEntry(ctx context.Context, entryID string) (tppclient.Item, error)
}

// Offer is a pairing offer this device is showing while it waits for a member
// to accept it. It is the library's Offer narrowed to what a UI needs.
type Offer interface {
	// Code is the string to render as a QR code and to show as text. The two
	// are one string.
	Code() string

	// ExpiresAt is when the relay stops holding the offer.
	ExpiresAt() time.Time

	// Wait blocks until a member accepts, installs the group key and brings
	// the client online.
	Wait(ctx context.Context) error

	// Close gives up an offer the user is no longer showing.
	Close()
}

// OfferAcceptance is a scanned or pasted offer waiting for the user's consent,
// the mirror of Revocation below and split for a sharper reason: accepting
// hands over the group key, so nothing is wrapped until Confirm.
type OfferAcceptance interface {
	// DeviceName is what the offering device calls itself. Display text from a
	// device that is not in the group yet; it proves nothing.
	DeviceName() string

	// Fingerprint is the offered public key rendered for a person to compare
	// with what that device is showing. This is the part that identifies it.
	Fingerprint() string

	// Confirm admits the device (docs/plans/joiner-emitted-pairing.md §3
	// step 5).
	Confirm(ctx context.Context) (tppclient.Device, error)
}

// Revocation is a prepared revocation waiting for the user's consent. It is
// the library's Revocation narrowed to what a UI needs, so that this package
// can be tested without one.
type Revocation interface {
	// Roster is the group as it stands, which the dialog must render.
	Roster() tppclient.Roster

	// Target is the device to be removed.
	Target() tppclient.Device

	// Remaining is every device that keeps access.
	Remaining() []tppclient.Device

	// Confirm carries the revocation out (SPEC §3.3 steps 3-5).
	Confirm(ctx context.Context) (tppclient.Roster, error)
}

// NewRelay adapts a client to Relay. The adaptation is one method deep, and
// only for the three that return a struct whose Confirm or Wait is a gate this
// package must be able to fake: Revoke, StartOffer and PrepareAcceptOffer.
func NewRelay(c *tppclient.Client) Relay { return clientRelay{c} }

type clientRelay struct{ *tppclient.Client }

func (c clientRelay) Revoke(ctx context.Context, deviceID string) (Revocation, error) {
	rev, err := c.Client.Revoke(ctx, deviceID)
	if err != nil {
		return nil, fmt.Errorf("service: prepare the revocation of device %s: %w", deviceID, err)
	}
	return clientRevocation{rev}, nil
}

func (c clientRelay) StartOffer(ctx context.Context, serverURL string) (Offer, error) {
	offer, err := c.Client.StartOffer(ctx, serverURL)
	if err != nil {
		return nil, fmt.Errorf("service: show a pairing offer: %w", err)
	}
	return clientOffer{offer}, nil
}

type clientOffer struct{ offer *tppclient.Offer }

func (o clientOffer) Code() string         { return o.offer.Code }
func (o clientOffer) ExpiresAt() time.Time { return o.offer.ExpiresAt }
func (o clientOffer) Close()               { o.offer.Close() }

func (o clientOffer) Wait(ctx context.Context) error {
	if err := o.offer.Wait(ctx); err != nil {
		return fmt.Errorf("service: wait for a member to accept the pairing offer: %w", err)
	}
	return nil
}

func (c clientRelay) PrepareAcceptOffer(ctx context.Context, code string) (OfferAcceptance, error) {
	accept, err := c.Client.PrepareAcceptOffer(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("service: read that pairing offer: %w", err)
	}
	return clientAcceptance{accept}, nil
}

type clientAcceptance struct{ accept *tppclient.OfferAcceptance }

func (a clientAcceptance) DeviceName() string  { return a.accept.DeviceName }
func (a clientAcceptance) Fingerprint() string { return a.accept.Fingerprint }

func (a clientAcceptance) Confirm(ctx context.Context) (tppclient.Device, error) {
	device, err := a.accept.Confirm(ctx)
	if err != nil {
		return tppclient.Device{}, fmt.Errorf("service: admit the offering device: %w", err)
	}
	return device, nil
}

type clientRevocation struct{ rev *tppclient.Revocation }

func (r clientRevocation) Roster() tppclient.Roster      { return r.rev.Roster }
func (r clientRevocation) Target() tppclient.Device      { return r.rev.Target }
func (r clientRevocation) Remaining() []tppclient.Device { return r.rev.Remaining }

func (r clientRevocation) Confirm(ctx context.Context) (tppclient.Roster, error) {
	roster, err := r.rev.Confirm(ctx)
	if err != nil {
		return tppclient.Roster{}, fmt.Errorf("service: carry out the revocation: %w", err)
	}
	return roster, nil
}
