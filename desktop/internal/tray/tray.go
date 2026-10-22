// Package tray is the desktop service's only native UI: an icon in the menu
// bar or the notification area, with the three items SPEC §7.2 asks for — sync
// now, open the UI, quit — and a few more that only appear when they must.
//
// The icon says what the service is doing: connecting, connected, uploading,
// downloading, or cut off from the relay — and, once idle, whether an
// automatic transfer was held back and waits for the user. Its glyph is
// redrawn for each state, so the state reads at sixteen pixels and in one
// colour.
//
// One of the extra items is the bind failure. The port is fixed by SPEC §7.2
// and a fixed port can be taken; a service that failed to bind and exited
// quietly would look to the user like a service that is running. When the
// localhost server cannot start, the tray says so and offers the settings
// file where the port is overridden.
//
// The others are the sync direction. A sync from the tray that cannot tell
// which side is newer asks, as the UI does (SPEC §6): with a notification
// carrying an Upload and a Download button, and with the same two items in
// the menu for when notifications are turned off.
//
// And the protected entry: an upload from the tray of a clipboard the service
// marked sensitive asks "push anyway?" the same way, with one button and one
// menu item (docs/internal/plans/auto-upload-filters.md).
package tray

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Options configures the tray.
type Options struct {
	// URL is the address the "Open TwoPlacePaste" item opens. It carries the
	// per-launch token, so it is never logged and never written to a file.
	URL string

	// SyncNow runs a manual sync. It must not block: it is called on the
	// tray's own goroutine.
	SyncNow func()

	// SyncDirection runs a sync the user gave a direction to, from the
	// notification AskDirection posts or from the menu items it reveals. It is
	// run on its own goroutine.
	SyncDirection func(Direction)

	// PushSensitive uploads a protected entry the user confirmed, with the
	// plan id AskSensitive was given. It is run on its own goroutine.
	PushSensitive func(planID string)

	// CheckUpdates checks for an update. It is run on its own goroutine.
	// Nil hides the item.
	CheckUpdates func()

	// Quit stops the service. The tray calls it before returning.
	Quit func()

	// SettingsPath is the settings file the error item opens.
	SettingsPath string

	// Err, when set, is a startup failure to display instead of the ordinary
	// menu — a taken port, most often. The tray still runs: an error the user
	// cannot see is an error they cannot fix.
	Err error
}

// Status is what the icon shows.
type Status int

// The statuses, from the one every launch starts in.
const (
	// StatusConnecting is a device in a group that has not reached the relay
	// yet since the service started.
	StatusConnecting Status = iota

	// StatusConnected is the resting state: connected and idle.
	StatusConnected

	// StatusUploading is this device sending an entry to the group.
	StatusUploading

	// StatusDownloading is this device fetching an entry from the group.
	StatusDownloading

	// StatusSyncing is an upload and a download both under way.
	StatusSyncing

	// StatusDisconnected is a connection that was up and has dropped, or a
	// device the group has revoked. The client keeps redialling meanwhile.
	StatusDisconnected

	// StatusUnpaired is a device in no group: nothing can sync until the
	// user creates one or pairs, which is done in the UI.
	StatusUnpaired

	// StatusFailed is the service failing to start (Options.Err).
	StatusFailed

	// StatusSkippedUpload is a connected, idle device whose last copy was
	// not uploaded automatically — over the user's size limit, or the upload
	// failed — and still waits to be sent by hand. It lasts until a sync in
	// either direction, or until the user dismisses the notice in the UI.
	StatusSkippedUpload

	// StatusSkippedDownload is the same for an entry another device wrote
	// that was not put on this clipboard automatically.
	StatusSkippedDownload
)

// transferring reports whether a status is an entry on the move, which is
// what the icon holds up for minSyncShown.
func (s Status) transferring() bool {
	return s == StatusUploading || s == StatusDownloading || s == StatusSyncing
}

// label is the status in the words the tooltip and the menu use.
func (s Status) label() string {
	switch s {
	case StatusConnecting:
		return "Connecting…"
	case StatusConnected:
		return "Connected"
	case StatusUploading:
		return "Uploading…"
	case StatusDownloading:
		return "Downloading…"
	case StatusSyncing:
		return "Uploading and downloading…"
	case StatusDisconnected:
		return "Disconnected — retrying"
	case StatusUnpaired:
		return "Not paired — open the app to set up"
	case StatusFailed:
		return "Not running"
	case StatusSkippedUpload:
		return "Your last copy was not uploaded automatically — open the app"
	case StatusSkippedDownload:
		return "A new entry was not downloaded automatically — open the app"
	}
	return ""
}

// Direction is the way a sync the user chose goes.
type Direction int

// The two directions of SPEC §6.
const (
	// Upload sends this machine's clipboard to the group.
	Upload Direction = iota + 1

	// Download puts the group's latest entry on this machine's clipboard.
	Download
)

// The identifiers the notification's buttons carry back. They are the same
// on every platform, so one parser serves them all.
const (
	actionUpload     = "upload"
	actionDownload   = "download"
	actionPushAnyway = "push-anyway"
)

// directionForAction maps a notification button back to its direction. Any
// other activation — a click on the body, a dismissal — is not a choice.
func directionForAction(action string) (Direction, bool) {
	switch action {
	case actionUpload:
		return Upload, true
	case actionDownload:
		return Download, true
	}
	return 0, false
}

// minSyncShown is the least time a transfer glyph stays up. Most transfers
// are over in a few milliseconds, and an icon that changes for one frame is a
// flicker, not a status.
const minSyncShown = 700 * time.Millisecond

// choiceTTL is how long the menu keeps offering a direction nobody picked.
// By then the user has synced some other way or stopped caring, and the two
// items would only be clutter.
const choiceTTL = 5 * time.Minute

// The words of the protected entry question, shared by every platform's
// notification and the menu.
const (
	sensitiveTitle  = "This is a protected entry"
	sensitivePrompt = "Push it to your other devices anyway?"
	pushAnywayLabel = "Push anyway"
	pushMenuLabel   = "Push protected entry anyway"
)

// sensitiveTTL is how long "push anyway?" stays answerable from the tray: the
// service's plan lives this long, and an answer after it would only be asked
// again.
const sensitiveTTL = 2 * time.Minute

// Tray is the tray icon. Its methods are safe to call from any goroutine,
// before Run as well as during it: what they change waits for the icon to
// exist, and shows when it does.
type Tray struct {
	logger  *slog.Logger
	now     func() time.Time
	minSync time.Duration

	mu      sync.Mutex
	want    Status // the status last asked for
	shown   Status // the status on screen, which lags want while syncing is held
	shownAt time.Time
	hold    *time.Timer
	paint   func(Status) // the platform's icon setter; nil until the icon exists

	// group is the group's display name, which the tooltip leads with. It
	// is read by paint, which always runs with mu held.
	group string

	onDirection  func(Direction)
	revealChoice func(bool) // shows or hides the menu's direction items
	choiceTimer  *time.Timer

	// The pending "push anyway?" question: the plan id that answers it, the
	// callback that does, and the menu item that offers it.
	onPush         func(planID string)
	pushPlan       string
	revealPush     func(bool)
	sensitiveTimer *time.Timer
}

// New returns a tray that has not been shown yet.
func New(logger *slog.Logger) *Tray {
	if logger == nil {
		logger = slog.Default()
	}
	return &Tray{logger: logger, now: time.Now, minSync: minSyncShown}
}

// Run shows the tray and blocks until the user quits or ctx is done.
//
// It must be called from the main goroutine: the macOS menu bar is only
// addressable from the process's first thread, and the systray library
// enforces it.
func (t *Tray) Run(ctx context.Context, opts Options) {
	t.mu.Lock()
	t.onDirection = opts.SyncDirection
	t.onPush = opts.PushSensitive
	if opts.Err != nil {
		t.want = StatusFailed
	}
	t.mu.Unlock()
	run(ctx, t, opts)
}

// SetStatus changes what the icon shows.
func (t *Tray) SetStatus(s Status) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.want = s
	t.settleLocked()
}

// settleLocked moves the icon towards want, holding a transfer's glyph up for
// minSyncShown first. One transfer glyph replaces another at once: an
// upload that a download joins is news, not flicker.
func (t *Tray) settleLocked() {
	if t.shown.transferring() && !t.want.transferring() {
		if rest := t.minSync - t.now().Sub(t.shownAt); rest > 0 {
			if t.hold == nil {
				t.hold = time.AfterFunc(rest, func() {
					t.mu.Lock()
					defer t.mu.Unlock()
					t.hold = nil
					t.settleLocked()
				})
			}
			return
		}
	}
	if t.hold != nil {
		t.hold.Stop()
		t.hold = nil
	}
	if t.shown == t.want && t.paint != nil {
		return
	}
	t.showLocked(t.want)
}

func (t *Tray) showLocked(s Status) {
	if s != t.shown {
		t.shownAt = t.now()
	}
	t.shown = s
	if t.paint != nil {
		t.paint(s)
	}
}

// SetGroupName puts the group's display name, as the server admin set it, in
// the tooltip. An empty name leaves it out.
func (t *Tray) SetGroupName(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.group == name {
		return
	}
	t.group = name
	if t.paint != nil {
		t.paint(t.shown)
	}
}

// title is the tooltip and, where a platform shows one, the menu title.
const title = "TwoPlacePaste"

// tooltipLocked is the tooltip for one status. The caller holds mu.
func (t *Tray) tooltipLocked(s Status) string {
	if t.group == "" {
		return title + " — " + s.label()
	}
	return title + " — " + t.group + " — " + s.label()
}

// attach is the platform saying the icon now exists: from here on, a status
// change repaints it, and the status asked for meanwhile is painted now.
func (t *Tray) attach(paint func(Status)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.paint = paint
	t.showLocked(t.want)
}

// AskDirection asks the user which way a sync goes: with a notification that
// carries both choices, and with both in the menu. The answer reaches
// Options.SyncDirection.
func (t *Tray) AskDirection() {
	t.mu.Lock()
	reveal := t.revealChoice
	if t.choiceTimer != nil {
		t.choiceTimer.Stop()
	}
	t.choiceTimer = time.AfterFunc(choiceTTL, t.withdrawChoice)
	t.mu.Unlock()
	if reveal != nil {
		reveal(true)
	}
	if err := postPrompt(); err != nil {
		// The menu items are the fallback, so this is not the end of it.
		t.logger.Warn("could not show the sync direction notification; the tray menu offers the choice",
			"error", err)
	}
}

// Notify shows a notification with no buttons. It is for the outcome of
// something the user did from the tray, which has no other way to answer.
func (t *Tray) Notify(title, body string) {
	if err := postText(title, body); err != nil {
		t.logger.Warn("could not show a notification", "error", err)
	}
}

// choose is the user answering AskDirection, from either place it asked.
func (t *Tray) choose(d Direction) {
	t.withdrawChoice()
	t.mu.Lock()
	fn := t.onDirection
	t.mu.Unlock()
	if fn != nil {
		go fn(d)
	}
}

// chooseAction is a notification button pressed.
func (t *Tray) chooseAction(action string) {
	if action == actionPushAnyway {
		t.pushAnyway()
		return
	}
	if d, ok := directionForAction(action); ok {
		t.choose(d)
	}
}

// AskSensitive asks whether to upload a protected entry anyway: with a
// notification carrying a Push anyway button, and with a menu item for when
// notifications are off. reason is what made it protected, in the user's
// words; planID is what the answer hands to Options.PushSensitive. Neither
// is logged: a filter's label is the user's own text.
func (t *Tray) AskSensitive(planID, reason string) {
	t.mu.Lock()
	t.pushPlan = planID
	reveal := t.revealPush
	if t.sensitiveTimer != nil {
		t.sensitiveTimer.Stop()
	}
	t.sensitiveTimer = time.AfterFunc(sensitiveTTL, t.withdrawSensitive)
	t.mu.Unlock()
	if reveal != nil {
		reveal(true)
	}
	if err := postSensitive(sensitiveBody(reason)); err != nil {
		t.logger.Warn("could not show the protected entry notification; the tray menu offers the choice",
			"error", err)
	}
}

// sensitiveBody is the notification's text for a reason.
func sensitiveBody(reason string) string {
	if reason == "" {
		return sensitivePrompt
	}
	return strings.TrimSuffix(reason, ".") + ". " + sensitivePrompt
}

// pushAnyway is the user answering AskSensitive, from either place it asked.
// The plan is used once: a second press has nothing to confirm.
func (t *Tray) pushAnyway() {
	t.mu.Lock()
	plan, fn := t.pushPlan, t.onPush
	t.mu.Unlock()
	t.withdrawSensitive()
	if plan != "" && fn != nil {
		go fn(plan)
	}
}

// withdrawSensitive takes the question back.
func (t *Tray) withdrawSensitive() {
	t.mu.Lock()
	t.pushPlan = ""
	reveal := t.revealPush
	if t.sensitiveTimer != nil {
		t.sensitiveTimer.Stop()
		t.sensitiveTimer = nil
	}
	t.mu.Unlock()
	if reveal != nil {
		reveal(false)
	}
	withdrawSensitivePrompt()
}

// setPushItem is the platform handing over its menu's Push anyway item.
func (t *Tray) setPushItem(reveal func(bool)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.revealPush = reveal
}

// withdrawChoice takes the question back: the menu items go, and so does a
// notification still on screen.
func (t *Tray) withdrawChoice() {
	t.mu.Lock()
	reveal := t.revealChoice
	if t.choiceTimer != nil {
		t.choiceTimer.Stop()
		t.choiceTimer = nil
	}
	t.mu.Unlock()
	if reveal != nil {
		reveal(false)
	}
	withdrawPrompt()
}

// setChoiceItems is the platform handing over its menu's direction items.
func (t *Tray) setChoiceItems(reveal func(bool)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.revealChoice = reveal
}
