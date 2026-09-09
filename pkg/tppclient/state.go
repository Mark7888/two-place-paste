package tppclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/Mark7888/two-place-paste/pkg/tppclient/keystore"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/tppcrypto"
)

// DefaultStateName is the keystore entry this package stores its state under.
const DefaultStateName = "session"

// State is everything an installation must survive a restart with: who this
// device is, which group it belongs to, and the two secrets it holds.
//
// It is stored as one blob because all of it is secret-bearing — the device
// private key and the group key obviously, and the identifiers because they
// are bearer credentials on this wire (see the transport's package docs). A
// caller persists it through a keystore.Store and nowhere else; it must never
// be written to a log or an ordinary config file (/spec/crypto.md §10).
type State struct {
	// ServerURL is the relay's base URL, e.g. "https://tpp.example.com".
	ServerURL string `json:"server_url"`

	GroupID    string `json:"group_id"`
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`

	// Epoch is the group key generation this client holds. A client holds
	// exactly one (epoch, group key) pair at a time (/spec/crypto.md §7).
	Epoch uint64 `json:"epoch"`

	// DevicePrivateKey is the X25519 private key generated on first launch.
	DevicePrivateKey []byte `json:"device_private_key"`

	// GroupKey is the current group key, unwrapped. Empty until this device
	// joins a group.
	GroupKey []byte `json:"group_key"`
}

// InGroup reports whether this state can talk to a group.
func (s State) InGroup() bool {
	return s.GroupID != "" && s.DeviceID != "" && len(s.GroupKey) == tppcrypto.GroupKeySize
}

// PublicKey returns this device's X25519 public key.
func (s State) PublicKey() ([]byte, error) {
	if len(s.DevicePrivateKey) != tppcrypto.KeySize {
		return nil, fmt.Errorf("tppclient: device private key is %d bytes, want %d",
			len(s.DevicePrivateKey), tppcrypto.KeySize)
	}
	pub, err := tppcrypto.PublicKey(s.DevicePrivateKey)
	if err != nil {
		return nil, fmt.Errorf("tppclient: derive this device's public key: %w", err)
	}
	return pub, nil
}

// String is deliberately terse: State goes nowhere near a log, and a Stringer
// that printed the keys would make that accident easy.
func (s State) String() string {
	return fmt.Sprintf("tppclient.State{group:%s device:%s epoch:%d}", s.GroupID, s.DeviceID, s.Epoch)
}

// LoadState reads state from a keystore. A store with nothing under name
// returns keystore.ErrNotFound, which a first launch treats as "no state yet".
func LoadState(store keystore.Store, name string) (State, error) {
	if name == "" {
		name = DefaultStateName
	}
	raw, err := store.Load(name)
	if err != nil {
		// Wrapped, not replaced: a first launch tells keystore.ErrNotFound
		// apart from a real failure with errors.Is.
		return State{}, fmt.Errorf("tppclient: read stored state: %w", err)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}, fmt.Errorf("tppclient: stored state could not be decoded: %w", err)
	}
	return s, nil
}

// SaveState writes state to a keystore.
func SaveState(store keystore.Store, name string, s State) error {
	if name == "" {
		name = DefaultStateName
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("tppclient: state could not be encoded: %w", err)
	}
	if err := store.Save(name, raw); err != nil {
		return fmt.Errorf("tppclient: state could not be stored: %w", err)
	}
	return nil
}

// normalizeServerURL validates a relay base URL and strips anything that is
// not scheme, host and port. A base URL with a path would silently produce
// wrong WebSocket and creation URLs.
func normalizeServerURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("tppclient: server URL %q is not a URL: %w", raw, err)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("tppclient: server URL %q must be http or https", raw)
	}
	if u.Host == "" {
		return "", errors.New("tppclient: server URL has no host")
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String(), nil
}

// splitCreationURL splits https://<host>/<token> into its base URL and token
// (SPEC §3.1). The whole URL is what an operator hands a user, as text or as
// the QR code on the admin screen.
func splitCreationURL(raw string) (base, token string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", fmt.Errorf("tppclient: creation URL %q is not a URL: %w", raw, err)
	}
	token = strings.Trim(u.Path, "/")
	if token == "" || strings.Contains(token, "/") {
		return "", "", fmt.Errorf("tppclient: creation URL %q does not end in a single-segment token", raw)
	}
	base, err = normalizeServerURL(u.Scheme + "://" + u.Host)
	if err != nil {
		return "", "", err
	}
	return base, token, nil
}

// websocketURL derives the transport endpoint from a base URL. deviceID is the
// connection's credential and is empty for the two unauthenticated flows,
// group creation and pairing-join.
func websocketURL(base, deviceID string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("tppclient: server URL %q is not a URL: %w", base, err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("tppclient: server URL %q must be http or https", base)
	}
	u.Path = websocketPath
	if deviceID != "" {
		u.RawQuery = url.Values{"device_id": {deviceID}}.Encode()
	}
	return u.String(), nil
}

// websocketPath is where the relay mounts the transport (server ws.DefaultPath).
const websocketPath = "/ws"
