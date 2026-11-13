package tppclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
)

// ProtocolLevel is the protocol level this build speaks (SPEC §5.3). It rises
// only for a change that cannot stay compatible; a compatible relay feature is
// a capability instead, which RelayInfo.Has checks for.
const ProtocolLevel uint32 = 1

// protocolParam is the WebSocket URL parameter the level travels in. It is on
// the URL rather than in a frame so a relay can refuse an app too old for it
// before the upgrade.
const protocolParam = "proto"

// outdatedRetry is how long a client the relay refused for its age waits
// before it asks again. The answer only changes when the app or the relay is
// updated, so the normal reconnect backoff would only produce a stream of
// refusals; Reconnect cuts the wait short, which is what a shell calls once
// the app has updated or the user asks.
const outdatedRetry = time.Hour

// Sentinel conditions of SPEC §5.3. ErrClientOutdated, the relay telling this
// build to update, is declared with the other relay error codes in store.go:
// a refusal at the upgrade wraps it too, so one check covers both.
var (
	// ErrRelayOutdated means the relay lacks something this build needs for
	// what was asked: a capability, or a protocol level. Everything else keeps
	// working. Only the relay's operator can fix it.
	ErrRelayOutdated = errors.New("tppclient: the relay is too old for this")

	// ErrCodeFromNewerApp means a pairing code was made by a newer version of
	// the app, in a form this build cannot read. The user updates this app.
	ErrCodeFromNewerApp = errors.New("tppclient: this pairing code was made by a newer version of the app")
)

// OutdatedError is the relay refusing this build at the upgrade because its
// protocol level is below the relay's minimum (SPEC §5.3). It matches
// ErrClientOutdated, and it is never a revocation: the device's record and
// keys are untouched.
type OutdatedError struct {
	// ProtocolMin and ProtocolMax are the levels the relay accepts.
	ProtocolMin, ProtocolMax uint32
}

func (e *OutdatedError) Error() string {
	return fmt.Sprintf("tppclient: the relay needs protocol level %d or newer, and this app speaks %d",
		e.ProtocolMin, ProtocolLevel)
}

// Unwrap makes errors.Is(err, ErrClientOutdated) hold.
func (e *OutdatedError) Unwrap() error { return ErrClientOutdated }

// outdatedBody is the relay's refusal body. The status alone means nothing:
// the WebSocket library on the relay answers 426 to any request that is not an
// upgrade, so only this body identifies a refusal for age.
type outdatedBody struct {
	Error       string `json:"error"`
	ProtocolMin uint32 `json:"protocol_min"`
	ProtocolMax uint32 `json:"protocol_max"`
}

// refusalForAge reads a refused upgrade's response and reports whether it is
// the relay refusing this build for its age. The caller still owns the body.
func refusalForAge(resp *http.Response) (*OutdatedError, bool) {
	if resp == nil || resp.StatusCode != http.StatusUpgradeRequired || resp.Body == nil {
		return nil, false
	}
	var body outdatedBody
	// A refusal body is a few dozen bytes; the WebSocket library keeps only
	// the first 1024 of a refused upgrade's body anyway. A body that is not
	// JSON is some other 426, which is not a refusal for age: the decode
	// error is the answer, not a failure.
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&body)
	if decodeErr != nil || body.Error != "client_outdated" {
		return nil, false //nolint:nilerr // see above: not JSON means not this refusal
	}
	return &OutdatedError{ProtocolMin: body.ProtocolMin, ProtocolMax: body.ProtocolMax}, true
}

// RelayInfo is what a relay supports, as its HelloResponse stated it.
type RelayInfo struct {
	// ProtocolMin and ProtocolMax are the levels the relay accepts.
	ProtocolMin, ProtocolMax uint32

	// Capabilities are the features the relay offers beyond its level.
	Capabilities []tppv1.Capability
}

// Has reports whether the relay offers a capability.
func (r RelayInfo) Has(capability tppv1.Capability) bool {
	return slices.Contains(r.Capabilities, capability)
}

// legacyRelay is a relay that predates HelloRequest: level 1, nothing extra.
var legacyRelay = RelayInfo{ProtocolMin: 1, ProtocolMax: 1}

// hello asks a relay what it supports. A relay that predates the question
// answers that it does not know it, which is a definite answer: level 1, no
// capabilities (SPEC §5.3).
func hello(ctx context.Context, conn *conn) (RelayInfo, error) {
	callCtx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	var resp tppv1.HelloResponse
	err := conn.call(callCtx, tppv1.MessageType_MESSAGE_TYPE_HELLO_REQUEST, &tppv1.HelloRequest{}, &resp)
	switch {
	case errors.Is(err, errUnsupported):
		return legacyRelay, nil
	case err != nil:
		return RelayInfo{}, err
	}
	return RelayInfo{
		ProtocolMin:  resp.GetProtocolMin(),
		ProtocolMax:  resp.GetProtocolMax(),
		Capabilities: resp.GetCapabilities(),
	}, nil
}

// Relay returns what the relay supports, asking once per connection: the
// relay a new connection reaches may have been upgraded since. A shell checks
// a capability with Relay(ctx).Has before offering the feature, and says the
// relay needs updating (ErrRelayOutdated) when it is missing.
func (c *Client) Relay(ctx context.Context) (RelayInfo, error) {
	conn, err := c.connection(ctx)
	if err != nil {
		return RelayInfo{}, err
	}
	c.mu.Lock()
	if c.relayConn == conn {
		r := c.relay
		c.mu.Unlock()
		return r, nil
	}
	c.mu.Unlock()

	r, err := hello(ctx, conn)
	if err != nil {
		return RelayInfo{}, err
	}
	c.mu.Lock()
	c.relay, c.relayConn = r, conn
	c.mu.Unlock()
	return r, nil
}

// refreshRelay asks a new connection's relay what it supports, so the answer
// is at hand before anything needs it. A failure is only logged: Relay asks
// again on demand.
func (c *Client) refreshRelay(runCtx context.Context, conn *conn) {
	r, err := hello(runCtx, conn)
	if err != nil {
		c.logger.Debug("asking the relay what it supports failed", slog.Any("error", err))
		return
	}
	c.mu.Lock()
	c.relay, c.relayConn = r, conn
	c.mu.Unlock()
	if r.ProtocolMax < ProtocolLevel {
		// Not a failure: this build talks to it at its level and does without
		// what the newer levels added (SPEC §5.3).
		c.logger.Info("the relay speaks an older protocol level than this app",
			slog.Uint64("relay_max", uint64(r.ProtocolMax)), slog.Uint64("app", uint64(ProtocolLevel)))
	}
}

// markOutdated records that the relay refused this build for its age, and
// tells the shell the first time in a run of refusals.
func (c *Client) markOutdated(err error) {
	c.mu.Lock()
	first := c.outdatedErr == nil
	c.outdatedErr = err
	if first {
		close(c.refused)
	}
	c.mu.Unlock()
	if !first {
		return
	}
	var outdated *OutdatedError
	if errors.As(err, &outdated) {
		c.logger.Warn("the relay needs a newer version of this app; retrying hourly",
			slog.Uint64("relay_min", uint64(outdated.ProtocolMin)), slog.Uint64("app", uint64(ProtocolLevel)))
	}
	if c.handlers.OnClientOutdated != nil {
		c.handlers.OnClientOutdated()
	}
}

// clearOutdatedLocked forgets a refusal for age. c.mu must be held.
func (c *Client) clearOutdatedLocked() {
	if c.outdatedErr == nil {
		return
	}
	c.outdatedErr = nil
	c.refused = make(chan struct{})
}

// Outdated reports whether the relay is currently refusing this build for its
// age, so a shell drawing its window can show the same banner OnClientOutdated
// asked for.
func (c *Client) Outdated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.outdatedErr != nil
}
