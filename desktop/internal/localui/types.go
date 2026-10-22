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

	// GroupDeleted is set with Revoked when the server admin deleted the
	// group, so the screen can say that rather than "revoked".
	GroupDeleted bool `json:"group_deleted"`

	// GroupName is the group's display name as the server admin set it, or
	// empty.
	GroupName string `json:"group_name,omitempty"`

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

	// Skipped is the latest automatic transfer held back by the user's size
	// limit, or nil. It stays until the user dismisses it or a newer entry
	// lands on either side — an upload from here, or an entry put on this
	// clipboard — so a UI opened after the fact still shows it.
	Skipped *SkippedView `json:"skipped,omitempty"`

	// SensitiveNote is the latest clipboard change auto-watch did not upload
	// because it was sensitive, or nil. It is deliberately not a Skipped: a
	// password copy is routine, and the tray does not flag it. It clears when
	// the clipboard changes again or something is synced.
	SensitiveNote *SensitiveNote `json:"sensitive_note,omitempty"`
}

// SensitiveNote says why a clipboard change stayed on this device. It carries
// the reason the user is shown — the OS marker, a detector's title or a
// filter's label — never the content.
type SensitiveNote struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// SkippedView is an automatic upload or download that was not made: over the
// user's limit for it, or an automatic upload that failed. It carries a size
// and an entry id, never content.
type SkippedView struct {
	// Direction is "upload" or "download".
	Direction string `json:"direction"`
	// EntryID is the skipped entry, for a download; empty for an upload,
	// which never reached the relay.
	EntryID string    `json:"entry_id,omitempty"`
	Bytes   int64     `json:"bytes"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
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

	// Sensitive is set when the content is a password manager's copy or
	// matches a detector or filter. Preview is then empty: the text is
	// masked, Chars says how long it is, and RevealClipboard shows it on
	// request.
	Sensitive       bool   `json:"sensitive,omitempty"`
	SensitiveReason string `json:"sensitive_reason,omitempty"`
	Chars           int    `json:"chars,omitempty"`
}

// ClipboardReveal is the preview of a masked clipboard, fetched only when the
// user asks to see it.
type ClipboardReveal struct {
	Preview string `json:"preview"`
}

// EntryView is one entry as the history browser shows it. Only the metadata
// the relay itself holds appears here until the user asks for the entry.
type EntryView struct {
	ID        string    `json:"id"`
	Epoch     uint64    `json:"epoch"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`

	// What the entry's sealed header says it holds (/spec/crypto.md §5.5),
	// read here without fetching the entry. Kind is "text", "image" or
	// "file", and empty when the type is unknown: the writer sent no header,
	// or this device cannot open it.
	Kind        string `json:"kind,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Filename    string `json:"filename,omitempty"`
	// Bytes is the plaintext size; absent when the type is unknown.
	Bytes *int64 `json:"bytes,omitempty"`

	// Previewable says the history offers a preview: a format the preview
	// table knows, no larger than the auto-download limit. NoPreview says why
	// not, when not.
	Previewable bool   `json:"previewable"`
	NoPreview   string `json:"no_preview,omitempty"`

	// Render is how a previewable entry is drawn — "text", "image", "audio"
	// or "video" — so the row can say so before it is opened. Kind stays the
	// clipboard's word for it: a song is a file when copied, and audio when
	// played.
	Render string `json:"render,omitempty"`

	// Saved says the store already holds this entry's content, by the hash
	// in its header. False when unknown: the header has no hash, or the
	// store has not been listed yet.
	Saved bool `json:"saved,omitempty"`
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
	// vocabulary: what the entry becomes when copied.
	Kind string `json:"kind"`

	// Render is how the entry is drawn: "text", "image", "audio" or "video".
	// It comes from the preview table, which is also where Format, the
	// format's name for the screen, comes from. Audio and video carry no body
	// here; the page fetches it from the content endpoint to play it.
	Render string `json:"render"`
	Format string `json:"format"`

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

// EntryContent is the decrypted body of an audio or video entry, as the page
// plays it. ContentType comes from the preview table, never from the sender.
type EntryContent struct {
	ContentType string
	Body        []byte
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
// member to accept it (docs/internal/plans/joiner-emitted-pairing.md). The code is both
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
	Port       int  `json:"port"`
	ListenPort int  `json:"listen_port"`
	AutoWatch  bool `json:"auto_watch"`
	AutoApply  bool `json:"auto_apply"`

	// The automatic transfer limits in bytes, as they apply now; zero is no
	// limit. Manual transfers are never limited by them.
	AutoUploadMaxBytes   int64 `json:"auto_upload_max_bytes"`
	AutoDownloadMaxBytes int64 `json:"auto_download_max_bytes"`

	Autostart          bool   `json:"autostart"`
	AutostartSupported bool   `json:"autostart_supported"`
	ClipboardSupported bool   `json:"clipboard_supported"`
	DeviceName         string `json:"device_name"`

	// The updater's settings (UpdateView carries its state).
	UpdateChannel string `json:"update_channel"`
	AutoUpdate    bool   `json:"auto_update"`
	NightlyCommit string `json:"nightly_commit"`

	// RestartRequired is true when a saved port differs from the one this
	// process is listening on: the service binds once, at startup.
	RestartRequired bool `json:"restart_required"`

	// DetectSensitive honours password managers' privacy markers.
	DetectSensitive bool `json:"detect_sensitive"`

	// Detectors is every built-in detector, each with whether it is on.
	Detectors []DetectorView `json:"detectors"`

	// Filters are the user's own rules. An exact filter's value is never
	// returned.
	Filters []FilterView `json:"filters"`
}

// DetectorView is a built-in detector as the filter editor lists it: fixed,
// and only switched on or off.
type DetectorView struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

// FilterView is one of the user's filters, as the editor shows and sends it.
// A filter sent without an ID is given one when saved.
//
// Sent back, it replaces the saved filter with the same ID. An exact filter
// is sent with a Value only when the user typed a new secret; without one, it
// keeps the hash already saved under its ID.
type FilterView struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Target          string `json:"target"`
	Value           string `json:"value"`
	CaseInsensitive bool   `json:"case_insensitive"`
	Enabled         bool   `json:"enabled"`
	Label           string `json:"label"`
}

// FilterTest asks which detectors and filters match a sample. Detectors and
// Filters are the editor's unsaved draft; nil means what is saved. Nothing
// in it is stored or logged.
type FilterTest struct {
	Text      string        `json:"text"`
	Filename  string        `json:"filename"`
	Detectors *[]string     `json:"detectors,omitempty"`
	Filters   *[]FilterView `json:"filters,omitempty"`
}

// FilterMatch is one rule that matched a test sample. Source is "detector"
// or "filter"; Rule is the detector's name or the filter's ID. The editor
// gives a new filter its ID before it is saved, so a draft's matches can be
// pointed at too.
type FilterMatch struct {
	Source string `json:"source"`
	Rule   string `json:"rule"`
	Reason string `json:"reason"`
	// Name is the detector's title or the filter's label; empty for a filter
	// without a label.
	Name string `json:"name,omitempty"`
}

// FilterTestResult lists every match, in the order a clipboard change is
// checked.
type FilterTestResult struct {
	Matches []FilterMatch `json:"matches"`
}

// SyncRequest is a sync the user asked for. PlanID confirms an upload the
// service refused as sensitive: it names the SensitiveError the user said
// "push anyway" to.
type SyncRequest struct {
	Direction Direction `json:"direction"`
	PlanID    string    `json:"plan_id,omitempty"`
}

// SensitiveError is a manual upload held back because the clipboard is
// sensitive. The user confirms it by syncing again with PlanID, which is
// valid for a short while and only while the clipboard holds the same
// content.
type SensitiveError struct {
	PlanID string
	Reason string
}

func (e *SensitiveError) Error() string {
	return "the clipboard is a protected entry: " + e.Reason
}

// SettingsPatch is a partial settings update. A nil field is left alone, which
// is what lets the UI toggle one switch without echoing the rest.
type SettingsPatch struct {
	Port      *int  `json:"port"`
	AutoWatch *bool `json:"auto_watch"`
	AutoApply *bool `json:"auto_apply"`
	Autostart *bool `json:"autostart"`

	// AutoUploadMaxBytes and AutoDownloadMaxBytes set the automatic transfer
	// limits; zero is no limit.
	AutoUploadMaxBytes   *int64  `json:"auto_upload_max_bytes"`
	AutoDownloadMaxBytes *int64  `json:"auto_download_max_bytes"`
	DeviceName           *string `json:"device_name"`

	UpdateChannel *string `json:"update_channel"`
	AutoUpdate    *bool   `json:"auto_update"`
	// NightlyCommit is the commit whose build the Nightly channel installs:
	// a full or abbreviated hash, resolved to a full one before it is saved.
	NightlyCommit *string `json:"nightly_commit"`

	DetectSensitive *bool `json:"detect_sensitive"`

	// Detectors replaces the list of built-in detectors that are on.
	Detectors *[]string `json:"detectors"`

	// Filters replaces the whole filter list (FilterView says how an exact
	// filter keeps its secret).
	Filters *[]FilterView `json:"filters"`
}

// Event is a push from the service to the UI.
type Event struct {
	Kind     string    `json:"kind"`
	At       time.Time `json:"at"`
	Message  string    `json:"message,omitempty"`
	Epoch    uint64    `json:"epoch,omitempty"`
	DeviceID string    `json:"device_id,omitempty"`
	Name     string    `json:"name,omitempty"`

	// EntryID names the entry an evicted or progress event is about. An id
	// is metadata the relay already holds, not content.
	EntryID string `json:"entry_id,omitempty"`

	// Direction, Done and Total describe a chunked transfer: "upload" or
	// "download", and ciphertext bytes so far and in all.
	Direction string `json:"direction,omitempty"`
	Done      int64  `json:"done,omitempty"`
	Total     int64  `json:"total,omitempty"`
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

	// EventEvicted says the relay removed an entry before its expiry, to stay
	// within its storage limit. A history view drops it.
	EventEvicted = "evicted"

	// EventSkipped says an automatic upload or download was not made because
	// the entry is over the user's limit for it; Direction and Total say
	// which and how big. The user can still send or fetch it by hand.
	EventSkipped = "skipped"

	// EventSensitive says auto-watch kept a sensitive clipboard change on
	// this device (Status.SensitiveNote). It carries the reason, never the
	// content.
	EventSensitive = "sensitive"

	// EventProgress reports a chunked transfer of an entry larger than one
	// frame, once per chunk.
	EventProgress = "progress"

	// EventGroupDeleted says the server admin deleted this device's group.
	// Like EventRevoked, the device can do nothing more in it.
	EventGroupDeleted = "group_deleted"

	// EventGroupName says the group's display name was learned or changed;
	// Name carries it.
	EventGroupName = "group_name"

	// EventStore says the store changed: an item was saved, edited or
	// deleted, here or on another device, or the admin turned the store on
	// or off. A Store tab lists it again; a history view refreshes its
	// "saved" marks.
	EventStore = "store"
)

// DownloadProgress is how far an update download has got, in bytes. Total is
// 0 when the size is not known yet.
type DownloadProgress struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

// UpdateView is the updates section's model
// (docs/internal/plans/versioning-releases-and-updates.md §3).
type UpdateView struct {
	// Current is the running build.
	Current BuildView `json:"current"`

	Channel       string `json:"channel"`
	AutoUpdate    bool   `json:"auto_update"`
	NightlyCommit string `json:"nightly_commit,omitempty"`

	// State is one of the UpdateState* constants.
	State string `json:"state"`

	// Progress is the download in progress while State is downloading, once
	// its first bytes have arrived.
	Progress *DownloadProgress `json:"progress,omitempty"`

	// Message says what the state means for a person: why a check failed,
	// why nothing can be installed.
	Message string `json:"message,omitempty"`

	// FailedRun is the GitHub Actions run whose failure State error reports,
	// when the error is a Nightly commit's build that failed.
	FailedRun string `json:"failed_run,omitempty"`

	// Available is the build the channel offers, when it differs from the
	// running one.
	Available *UpdateCandidate `json:"available,omitempty"`

	LastChecked *time.Time `json:"last_checked,omitempty"`

	// CanInstall is false where this installation cannot replace itself: a
	// build from a checkout, a binary outside an app bundle, a folder it may
	// not write to. CannotInstall says which.
	CanInstall    bool   `json:"can_install"`
	CannotInstall string `json:"cannot_install,omitempty"`

	// Previous is the build the last update replaced, which Roll back restores.
	Previous *BuildView `json:"previous,omitempty"`

	// Token is the state of the Nightly channel's GitHub token. The token
	// itself is never sent to the UI.
	Token TokenView `json:"token"`
}

// BuildView names one build.
type BuildView struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
	Commit  string `json:"commit,omitempty"`
	Stamp   int64  `json:"stamp"`
}

// UpdateCandidate is a build the channel offers.
type UpdateCandidate struct {
	BuildView

	// Older is set when the build is older than the running one, which only
	// happens after a channel switch; installing it needs AllowOlder.
	Older bool `json:"older"`

	// Signed is false for a fork's Nightly build, which CI could not sign.
	// Such a build needs Confirm, after the person has seen Origin.
	Signed            bool `json:"signed"`
	NeedsConfirmation bool `json:"needs_confirmation"`

	// Building is set while the commit's build is still running.
	Building bool `json:"building,omitempty"`

	// Origin says where a Nightly build came from.
	Origin *BuildOrigin `json:"origin,omitempty"`
}

// BuildOrigin is what the Nightly confirmation screen shows about a commit.
type BuildOrigin struct {
	Repository       string `json:"repository"`
	Branch           string `json:"branch,omitempty"`
	Fork             bool   `json:"fork"`
	PullRequest      int    `json:"pull_request,omitempty"`
	PullRequestTitle string `json:"pull_request_title,omitempty"`
	CommitMessage    string `json:"commit_message,omitempty"`
	Author           string `json:"author,omitempty"`
	URL              string `json:"url"`
	RunURL           string `json:"run_url,omitempty"`

	// ChangesWorkflows is set when the commit's pull request touches
	// .github/workflows: a pull_request run uses the PR's own workflow files,
	// so such a PR can change how its artifact was built.
	ChangesWorkflows bool `json:"changes_workflows"`
}

// TokenView describes the stored GitHub token without revealing it.
type TokenView struct {
	Set bool `json:"set"`

	// Source is "keystore", or "environment" when TPP_DESKTOP_GITHUB_TOKEN
	// overrides it.
	Source string `json:"source,omitempty"`

	// Invalid is set after GitHub rejected the token.
	Invalid bool `json:"invalid,omitempty"`

	// ExpiresAt is when the token stops working, and NoExpiry says it never
	// does. Both are unset until GitHub has answered a call made with it,
	// which is the only place the expiry can be read from.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	NoExpiry  bool       `json:"no_expiry,omitempty"`
}

// InstallRequest is what the Install button sends.
type InstallRequest struct {
	// Confirm accepts an unsigned Nightly build after its origin was shown.
	Confirm bool `json:"confirm"`
	// AllowOlder accepts a build older than the running one.
	AllowOlder bool `json:"allow_older"`
}

// TokenRequest stores a GitHub token.
type TokenRequest struct {
	Token string `json:"token"`
}

// Update states.
const (
	UpdateStateIdle        = "idle"
	UpdateStateChecking    = "checking"
	UpdateStateUpToDate    = "up_to_date"
	UpdateStateAvailable   = "available"
	UpdateStateBuilding    = "building"
	UpdateStateDownloading = "downloading"
	UpdateStateInstalling  = "installing"
	UpdateStateRestarting  = "restarting"
	UpdateStateError       = "error"
)

// EventUpdate tells the UI the update state changed.
const EventUpdate = "update"
