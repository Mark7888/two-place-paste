// Package localui is the localhost HTTP server that serves the React UI to the
// user's browser and exposes the service's operations as JSON (SPEC §7.2).
//
// Everything here exists because of one fact: any page in the user's browser
// can issue requests to 127.0.0.1. The defences are therefore not decoration —
//
//   - the listener binds 127.0.0.1 explicitly, never 0.0.0.0, so nothing off
//     the machine can reach it and Windows raises no firewall prompt;
//   - a token generated per launch is required on every request, and the tray
//     is the only thing that knows it;
//   - the Origin header is validated on every request, the WebSocket upgrade
//     included, which is what stops a page the user is browsing from driving
//     this service with the user's own clipboard.
//
// A bind failure is surfaced, never swallowed: the port is fixed by SPEC §7.2
// and a fixed port can be taken, so the tray shows the error and the settings
// file overrides the port.
package localui

import "time"

// Status is what the sync screen renders.
type Status struct {
	InGroup    bool       `json:"in_group"`
	Connected  bool       `json:"connected"`
	Epoch      uint64     `json:"epoch"`
	DeviceID   string     `json:"device_id"`
	DeviceName string     `json:"device_name"`
	ServerURL  string     `json:"server_url"`
	Keystore   string     `json:"keystore"`
	Revoked    bool       `json:"revoked"`
	LastSync   *time.Time `json:"last_sync,omitempty"`
	LastError  string     `json:"last_error,omitempty"`

	// Clipboard describes what is on the local clipboard right now, for the
	// direction the user is about to choose. It is never sent anywhere else.
	Clipboard ClipboardView `json:"clipboard"`

	// Latest is the relay's newest entry, or nil when the group has none.
	Latest *EntryView `json:"latest,omitempty"`

	// DirectionKnown is false when the service cannot tell which side is
	// newer. SPEC §6 says a client that cannot compare timestamps offers two
	// explicit buttons instead of guessing, and this is that flag.
	DirectionKnown bool      `json:"direction_known"`
	Suggested      Direction `json:"suggested_direction"`
}

// ClipboardView is the local clipboard as the UI shows it. Preview is bounded
// and is only ever rendered locally.
type ClipboardView struct {
	Available bool       `json:"available"`
	Empty     bool       `json:"empty"`
	Kind      string     `json:"kind,omitempty"`
	Filename  string     `json:"filename,omitempty"`
	Bytes     int        `json:"bytes"`
	Preview   string     `json:"preview,omitempty"`
	ChangedAt *time.Time `json:"changed_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// EntryView is one entry as the history browser shows it. Only the metadata
// the relay itself holds appears here until the user asks for the entry.
type EntryView struct {
	ID        string    `json:"id"`
	Epoch     uint64    `json:"epoch"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Optional reports a timestamp that may not exist. A zero time.Time marshals
// as year 1 rather than as nothing, and a UI that renders "1 January 0001" for
// "never synced" is a UI nobody trusts.
func Optional(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// Direction is which way a sync goes.
type Direction string

// The three directions a UI can ask for. DirectionAuto compares the local
// clipboard's observed change time with the relay's newest entry (SPEC §6) and
// fails rather than guessing when it has no local timestamp.
const (
	DirectionAuto     Direction = "auto"
	DirectionUpload   Direction = "upload"
	DirectionDownload Direction = "download"
)

// SyncResult reports what a sync did.
type SyncResult struct {
	Direction Direction  `json:"direction"`
	Changed   bool       `json:"changed"`
	Message   string     `json:"message"`
	Entry     *EntryView `json:"entry,omitempty"`
}

// HistoryQuery bounds a history listing.
type HistoryQuery struct {
	Limit  uint32
	Before time.Time
}

// HistoryPage is one page of history, newest first.
type HistoryPage struct {
	Entries    []EntryView `json:"entries"`
	NextBefore *time.Time  `json:"next_before,omitempty"`
}

// EntryPreview is one history entry rendered for a look rather than a paste.
//
// It exists because picking an entry out of a list of sizes and timestamps is
// guesswork: the relay holds only metadata, so nothing in a listing says which
// row is the address you copied and which is the log line. Fetching a preview
// decrypts that one entry on this machine — the same operation copying it
// would do — and renders it here and nowhere else. Nothing about it is cached,
// logged, or sent anywhere, and an entry from before a rekey has no preview at
// all because this device no longer holds the key it was written under.
type EntryPreview struct {
	ID          string `json:"id"`
	Epoch       uint64 `json:"epoch"`
	ContentType string `json:"content_type"`
	Filename    string `json:"filename,omitempty"`
	Bytes       int    `json:"bytes"`

	// Kind is "text", "image" or "file", matching the clipboard's own
	// vocabulary. A file has no preview body: rendering its bytes helps
	// nobody, and its name and size are already in the listing.
	Kind string `json:"kind"`

	// Text is a bounded rendering of a text entry, and Truncated says whether
	// there was more of it.
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`

	// ImageDataURL is a small image inlined as a data URL, so the browser
	// renders it without a second request for a decrypted body. It is empty
	// for an image too large to be worth inlining, and ImageTooLarge says so.
	ImageDataURL  string `json:"image_data_url,omitempty"`
	ImageTooLarge bool   `json:"image_too_large,omitempty"`
}

// DeviceView is one group member, by name, as SPEC §3.3 step 2 requires.
type DeviceView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	This      bool       `json:"this"`
}

// RosterView is the group's devices at one epoch.
type RosterView struct {
	Devices []DeviceView `json:"devices"`
	Epoch   uint64       `json:"epoch"`
}

// RevokePlan is a prepared revocation waiting for the user's confirmation.
//
// It is handed out by PrepareRevoke and is the only thing ConfirmRevoke
// accepts: a UI that has not fetched and rendered Remaining has no plan id and
// therefore cannot confirm anything. That is SPEC §3.3 step 2 enforced by the
// API rather than by the front end remembering to ask.
type RevokePlan struct {
	ID        string       `json:"id"`
	Target    DeviceView   `json:"target"`
	Remaining []DeviceView `json:"remaining"`
	Epoch     uint64       `json:"epoch"`
	ExpiresAt time.Time    `json:"expires_at"`
}

// Invite is a pairing invitation to show (SPEC §3.2 step 1). Pairing is
// symmetric, and a phone must be able to read what a desktop shows.
type Invite struct {
	// Payload is the bare code. It stays in the response because anything that
	// can already read one must keep working.
	Payload string `json:"payload"`

	// Link is what the QR carries and what the copy button copies: the same
	// code wrapped in a link to this group's relay (SPEC §6a). A bare
	// base64url string is unreadable to a general-purpose scanner — Google
	// Lens shows it as text and offers nothing to open.
	Link string `json:"link"`

	ExpiresAt time.Time `json:"expires_at"`
}

// OfferView is a pairing offer this device is showing while it waits for a
// member to accept it (docs/plans/joiner-emitted-pairing.md). The code is both
// the QR contents and the copyable string: one string, whichever form the
// accepting device can read.
type OfferView struct {
	// Code is the bare code; Link wraps it for a scanner, as with Invite.
	Code      string    `json:"code"`
	Link      string    `json:"link"`
	ExpiresAt time.Time `json:"expires_at"`
}

// OfferPlan is a scanned or pasted offer waiting for the user's confirmation.
//
// It is handed out by PrepareAcceptOffer and is the only thing
// ConfirmAcceptOffer accepts, for the same reason RevokePlan works that way —
// and with more at stake. Accepting admits a device to the group and hands it
// the group key, so a UI that has not fetched and rendered DeviceName and
// Fingerprint has no plan id and cannot confirm anything.
type OfferPlan struct {
	ID string `json:"id"`

	// DeviceName is what the offering device calls itself. Display text from a
	// device that is not in the group yet; it proves nothing, which is why
	// Fingerprint is beside it.
	DeviceName string `json:"device_name"`

	// Fingerprint is the offered public key rendered for a person to compare
	// with what that device is showing.
	Fingerprint string `json:"fingerprint"`

	ExpiresAt time.Time `json:"expires_at"`
}

// SettingsView is the settings screen's model.
type SettingsView struct {
	Port               int    `json:"port"`
	ListenPort         int    `json:"listen_port"`
	AutoWatch          bool   `json:"auto_watch"`
	AutoApply          bool   `json:"auto_apply"`
	Autostart          bool   `json:"autostart"`
	AutostartSupported bool   `json:"autostart_supported"`
	ClipboardSupported bool   `json:"clipboard_supported"`
	DeviceName         string `json:"device_name"`

	// RestartRequired is true when a saved port differs from the one this
	// process is listening on: the service binds once, at startup.
	RestartRequired bool `json:"restart_required"`
}

// SettingsPatch is a partial settings update. A nil field is left alone, which
// is what lets the UI toggle one switch without echoing the rest.
type SettingsPatch struct {
	Port       *int    `json:"port"`
	AutoWatch  *bool   `json:"auto_watch"`
	AutoApply  *bool   `json:"auto_apply"`
	Autostart  *bool   `json:"autostart"`
	DeviceName *string `json:"device_name"`
}

// Event is a push from the service to the UI.
type Event struct {
	Kind     string    `json:"kind"`
	At       time.Time `json:"at"`
	Message  string    `json:"message,omitempty"`
	Epoch    uint64    `json:"epoch,omitempty"`
	DeviceID string    `json:"device_id,omitempty"`
	Name     string    `json:"name,omitempty"`
}

// Event kinds. A UI refreshes on any of them; none of them carries clipboard
// content, because an event stream is a place content would leak from.
const (
	EventConnected     = "connected"
	EventDisconnected  = "disconnected"
	EventEpoch         = "epoch"
	EventDevicePaired  = "device_paired"
	EventOffer         = "offer"
	EventDeviceRevoked = "device_revoked"
	EventRevoked       = "revoked"
	EventClipboard     = "clipboard"
	EventSync          = "sync"
	EventSettings      = "settings"
)
