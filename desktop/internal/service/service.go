package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Mark7888/two-place-paste/desktop/internal/autostart"
	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
	"github.com/Mark7888/two-place-paste/desktop/internal/config"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// planTTL bounds how long a prepared revocation stays confirmable. A dialog
// the user left open for an hour is a dialog whose roster is no longer the
// group's roster, and confirming it would rekey for a device list that has
// moved on (SPEC §3.3 step 2).
const planTTL = 10 * time.Minute

// previewRunes bounds the clipboard preview the UI renders. The preview never
// leaves this machine; it is bounded because a 50 MB paste in a status
// response helps nobody.
const previewRunes = 240

// maxPreviewImageBytes bounds an image the history screen inlines as a data
// URL. Base64 costs a third on top, and everything past this is a thumbnail
// nobody is squinting at anyway.
const maxPreviewImageBytes = 4 << 20

// requestTimeout bounds one relay round trip made on the UI's behalf.
const requestTimeout = 30 * time.Second

// Options configures a Service.
type Options struct {
	// Clipboard is the OS clipboard. Required.
	Clipboard clipboard.Clipboard

	// Autostart manages the login item. Required.
	Autostart autostart.Manager

	// Settings is the loaded settings file.
	Settings config.Settings

	// ConfigDir is where settings are saved. Empty means the per-user default.
	ConfigDir string

	// ListenPort is the port the localhost server actually bound, which is not
	// the configured one when the configured one was taken.
	ListenPort int

	// KeystoreBackend names the store the client's secrets are in, so the UI
	// can tell the user rather than silently taking the weakest one.
	KeystoreBackend string

	// WatchInterval overrides the clipboard poll period.
	WatchInterval time.Duration

	// WatchPaused, when set, holds clipboard polling off while it answers
	// true: a locked screen, where nobody is copying anything.
	WatchPaused func() bool

	// Updates hears every change to the settings, so the updater follows
	// the channel the user picks. Optional.
	Updates Preferences

	// OnActivity hears every change to the service's Activity: connecting,
	// disconnecting, a transfer starting or ending. It is called from the
	// client's goroutines and must not block. Optional.
	OnActivity func(Activity)

	// Logger receives service-level logs. It never sees clipboard content.
	Logger *slog.Logger

	// Now supplies UTC timestamps; tests replace it.
	Now func() time.Time
}

// Preferences is the part of the updater the service drives: it owns the
// settings file, and hands the updater its settings after each save.
type Preferences interface {
	SetPreferences(config.Settings)
}

// CommitResolver turns an abbreviated commit hash into the full one. An
// updater that can do this lets the user type a short hash for Nightly.
type CommitResolver interface {
	ResolveCommit(ctx context.Context, hash string) (string, error)
}

// Service is the desktop shell: it answers the UI, drives the clipboard, and
// owns the sync direction decision of SPEC §6.
type Service struct {
	clip      clipboard.Clipboard
	watcher   *clipboard.Watcher
	auto      autostart.Manager
	updates   Preferences
	hub       *hub
	incoming  chan tppclient.EntryMeta
	logger    *slog.Logger
	now       func() time.Time
	configDir string
	backend   string
	listen    int

	// catchUps tracks catchUp goroutines, so a test can wait for one.
	catchUps sync.WaitGroup

	onActivity   func(Activity)
	activityMu   sync.Mutex
	lastActivity Activity
	activitySent bool

	mu          sync.Mutex
	relay       Relay
	settings    config.Settings
	connected   bool
	revoked     bool
	lastSync    time.Time
	lastApplied time.Time

	// downloads counts downloads in flight, manual and automatic, for the
	// tray's icon; uploads, below, does the same for uploads.
	downloads int

	// everConnected is set by the first connection and cleared by leaving
	// the group: it is what tells "connecting" from "disconnected".
	everConnected bool

	// skipped is the latest automatic transfer the user's size limit held
	// back, or nil (localui.Status.Skipped).
	skipped   *localui.SkippedView
	lastError string

	// latestID is the newest entry this service knows the group holds: the
	// last one announced, uploaded, or found on connecting. latestKnown is
	// false until the first connection has said what that is.
	latestID    string
	latestKnown bool

	// uploads counts this device's own uploads in flight. While one is, an
	// announced entry is not applied but held in deferred: the upload will
	// reach the relay after it and win, so applying it would only leave this
	// clipboard and the group's latest disagreeing. lastUploaded is the
	// relay's timestamp on the newest entry this device uploaded, which is
	// what a held entry is judged against when the uploads end.
	uploads      int
	deferred     *tppclient.EntryMeta
	lastUploaded time.Time

	// fetching is the automatic download in flight, or nil. A newer
	// announcement or a local copy cancels it: either makes it stale.
	fetching *inflight

	plans       map[string]*plan
	offerPlans  map[string]*offerPlan
	offer       Offer
	offerCancel context.CancelFunc
}

// inflight is one automatic download, cancellable.
type inflight struct {
	createdAt time.Time
	cancel    context.CancelFunc
}

type plan struct {
	rev       Revocation
	expiresAt time.Time
}

type offerPlan struct {
	accept    OfferAcceptance
	expiresAt time.Time
}

// New builds the service. The relay is attached separately: the client's
// callbacks are wired to this service, so it must exist before the client
// does.
func New(opts Options) (*Service, error) {
	if opts.Clipboard == nil {
		return nil, errors.New("service: a clipboard is required")
	}
	if opts.Autostart == nil {
		return nil, errors.New("service: an autostart manager is required")
	}
	s := &Service{
		clip:       opts.Clipboard,
		auto:       opts.Autostart,
		updates:    opts.Updates,
		onActivity: opts.OnActivity,
		logger:     opts.Logger,
		now:        opts.Now,
		configDir:  opts.ConfigDir,
		backend:    opts.KeystoreBackend,
		listen:     opts.ListenPort,
		settings:   opts.Settings,
		incoming:   make(chan tppclient.EntryMeta, 1),
		plans:      map[string]*plan{},
		offerPlans: map[string]*offerPlan{},
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	s.hub = newHub(s.now)
	s.watcher = clipboard.NewWatcher(clipboard.WatchOptions{
		Clipboard: opts.Clipboard,
		Interval:  opts.WatchInterval,
		Paused:    opts.WatchPaused,
		Logger:    s.logger,
		Now:       s.now,
		OnChange:  s.onClipboardChange,
	})
	s.watcher.SetEnabled(s.settings.AutoWatch && opts.Clipboard.Available())
	return s, nil
}

// Attach gives the service its relay. It is called once, after the client has
// been built with ClientHandlers.
func (s *Service) Attach(r Relay) {
	s.mu.Lock()
	s.relay = r
	s.mu.Unlock()
	s.activityChanged()
}

// SetListenPort records the port the localhost server actually bound, which is
// only known after it binds: the settings screen shows it, and compares it
// with the configured one to say whether a restart is needed.
func (s *Service) SetListenPort(port int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listen = port
}

// Run watches the clipboard and applies entries other devices announce until
// ctx is done, then ends every UI event stream.
func (s *Service) Run(ctx context.Context) {
	defer s.hub.close()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.applyIncoming(ctx)
	}()
	s.watcher.Run(ctx)
	wg.Wait()
}

// ClientHandlers are the callbacks to hand to tppclient.New.
//
// None of them touches the clipboard. A rekey in particular must leave the
// local clipboard exactly as the user left it (SPEC §3.3), and the only thing
// these handlers do with one is refresh the screen that displays it.
func (s *Service) ClientHandlers() tppclient.Handlers {
	return tppclient.Handlers{
		OnConnected: func() {
			s.setConnected(true, "")
			s.hub.publish(localui.Event{Kind: localui.EventConnected})
			// On its own goroutine: this runs before the client's read loop
			// starts, and the request needs that loop to see its answer.
			s.catchUps.Go(func() { s.catchUp(context.Background()) })
		},
		OnDisconnected: func(err error) {
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			s.setConnected(false, msg)
			s.hub.publish(localui.Event{Kind: localui.EventDisconnected, Message: msg})
		},
		OnEpoch: func(epoch uint64) {
			s.hub.publish(localui.Event{Kind: localui.EventEpoch, Epoch: epoch})
		},
		OnDevicePaired: func(d tppclient.Device) {
			s.hub.publish(localui.Event{Kind: localui.EventDevicePaired, DeviceID: d.ID, Name: d.Name})
		},
		OnDeviceRevoked: func(deviceID string, epoch uint64) {
			s.hub.publish(localui.Event{Kind: localui.EventDeviceRevoked, DeviceID: deviceID, Epoch: epoch})
		},
		OnEntryAdded: s.announce,
		OnEntryEvicted: func(entryID string) {
			s.hub.publish(localui.Event{Kind: localui.EventEvicted, EntryID: entryID})
		},
		OnProgress: func(p tppclient.Progress) {
			dir := "upload"
			if p.Direction == tppclient.Download {
				dir = "download"
			}
			s.hub.publish(localui.Event{
				Kind: localui.EventProgress, EntryID: p.EntryID,
				Direction: dir, Done: p.Done, Total: p.Total,
			})
		},
		OnRevoked: func() {
			s.mu.Lock()
			s.revoked, s.connected = true, false
			s.mu.Unlock()
			s.activityChanged()
			s.hub.publish(localui.Event{
				Kind:    localui.EventRevoked,
				Message: "this device was revoked; pair it again to rejoin the group",
			})
		},
	}
}

// Subscribe implements localui.API.
func (s *Service) Subscribe() (<-chan localui.Event, func()) { return s.hub.subscribe() }

func (s *Service) setConnected(on bool, msg string) {
	s.mu.Lock()
	s.connected = on
	s.lastError = msg
	if on {
		s.revoked = false
		s.everConnected = true
	}
	s.mu.Unlock()
	s.activityChanged()
}

// Status implements localui.API.
func (s *Service) Status(ctx context.Context) localui.Status {
	s.mu.Lock()
	relay, connected, revoked, lastSync, lastErr := s.relay, s.connected, s.revoked, s.lastSync, s.lastError
	var skipped *localui.SkippedView
	if s.skipped != nil {
		v := *s.skipped // a copy: the UI must not see it change under it
		skipped = &v
	}
	s.mu.Unlock()

	st := localui.Status{
		Skipped:   skipped,
		Connected: connected,
		Revoked:   revoked,
		LastSync:  localui.Optional(lastSync),
		LastError: lastErr,
		Keystore:  s.backend,
		Clipboard: s.clipboardView(ctx),
	}
	if relay == nil {
		return st
	}
	state := relay.State()
	st.InGroup = relay.InGroup()
	st.Epoch = relay.Epoch()
	st.DeviceID = state.DeviceID
	st.DeviceName = state.DeviceName
	st.ServerURL = state.ServerURL
	if !st.InGroup || !connected {
		return st
	}

	latest, err := s.latest(ctx, relay)
	if err != nil {
		// A status poll that cannot reach the relay is a status, not a
		// failure: the screen shows what it knows and says why.
		st.LastError = err.Error()
		return st
	}
	st.Latest = latest
	st.Suggested, st.DirectionKnown = s.suggest(latest)
	return st
}

// suggest applies SPEC §6: upload when the local clipboard is newer than the
// relay's latest entry, download otherwise.
//
// The second return value is the honest part. Neither NSPasteboard nor the
// Windows clipboard records when its content arrived, so the only local
// timestamp this service has is one it observed itself while watching. Without
// one there is no comparison to make, and SPEC §6 says a client in that
// position offers two explicit buttons rather than guessing — which is what a
// false here tells the UI to do.
func (s *Service) suggest(latest *localui.EntryView) (localui.Direction, bool) {
	local := s.watcher.LastChange()
	switch {
	case latest == nil:
		return localui.DirectionUpload, !local.IsZero()
	case local.IsZero():
		return localui.DirectionDownload, false
	case !local.Before(latest.CreatedAt):
		// Not-older rather than newer: when the two timestamps are equal the
		// entry is the one this device wrote, and re-downloading its own copy
		// is not what "sync" means.
		return localui.DirectionUpload, true
	default:
		return localui.DirectionDownload, true
	}
}

func (s *Service) clipboardView(ctx context.Context) localui.ClipboardView {
	view := localui.ClipboardView{
		Available: s.clip.Available(),
		ChangedAt: localui.Optional(s.watcher.LastChange()),
	}
	if !view.Available {
		view.Empty = true
		view.Error = "this build cannot reach a clipboard"
		return view
	}
	c, err := s.clip.Read(ctx)
	switch {
	case errors.Is(err, clipboard.ErrEmpty):
		view.Empty = true
		return view
	case err != nil:
		view.Error = err.Error()
		return view
	}
	view.Kind = string(c.Kind())
	view.Filename = c.Filename
	view.Bytes = len(c.Body)
	view.Preview = preview(c)
	return view
}

// preview renders at most previewRunes of a text payload. Anything that is not
// text has no preview: rendering a file's bytes helps nobody, and the UI shows
// its kind and size instead.
func preview(c clipboard.Content) string {
	text := c.Text()
	if text == "" {
		return ""
	}
	if utf8.RuneCountInString(text) <= previewRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:previewRunes]) + "…"
}

// Sync implements localui.API: it moves the latest entry in one direction.
//
// It sets no overall deadline: a transfer takes as long as its size needs,
// and the client bounds each request in it instead (tppclient.CallTimeout).
func (s *Service) Sync(ctx context.Context, d localui.Direction) (localui.SyncResult, error) {
	relay, err := s.ready()
	if err != nil {
		return localui.SyncResult{}, err
	}

	if d == localui.DirectionAuto {
		latest, err := s.latest(ctx, relay)
		if err != nil {
			return localui.SyncResult{}, err
		}
		chosen, known := s.suggest(latest)
		if !known {
			return localui.SyncResult{}, localui.Errorf(http.StatusConflict, ErrDirectionUnknown,
				"this service cannot tell which side is newer: it has not seen the clipboard change. "+
					"Choose upload or download.")
		}
		d = chosen
	}

	switch d {
	case localui.DirectionUpload:
		return s.upload(ctx, relay)
	case localui.DirectionDownload:
		return s.download(ctx, relay)
	default:
		return localui.SyncResult{}, localui.Errorf(http.StatusBadRequest, nil, "unknown sync direction %q", d)
	}
}

func (s *Service) upload(ctx context.Context, relay Relay) (localui.SyncResult, error) {
	c, err := s.clip.Read(ctx)
	switch {
	case errors.Is(err, clipboard.ErrEmpty):
		return localui.SyncResult{Direction: localui.DirectionUpload, Message: "the clipboard is empty"}, nil
	case errors.Is(err, clipboard.ErrUnsupported):
		return localui.SyncResult{}, localui.Errorf(http.StatusNotImplemented, err,
			"this build cannot reach a clipboard")
	case err != nil:
		return localui.SyncResult{}, localui.Errorf(http.StatusInternalServerError, err,
			"the clipboard could not be read")
	}

	now := s.now()
	s.beginUpload()
	meta, err := relay.PutEntry(ctx, tppclient.Item{
		ContentType: c.ContentType,
		Filename:    c.Filename,
		Body:        c.Body,
		CreatedAt:   now,
	})
	if err != nil {
		s.endUpload(nil)
		return localui.SyncResult{}, s.relayError("upload the clipboard", err)
	}
	s.endUpload(&meta)
	s.watcher.MarkLocal(c, now)
	s.noteSync(now)
	s.noteLatest(meta.ID)

	s.logger.InfoContext(ctx, "clipboard uploaded", "entry_id", meta.ID, "epoch", meta.Epoch, "bytes", meta.Size)
	s.clearSkipped()
	s.hub.publish(localui.Event{Kind: localui.EventSync, Message: "uploaded"})
	return localui.SyncResult{
		Direction: localui.DirectionUpload,
		Changed:   true,
		Message:   "uploaded " + describe(c),
		Entry:     s.entryView(meta),
	}, nil
}

func (s *Service) download(ctx context.Context, relay Relay) (localui.SyncResult, error) {
	defer s.beginDownload()()
	item, err := relay.GetLatest(ctx)
	switch {
	case errors.Is(err, tppclient.ErrNoEntry):
		return localui.SyncResult{Direction: localui.DirectionDownload, Message: "the group has no entry yet"}, nil
	case errors.Is(err, tppclient.ErrStaleEntry):
		// SPEC §3.3: an entry from before a rekey is skipped silently. It is
		// not an error, and it is not something to retry.
		return localui.SyncResult{Direction: localui.DirectionDownload, Message: "nothing to sync"}, nil
	case errors.Is(err, tppclient.ErrEpochAhead):
		return localui.SyncResult{Direction: localui.DirectionDownload,
			Message: "waiting for this device's copy of the new group key"}, nil
	case err != nil:
		return localui.SyncResult{}, s.relayError("fetch the latest entry", err)
	}
	return s.applyToClipboard(ctx, localui.DirectionDownload, item)
}

// applyToClipboard writes a decrypted entry to the OS clipboard.
//
// The two watcher calls around the write are what stops the sync loop: without
// them the next poll sees content it did not put there, calls it a user copy,
// and uploads it again — once per poll, for as long as the service runs.
func (s *Service) applyToClipboard(ctx context.Context, d localui.Direction, item tppclient.Item) (localui.SyncResult, error) {
	c := clipboard.Content{
		ContentType: item.ContentType,
		Body:        item.Body,
	}
	if item.Filename != "" {
		c.Filename = clipboard.SafeName(item.Filename)
	}
	s.watcher.Suppress(c)
	if err := s.clip.Write(ctx, c); err != nil {
		if errors.Is(err, clipboard.ErrUnsupported) {
			return localui.SyncResult{}, localui.Errorf(http.StatusNotImplemented, err,
				"this build cannot reach a clipboard")
		}
		return localui.SyncResult{}, localui.Errorf(http.StatusInternalServerError, err,
			"the clipboard could not be written")
	}
	// The clipboard now holds this entry, so it is as of when the entry was
	// written, not as of whenever the watcher last saw a user copy. Telling
	// the watcher that is what keeps "changed 2 minutes ago" on the status
	// screen honest after a download.
	s.watcher.Rebase(ctx, item.Meta.CreatedAt)
	s.noteSync(s.now())

	s.logger.InfoContext(ctx, "entry written to the clipboard",
		"entry_id", item.Meta.ID, "epoch", item.Meta.Epoch, "bytes", len(item.Body))
	s.clearSkipped()
	s.hub.publish(localui.Event{Kind: localui.EventSync, Message: "copied to the clipboard"})
	return localui.SyncResult{
		Direction: d,
		Changed:   true,
		Message:   "copied " + describe(c) + " to the clipboard",
		Entry:     s.entryView(item.Meta),
	}, nil
}

// CopyEntry implements localui.API.
func (s *Service) CopyEntry(ctx context.Context, entryID string) (localui.SyncResult, error) {
	relay, err := s.ready()
	if err != nil {
		return localui.SyncResult{}, err
	}
	defer s.beginDownload()()
	item, err := relay.GetEntry(ctx, entryID)
	switch {
	case errors.Is(err, tppclient.ErrStaleEntry):
		return localui.SyncResult{}, localui.Errorf(http.StatusGone, err,
			"that entry predates this device's group key and cannot be opened")
	case err != nil:
		return localui.SyncResult{}, s.relayError("fetch that entry", err)
	}
	return s.applyToClipboard(ctx, localui.DirectionDownload, item)
}

// EntryPreview implements localui.API: it decrypts one entry and describes it
// for the screen, without touching the clipboard.
//
// An entry from an older epoch has no preview, and that is not an omission: a
// rekey replaced the key it was written under, so this device cannot read it
// at all (SPEC §3.3). The UI is told the same thing it would be told for a
// copy, so a history list can offer previews only for the rows it can honour.
func (s *Service) EntryPreview(ctx context.Context, entryID string) (localui.EntryPreview, error) {
	relay, err := s.ready()
	if err != nil {
		return localui.EntryPreview{}, err
	}
	item, err := relay.GetEntry(ctx, entryID)
	switch {
	case errors.Is(err, tppclient.ErrStaleEntry):
		return localui.EntryPreview{}, localui.Errorf(http.StatusGone, err,
			"that entry predates this device's group key and cannot be opened")
	case err != nil:
		return localui.EntryPreview{}, s.relayError("fetch that entry", err)
	}

	c := clipboard.Content{ContentType: item.ContentType, Body: item.Body}
	if item.Filename != "" {
		c.Filename = clipboard.SafeName(item.Filename)
	}
	// The history offers no preview for anything but text and images, from
	// the entry's header; this holds the line for an entry with no header, or
	// one whose header disagreed with what it holds.
	if headerKind(tppclient.EntryHeader{ContentType: item.ContentType, Filename: item.Filename}) == clipboard.KindFile {
		return localui.EntryPreview{}, localui.Errorf(http.StatusUnprocessableEntity, nil,
			"this entry has no preview; copy it instead")
	}

	view := localui.EntryPreview{
		ID:          item.Meta.ID,
		Epoch:       item.Meta.Epoch,
		ContentType: item.ContentType,
		Filename:    c.Filename,
		Bytes:       len(item.Body),
		Kind:        string(c.Kind()),
	}

	switch c.Kind() {
	case clipboard.KindText:
		text := c.Text()
		view.Text = preview(c)
		view.Truncated = utf8.RuneCountInString(text) > previewRunes
	case clipboard.KindImage:
		// Inlined as a data URL so the page renders it without a second
		// request for a decrypted body. Past the bound the screen says how big
		// it is instead: a browser that has to parse 20 MB of base64 to draw a
		// thumbnail is a browser that stops answering.
		if len(item.Body) > maxPreviewImageBytes {
			view.ImageTooLarge = true
			break
		}
		view.ImageDataURL = "data:" + item.ContentType + ";base64," +
			base64.StdEncoding.EncodeToString(item.Body)
	}

	s.logger.DebugContext(ctx, "entry previewed", "entry_id", item.Meta.ID, "kind", view.Kind)
	return view, nil
}

// History implements localui.API.
func (s *Service) History(ctx context.Context, q localui.HistoryQuery) (localui.HistoryPage, error) {
	relay, err := s.ready()
	if err != nil {
		return localui.HistoryPage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	page, err := relay.GetHistory(ctx, tppclient.HistoryQuery{Limit: q.Limit, Before: q.Before})
	if err != nil {
		return localui.HistoryPage{}, s.relayError("list the history", err)
	}
	out := localui.HistoryPage{
		Entries:    make([]localui.EntryView, 0, len(page.Entries)),
		NextBefore: localui.Optional(page.NextBefore),
	}
	for _, m := range page.Entries {
		out.Entries = append(out.Entries, *s.entryView(m))
	}
	return out, nil
}

// Devices implements localui.API.
func (s *Service) Devices(ctx context.Context) (localui.RosterView, error) {
	relay, err := s.ready()
	if err != nil {
		return localui.RosterView{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	roster, err := relay.Devices(ctx)
	if err != nil {
		return localui.RosterView{}, s.relayError("list the group's devices", err)
	}
	return rosterView(roster), nil
}

// PrepareRevoke implements localui.API. It changes nothing: it returns the
// roster the confirmation dialog must render (SPEC §3.3 steps 1-2).
func (s *Service) PrepareRevoke(ctx context.Context, deviceID string) (localui.RevokePlan, error) {
	relay, err := s.ready()
	if err != nil {
		return localui.RevokePlan{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	rev, err := relay.Revoke(ctx, deviceID)
	if err != nil {
		return localui.RevokePlan{}, s.relayError("prepare that revocation", err)
	}
	id, err := planID()
	if err != nil {
		return localui.RevokePlan{}, err
	}
	expires := s.now().Add(planTTL)

	s.mu.Lock()
	s.sweepPlansLocked()
	s.plans[id] = &plan{rev: rev, expiresAt: expires}
	s.mu.Unlock()

	view := localui.RevokePlan{
		ID:        id,
		Target:    deviceView(rev.Target()),
		Epoch:     rev.Roster().Epoch,
		ExpiresAt: expires,
	}
	for _, d := range rev.Remaining() {
		view.Remaining = append(view.Remaining, deviceView(d))
	}
	return view, nil
}

// ConfirmRevoke implements localui.API: it carries out a plan the user
// confirmed, and it is the only way a revocation happens.
func (s *Service) ConfirmRevoke(ctx context.Context, planID string) (localui.RosterView, error) {
	if _, err := s.ready(); err != nil {
		return localui.RosterView{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	s.mu.Lock()
	s.sweepPlansLocked()
	p, ok := s.plans[planID]
	delete(s.plans, planID)
	s.mu.Unlock()
	if !ok {
		return localui.RosterView{}, localui.Errorf(http.StatusConflict, nil,
			"that revocation is no longer pending; fetch the device list and try again")
	}

	roster, err := p.rev.Confirm(ctx)
	if err != nil {
		return localui.RosterView{}, s.relayError("revoke that device", err)
	}
	s.logger.InfoContext(ctx, "device revoked", "device_id", p.rev.Target().ID, "epoch", roster.Epoch)
	s.hub.publish(localui.Event{
		Kind:     localui.EventDeviceRevoked,
		DeviceID: p.rev.Target().ID,
		Epoch:    roster.Epoch,
	})
	return rosterView(roster), nil
}

func (s *Service) sweepPlansLocked() {
	now := s.now()
	for id, p := range s.plans {
		if now.After(p.expiresAt) {
			delete(s.plans, id)
		}
	}
	for id, p := range s.offerPlans {
		if now.After(p.expiresAt) {
			delete(s.offerPlans, id)
		}
	}
}

// StartOffer implements localui.API: this device, which is in no group, shows
// a code for a member of one to accept
// (docs/plans/joiner-emitted-pairing.md §3 steps 1-3).
//
// The relay URL is the one thing the user supplies in this direction, because
// a device with no group has no relay URL either. Showing a second code
// withdraws the first: two live codes for one device would be two ways in,
// and only one of them is on screen.
func (s *Service) StartOffer(ctx context.Context, serverURL string) (localui.OfferView, error) {
	s.mu.Lock()
	relay := s.relay
	s.mu.Unlock()
	if relay == nil {
		return localui.OfferView{}, localui.Errorf(http.StatusServiceUnavailable, nil, "the service is still starting")
	}

	dialCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	offer, err := relay.StartOffer(dialCtx, strings.TrimSpace(serverURL))
	if err != nil {
		return localui.OfferView{}, s.relayError("show a pairing code", err)
	}

	// The wait outlives this request: the code stays on screen until a member
	// reads it, which is minutes, not the seconds a round trip gets.
	waitCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	s.replaceOffer(offer, stop)
	go s.awaitOffer(waitCtx, offer)

	// The relay URL is the one the user just typed: a device with no group has
	// no other way to know it, which is also why the offer carries it.
	return localui.OfferView{
		Code:      offer.Code(),
		Link:      tppclient.PairingLink(strings.TrimSpace(serverURL), offer.Code()),
		ExpiresAt: offer.ExpiresAt(),
	}, nil
}

// CancelOffer implements localui.API. Withdrawing a code the user is no longer
// showing is not housekeeping: the socket it holds open is the only way the
// relay can reach this device, and leaving it open leaves a way in.
func (s *Service) CancelOffer(context.Context) error {
	s.replaceOffer(nil, nil)
	return nil
}

// replaceOffer installs the offer this device is showing and gives up whatever
// it was showing before.
func (s *Service) replaceOffer(offer Offer, cancel context.CancelFunc) {
	s.mu.Lock()
	previous, previousCancel := s.offer, s.offerCancel
	s.offer, s.offerCancel = offer, cancel
	s.mu.Unlock()

	if previousCancel != nil {
		previousCancel()
	}
	if previous != nil {
		previous.Close()
	}
}

// awaitOffer blocks until a member accepts the code this device is showing,
// and tells the UI either way.
func (s *Service) awaitOffer(ctx context.Context, offer Offer) {
	err := offer.Wait(ctx)

	s.mu.Lock()
	current := s.offer == offer
	if current {
		s.offer, s.offerCancel = nil, nil
	}
	s.mu.Unlock()
	if !current {
		// Superseded or withdrawn: the user is looking at something else, and
		// saying anything about this one would be noise.
		return
	}

	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.logger.WarnContext(ctx, "a pairing offer ended without being accepted", "error", err)
		s.hub.publish(localui.Event{Kind: localui.EventOffer, Message: err.Error()})
		return
	}
	s.logger.InfoContext(ctx, "this device was admitted to a group from a pairing offer")
	s.activityChanged()
	s.hub.publish(localui.Event{Kind: localui.EventConnected, Message: "paired"})
}

// PrepareAcceptOffer implements localui.API. It changes nothing: it returns
// the name and fingerprint the confirmation dialog must render
// (docs/plans/joiner-emitted-pairing.md §5).
func (s *Service) PrepareAcceptOffer(ctx context.Context, code string) (localui.OfferPlan, error) {
	relay, err := s.ready()
	if err != nil {
		return localui.OfferPlan{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	accept, err := relay.PrepareAcceptOffer(ctx, strings.TrimSpace(code))
	if err != nil {
		return localui.OfferPlan{}, s.relayError("read that pairing code", err)
	}
	id, err := planID()
	if err != nil {
		return localui.OfferPlan{}, err
	}
	expires := s.now().Add(planTTL)

	s.mu.Lock()
	s.sweepPlansLocked()
	s.offerPlans[id] = &offerPlan{accept: accept, expiresAt: expires}
	s.mu.Unlock()

	return localui.OfferPlan{
		ID:          id,
		DeviceName:  accept.DeviceName(),
		Fingerprint: accept.Fingerprint(),
		ExpiresAt:   expires,
	}, nil
}

// ConfirmAcceptOffer implements localui.API: it admits the device a plan
// describes, and it is the only way a device is admitted this way.
func (s *Service) ConfirmAcceptOffer(ctx context.Context, planID string) (localui.DeviceView, error) {
	if _, err := s.ready(); err != nil {
		return localui.DeviceView{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	s.mu.Lock()
	s.sweepPlansLocked()
	p, ok := s.offerPlans[planID]
	delete(s.offerPlans, planID)
	s.mu.Unlock()
	if !ok {
		return localui.DeviceView{}, localui.Errorf(http.StatusConflict, nil,
			"that pairing code is no longer pending; read it again and try once more")
	}

	device, err := p.accept.Confirm(ctx)
	if err != nil {
		return localui.DeviceView{}, s.relayError("admit that device", err)
	}
	s.logger.InfoContext(ctx, "device admitted from a pairing offer", "device_id", device.ID)
	return deviceView(device), nil
}

// StartPairing implements localui.API.
func (s *Service) StartPairing(ctx context.Context) (localui.Invite, error) {
	relay, err := s.ready()
	if err != nil {
		return localui.Invite{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	inv, err := relay.StartPairing(ctx)
	if err != nil {
		return localui.Invite{}, s.relayError("start pairing", err)
	}
	// The link is built here rather than in the client core because only this
	// side knows which relay this device is talking to, and a self-hosted
	// deployment has no other name for it.
	return localui.Invite{
		Payload:   inv.Payload,
		Link:      tppclient.PairingLink(relay.State().ServerURL, inv.Payload),
		ExpiresAt: inv.ExpiresAt,
	}, nil
}

// JoinPairing implements localui.API.
func (s *Service) JoinPairing(ctx context.Context, payload string) error {
	s.mu.Lock()
	relay := s.relay
	s.mu.Unlock()
	if relay == nil {
		return localui.Errorf(http.StatusServiceUnavailable, nil, "the service is still starting")
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	if err := relay.JoinPairing(ctx, strings.TrimSpace(payload)); err != nil {
		return s.relayError("join that pairing", err)
	}
	s.activityChanged()
	s.hub.publish(localui.Event{Kind: localui.EventConnected, Message: "paired"})
	return nil
}

// CreateGroup implements localui.API.
func (s *Service) CreateGroup(ctx context.Context, creationURL string) error {
	s.mu.Lock()
	relay := s.relay
	s.mu.Unlock()
	if relay == nil {
		return localui.Errorf(http.StatusServiceUnavailable, nil, "the service is still starting")
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	if err := relay.CreateGroup(ctx, strings.TrimSpace(creationURL)); err != nil {
		return s.relayError("create a group", err)
	}
	s.activityChanged()
	s.hub.publish(localui.Event{Kind: localui.EventConnected, Message: "group created"})
	return nil
}

// Forget implements localui.API: it disconnects this device from its group and
// deletes the keys it held, which is the only way to move a desktop from one
// group to another — a device holds exactly one group key at a time
// (/spec/crypto.md §7).
//
// Three things are cleaned up here rather than in the client core, because
// they are this shell's and not the protocol's: a pairing code still on screen
// (the socket holding it open is the relay's only way to reach a device with
// no identity), a prepared revocation or offer acceptance whose plan is about
// a group this device has just left, and the connection state the UI renders.
// The clipboard is not touched, and neither is the settings file: leaving a
// group is not a reason to forget the port or the device's name.
func (s *Service) Forget(context.Context) error {
	s.mu.Lock()
	relay := s.relay
	s.mu.Unlock()
	if relay == nil {
		return localui.Errorf(http.StatusServiceUnavailable, nil, "the service is still starting")
	}

	s.replaceOffer(nil, nil)
	if err := relay.Forget(); err != nil {
		return localui.Errorf(http.StatusInternalServerError, err,
			"this device's keys could not be deleted")
	}

	s.mu.Lock()
	s.connected = false
	s.revoked = false
	s.everConnected = false
	s.lastError = ""
	s.lastSync = time.Time{}
	s.latestID, s.latestKnown = "", false
	s.plans = map[string]*plan{}
	s.offerPlans = map[string]*offerPlan{}
	s.mu.Unlock()

	s.activityChanged()
	s.logger.Info("this device left its group at the user's request")
	s.hub.publish(localui.Event{
		Kind:    localui.EventDisconnected,
		Message: "this device left the group; its keys have been deleted",
	})
	return nil
}

// Settings implements localui.API.
func (s *Service) Settings(context.Context) localui.SettingsView {
	s.mu.Lock()
	set := s.settings
	s.mu.Unlock()
	return s.settingsView(set)
}

func (s *Service) listenPort() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listen
}

func (s *Service) settingsView(set config.Settings) localui.SettingsView {
	on, err := s.auto.Enabled()
	if err != nil {
		s.logger.Warn("could not read the login item", "error", err)
		on = set.Autostart
	}
	return localui.SettingsView{
		Port:                 set.Port,
		ListenPort:           s.listenPort(),
		AutoWatch:            s.watcher.Enabled(),
		AutoApply:            set.AutoApply && s.clip.Available(),
		AutoUploadMaxBytes:   set.AutoUploadLimit(),
		AutoDownloadMaxBytes: set.AutoDownloadLimit(),
		Autostart:            on,
		AutostartSupported:   s.auto.Available(),
		ClipboardSupported:   s.clip.Available(),
		DeviceName:           set.DeviceName,
		UpdateChannel:        set.Channel(),
		AutoUpdate:           set.AutoUpdate,
		NightlyCommit:        set.NightlyCommit,
		RestartRequired:      set.ListenPort() != s.listenPort(),
	}
}

// UpdateSettings implements localui.API.
//
// The port takes effect at the next launch and says so: the service binds once
// at startup, and silently rebinding underneath an open UI would break the
// page that asked for it.
func (s *Service) UpdateSettings(ctx context.Context, p localui.SettingsPatch) (localui.SettingsView, error) {
	s.mu.Lock()
	next := s.settings
	s.mu.Unlock()

	if p.Port != nil {
		next.Port = *p.Port
		if err := next.Validate(); err != nil {
			return localui.SettingsView{}, localui.Errorf(http.StatusBadRequest, err, "that port cannot be used")
		}
	}
	if p.DeviceName != nil {
		next.DeviceName = strings.TrimSpace(*p.DeviceName)
	}
	if p.AutoWatch != nil {
		if *p.AutoWatch && !s.clip.Available() {
			return localui.SettingsView{}, localui.Errorf(http.StatusNotImplemented, nil,
				"this build cannot reach a clipboard, so it cannot watch one")
		}
		next.AutoWatch = *p.AutoWatch
	}
	if p.AutoApply != nil {
		if *p.AutoApply && !s.clip.Available() {
			return localui.SettingsView{}, localui.Errorf(http.StatusNotImplemented, nil,
				"this build cannot reach a clipboard, so it cannot write one")
		}
		next.AutoApply = *p.AutoApply
	}
	if p.AutoUploadMaxBytes != nil {
		v := *p.AutoUploadMaxBytes
		next.AutoUploadMaxBytes = &v
	}
	if p.AutoDownloadMaxBytes != nil {
		v := *p.AutoDownloadMaxBytes
		next.AutoDownloadMaxBytes = &v
	}
	if p.AutoUploadMaxBytes != nil || p.AutoDownloadMaxBytes != nil {
		if err := next.Validate(); err != nil {
			return localui.SettingsView{}, localui.Errorf(http.StatusBadRequest, err,
				"a limit must be between 0 (no limit) and 1 TB")
		}
	}
	if p.UpdateChannel != nil {
		next.UpdateChannel = *p.UpdateChannel
	}
	if p.AutoUpdate != nil {
		next.AutoUpdate = *p.AutoUpdate
	}
	if p.NightlyCommit != nil {
		commit := strings.ToLower(strings.TrimSpace(*p.NightlyCommit))
		if commit != "" && !config.IsFullSHA(commit) {
			r, ok := s.updates.(CommitResolver)
			if !ok {
				return localui.SettingsView{}, localui.Errorf(http.StatusBadRequest, nil, "enter the full 40-character commit hash")
			}
			full, err := r.ResolveCommit(ctx, commit)
			if err != nil {
				return localui.SettingsView{}, fmt.Errorf("resolve commit %s: %w", commit, err)
			}
			commit = full
		}
		next.NightlyCommit = commit
	}
	if p.UpdateChannel != nil || p.NightlyCommit != nil {
		if err := next.Validate(); err != nil {
			return localui.SettingsView{}, localui.Errorf(http.StatusBadRequest, err, "%v", err)
		}
	}
	if p.Autostart != nil {
		on, err := autostart.Apply(s.auto, *p.Autostart)
		if err != nil {
			if errors.Is(err, autostart.ErrUnsupported) {
				return localui.SettingsView{}, localui.Errorf(http.StatusNotImplemented, err,
					"this platform has no login item in this build")
			}
			return localui.SettingsView{}, localui.Errorf(http.StatusInternalServerError, err,
				"the login item could not be changed")
		}
		next.Autostart = on
	}

	if err := config.Save(s.configDir, next); err != nil {
		return localui.SettingsView{}, localui.Errorf(http.StatusInternalServerError, err,
			"the settings could not be saved")
	}

	s.mu.Lock()
	s.settings = next
	s.mu.Unlock()
	s.watcher.SetEnabled(next.AutoWatch && s.clip.Available())
	if s.updates != nil {
		s.updates.SetPreferences(next)
	}

	s.logger.Info("settings updated",
		"auto_watch", next.AutoWatch, "auto_apply", next.AutoApply, "autostart", next.Autostart, "port", next.Port,
		"auto_upload_max_bytes", next.AutoUploadLimit(), "auto_download_max_bytes", next.AutoDownloadLimit(),
		"update_channel", next.Channel(), "auto_update", next.AutoUpdate)
	s.hub.publish(localui.Event{Kind: localui.EventSettings})
	return s.settingsView(next), nil
}

// onClipboardChange uploads a change the user made, when auto-watch is on. A
// change this service made itself never reaches here.
func (s *Service) onClipboardChange(ctx context.Context, c clipboard.Content, at time.Time) {
	relay, err := s.ready()
	if err != nil {
		return
	}
	s.hub.publish(localui.Event{Kind: localui.EventClipboard, Message: string(c.Kind())})
	// The user's copy is newer than anything still downloading: let that go
	// rather than have it land on top of this.
	s.cancelDownload(time.Time{})

	s.mu.Lock()
	limit := s.settings.AutoUploadLimit()
	s.mu.Unlock()
	if size := int64(len(c.Body)); limit > 0 && size > limit {
		// Over the user's limit for automatic uploads: left for them to send
		// by hand, which nothing here limits. Said once, on the screen.
		s.logger.InfoContext(ctx, "clipboard change not uploaded automatically: over the auto-upload limit",
			"bytes", size, "limit", limit)
		s.noteSkipped(localui.SkippedView{
			Direction: "upload", Bytes: size,
			Message: fmt.Sprintf("Not uploaded automatically: %s is over your %s auto-upload limit. Use Upload to send it.",
				formatBytes(size), formatBytes(limit)),
		})
		return
	}

	// No overall deadline: the client bounds each request by what it moves,
	// so a large copy on a slow link is slow rather than failed.
	s.beginUpload()
	meta, err := relay.PutEntry(ctx, tppclient.Item{
		ContentType: c.ContentType,
		Filename:    c.Filename,
		Body:        c.Body,
		CreatedAt:   at,
	})
	if err != nil {
		s.endUpload(nil)
		if ctx.Err() != nil {
			return // shutting down
		}
		s.logger.WarnContext(ctx, "auto-upload of a clipboard change failed", "error", err)
		s.noteSkipped(localui.SkippedView{
			Direction: "upload", Bytes: int64(len(c.Body)),
			Message: fmt.Sprintf("Automatic upload of %s failed: %s. Use Upload to try again.",
				formatBytes(int64(len(c.Body))), s.failureText(err)),
		})
		return
	}
	s.endUpload(&meta)
	s.noteSync(at)
	s.noteLatest(meta.ID)
	s.logger.InfoContext(ctx, "clipboard change uploaded", "entry_id", meta.ID, "epoch", meta.Epoch, "bytes", meta.Size)
	s.clearSkipped()
	s.hub.publish(localui.Event{Kind: localui.EventSync, Message: "uploaded"})
}

// announce queues an entry another device wrote. It runs on the client's read
// loop, so it never blocks: only the newest announcement matters (SPEC §6
// syncs the latest entry only), and one still waiting is replaced.
func (s *Service) announce(meta tppclient.EntryMeta) {
	s.noteLatest(meta.ID)
	// A download still running for an older entry is wasted work now.
	s.cancelDownload(meta.CreatedAt)
	s.enqueue(meta)
}

// enqueue hands an entry to applyIncoming, replacing one still waiting.
func (s *Service) enqueue(meta tppclient.EntryMeta) {
	for {
		select {
		case s.incoming <- meta:
			return
		default:
		}
		select {
		case <-s.incoming:
		default:
		}
	}
}

// noteLatest records the newest entry this service knows the group holds.
func (s *Service) noteLatest(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latestID, s.latestKnown = id, true
}

// catchUp handles what was written while this device was not listening.
//
// Announcements reach only a connected device, so an entry another device
// wrote while this one slept, or while the network was down, was never
// announced here. On every connection after the first, catchUp asks the relay
// for its newest entry and, when that is not the one this service last knew
// of, treats it as announced — and from there auto-apply decides, by the same
// rules as for any announcement, whether to put it on the clipboard.
//
// The first connection only records what is there. Nothing was missed before
// it, and an entry from before this service started is no more the user's
// latest intent than what is on the local clipboard; which of those wins is
// the question SPEC §6 says to ask the user rather than guess.
func (s *Service) catchUp(ctx context.Context) {
	relay, err := s.ready()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	page, err := relay.GetHistory(ctx, tppclient.HistoryQuery{Limit: 1})
	if err != nil {
		s.logger.DebugContext(ctx, "could not check for entries missed while offline", "error", err)
		return
	}
	if len(page.Entries) == 0 {
		// An empty group is a baseline too: whatever appears next was
		// written after this connection, and a later one must not miss it.
		s.mu.Lock()
		s.latestID, s.latestKnown = "", true
		s.mu.Unlock()
		return
	}
	meta := page.Entries[0]

	s.mu.Lock()
	missed := s.latestKnown && s.latestID != meta.ID
	s.latestID, s.latestKnown = meta.ID, true
	s.mu.Unlock()
	if missed {
		s.logger.InfoContext(ctx, "an entry was written while this device was offline", "entry_id", meta.ID)
		s.announce(meta)
	}
}

// Resumed tells the service the machine has just woken from sleep, after
// sleeping for about `slept`.
//
// The relay connection is all but certainly dead by then — the relay, or a
// NAT on the way to it, has forgotten it — and the client will not notice for
// up to a ping interval and a ping timeout. Reconnecting straight away also
// runs catchUp, so an entry written during the sleep arrives now rather than
// never.
func (s *Service) Resumed(slept time.Duration) {
	s.mu.Lock()
	relay := s.relay
	s.mu.Unlock()
	if relay == nil || !relay.InGroup() {
		return
	}
	s.logger.Info("woke from sleep; reconnecting to the relay", "slept", slept.Round(time.Second).String())
	relay.Reconnect()
}

// applyIncoming writes announced entries to the clipboard, when the user has
// asked for that, until ctx is done.
func (s *Service) applyIncoming(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case meta := <-s.incoming:
			s.applyAnnounced(ctx, meta)
		}
	}
}

// applyAnnounced fetches one announced entry and puts it on the clipboard.
//
// It stands down rather than overwrite something newer: an entry older than
// one already applied, or than a copy the user made here since, loses the
// same comparison a manual sync would make (SPEC §6).
func (s *Service) applyAnnounced(ctx context.Context, meta tppclient.EntryMeta) {
	s.mu.Lock()
	on, applied, limit := s.settings.AutoApply, s.lastApplied, s.settings.AutoDownloadLimit()
	held := on && s.holdLocked(meta)
	s.mu.Unlock()
	if held {
		return
	}
	if !on || !s.clip.Available() {
		return
	}
	if meta.CreatedAt.Before(applied) || meta.CreatedAt.Before(s.watcher.LastChange()) {
		return
	}
	if limit > 0 && meta.Size > limit {
		// Decided from the announcement's metadata, before a byte is
		// fetched: the point of the limit is not to download it.
		s.logger.InfoContext(ctx, "announced entry not applied automatically: over the auto-download limit",
			"entry_id", meta.ID, "bytes", meta.Size, "limit", limit)
		s.noteSkipped(localui.SkippedView{
			Direction: "download", EntryID: meta.ID, Bytes: meta.Size,
			Message: fmt.Sprintf("A %s entry arrived and was not copied here automatically: it is over your %s auto-download limit. Use Download or History to get it.",
				formatBytes(meta.Size), formatBytes(limit)),
		})
		return
	}
	relay, err := s.ready()
	if err != nil {
		return
	}
	defer s.beginDownload()()

	before := s.watcher.LastChange()
	ctx, done := s.startDownload(ctx, meta)
	defer done()
	item, err := relay.GetEntry(ctx, meta.ID)
	switch {
	case ctx.Err() != nil:
		// Superseded by a newer entry or a local copy, or shutting down.
		s.logger.DebugContext(ctx, "announced entry download cancelled", "entry_id", meta.ID)
		return
	case errors.Is(err, tppclient.ErrStaleEntry), errors.Is(err, tppclient.ErrEpochAhead):
		// Written under a key this device does not hold: skipped silently,
		// as a manual download would skip it (SPEC §3.3).
		s.logger.DebugContext(ctx, "announced entry skipped", "entry_id", meta.ID, "epoch", meta.Epoch)
		return
	case err != nil:
		s.logger.WarnContext(ctx, "fetching an announced entry failed", "entry_id", meta.ID, "error", err)
		return
	}
	// The download took time, and the world may have moved meanwhile. An
	// upload of this device's own that started since will win at the relay,
	// so the entry waits for it; a copy the user made since — reported, or
	// still waiting for the next poll — is newer than this entry, so the
	// entry stands down rather than overwrite it.
	s.mu.Lock()
	held = s.holdLocked(meta)
	s.mu.Unlock()
	if held {
		return
	}
	if !s.watcher.LastChange().Equal(before) || s.watcher.Pending(ctx) {
		s.logger.InfoContext(ctx, "announced entry not applied: the clipboard changed here while it downloaded",
			"entry_id", meta.ID)
		return
	}
	if _, err := s.applyToClipboard(ctx, localui.DirectionDownload, item); err != nil {
		s.logger.WarnContext(ctx, "applying an announced entry failed", "entry_id", meta.ID, "error", err)
		return
	}
	s.mu.Lock()
	if item.Meta.CreatedAt.After(s.lastApplied) {
		s.lastApplied = item.Meta.CreatedAt
	}
	s.mu.Unlock()
}

// beginUpload marks one of this device's own uploads as in flight. It
// cancels an automatic download: the upload will be the group's latest.
func (s *Service) beginUpload() {
	s.cancelDownload(time.Time{})
	s.mu.Lock()
	s.uploads++
	s.mu.Unlock()
	s.activityChanged()
}

// endUpload marks an upload finished; meta is nil when it failed. When the
// last one ends, an entry held while they ran is applied after all if it is
// newer than what this device uploaded — that is, if it reached the relay
// later, or this device's upload never did.
func (s *Service) endUpload(meta *tppclient.EntryMeta) {
	s.mu.Lock()
	s.uploads--
	if meta != nil && meta.CreatedAt.After(s.lastUploaded) {
		s.lastUploaded = meta.CreatedAt
	}
	var again *tppclient.EntryMeta
	if s.uploads == 0 && s.deferred != nil {
		if s.deferred.CreatedAt.After(s.lastUploaded) {
			again = s.deferred
		}
		s.deferred = nil
	}
	s.mu.Unlock()
	s.activityChanged()
	if again != nil {
		s.enqueue(*again)
	}
}

// holdLocked holds an announced entry while an upload of this device's own is
// in flight, keeping only the newest, and reports whether it did. s.mu is
// held.
func (s *Service) holdLocked(meta tppclient.EntryMeta) bool {
	if s.uploads == 0 {
		return false
	}
	if s.deferred == nil || meta.CreatedAt.After(s.deferred.CreatedAt) {
		m := meta
		s.deferred = &m
	}
	s.logger.Debug("announced entry held: an upload from this device is in flight", "entry_id", meta.ID)
	return true
}

// startDownload registers an automatic download and returns its context and
// the function that unregisters it.
func (s *Service) startDownload(ctx context.Context, meta tppclient.EntryMeta) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	d := &inflight{createdAt: meta.CreatedAt, cancel: cancel}
	s.mu.Lock()
	s.fetching = d
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		if s.fetching == d {
			s.fetching = nil
		}
		s.mu.Unlock()
	}
}

// cancelDownload cancels the automatic download in flight when it is for an
// entry older than newer; a zero newer cancels it whatever it is.
func (s *Service) cancelDownload(newer time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fetching != nil && (newer.IsZero() || newer.After(s.fetching.createdAt)) {
		s.fetching.cancel()
	}
}

// noteSkipped records an automatic transfer held back by a size limit as the
// one notice the UI shows, replacing any older one: only the latest matters,
// because a newer entry is what the user would sync next anyway.
func (s *Service) noteSkipped(v localui.SkippedView) {
	v.At = s.now()
	s.mu.Lock()
	s.skipped = &v
	s.mu.Unlock()
	s.activityChanged()
	s.hub.publish(localui.Event{
		Kind: localui.EventSkipped, Direction: v.Direction, EntryID: v.EntryID, Total: v.Bytes, Message: v.Message,
	})
}

// clearSkipped drops the notice. It is called when a newer entry lands on
// either side — this device uploaded something, or put an entry on its
// clipboard — at which point what was skipped is no longer the thing to act
// on.
func (s *Service) clearSkipped() {
	s.mu.Lock()
	s.skipped = nil
	s.mu.Unlock()
	s.activityChanged()
}

// DismissSkipped implements localui.API.
func (s *Service) DismissSkipped(context.Context) {
	s.clearSkipped()
	s.hub.publish(localui.Event{Kind: localui.EventSkipped})
}

// formatBytes renders a size the way the settings screen states limits: whole
// or one-decimal megabytes (MiB), kilobytes below one.
func formatBytes(n int64) string {
	const mb = 1 << 20
	switch {
	case n >= mb && n%mb == 0:
		return fmt.Sprintf("%d MB", n/mb)
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/mb)
	default:
		return fmt.Sprintf("%.0f KB", math.Ceil(float64(n)/1024))
	}
}

// NotifyUpdate tells every open UI the update state changed.
func (s *Service) NotifyUpdate() {
	s.hub.publish(localui.Event{Kind: localui.EventUpdate})
}

// LastSync is when the clipboard last moved in either direction, or zero if
// it has not since this process started. The updater waits for it to be a
// while ago before restarting the service on its own.
func (s *Service) LastSync() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSync
}

func (s *Service) noteSync(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSync = at
}

// latest returns the relay's newest entry as metadata only.
//
// Metadata, not content: deciding a direction needs a timestamp, and pulling a
// 50 MB ciphertext to read one would be a download the user never asked for.
func (s *Service) latest(ctx context.Context, relay Relay) (*localui.EntryView, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	page, err := relay.GetHistory(ctx, tppclient.HistoryQuery{Limit: 1})
	if err != nil {
		return nil, s.relayError("read the group's latest entry", err)
	}
	if len(page.Entries) == 0 {
		return nil, nil
	}
	return s.entryView(page.Entries[0]), nil
}

// ready returns the relay once the service has one and this device is in a
// group.
func (s *Service) ready() (Relay, error) {
	s.mu.Lock()
	relay, revoked := s.relay, s.revoked
	s.mu.Unlock()
	switch {
	case relay == nil:
		return nil, localui.Errorf(http.StatusServiceUnavailable, nil, "the service is still starting")
	case revoked:
		return nil, localui.Errorf(http.StatusForbidden, nil,
			"this device was revoked; pair it again to rejoin the group")
	case !relay.InGroup():
		return nil, localui.Errorf(http.StatusConflict, tppclient.ErrNoGroup,
			"this device is not in a group yet: create one or pair with a device that is")
	}
	return relay, nil
}

// relayError turns a client-core failure into something a person can act on.
// The mapping is by sentinel, never by message (SPEC §5.1).
func (s *Service) relayError(op string, err error) error {
	switch {
	case errors.Is(err, tppclient.ErrNoGroup):
		return localui.Errorf(http.StatusConflict, err, "this device is not in a group yet")
	case errors.Is(err, tppclient.ErrRevoked), errors.Is(err, tppclient.ErrUnauthenticated):
		return localui.Errorf(http.StatusForbidden, err,
			"this device was revoked; pair it again to rejoin the group")
	case errors.Is(err, tppclient.ErrEpochConflict):
		return localui.Errorf(http.StatusConflict, err,
			"another device changed the group key first; refresh and try again")
	case errors.Is(err, tppclient.ErrEpochAhead):
		return localui.Errorf(http.StatusConflict, err,
			"this device is waiting for its copy of the new group key")
	case errors.Is(err, tppclient.ErrTooLarge):
		return localui.Errorf(http.StatusRequestEntityTooLarge, err,
			"that is larger than the relay accepts")
	case errors.Is(err, tppclient.ErrQuotaExceeded):
		return localui.Errorf(http.StatusInsufficientStorage, err,
			"the relay's storage is full right now; try again shortly")
	case errors.Is(err, tppclient.ErrTokenConsumed):
		return localui.Errorf(http.StatusConflict, err, "that token has already been used")
	case errors.Is(err, tppclient.ErrTokenExpired):
		return localui.Errorf(http.StatusConflict, err, "that token has expired; generate a new one")
	case errors.Is(err, tppclient.ErrRateLimited):
		return localui.Errorf(http.StatusTooManyRequests, err, "the relay is rate limiting; try again shortly")
	case errors.Is(err, tppclient.ErrNotFound):
		return localui.Errorf(http.StatusNotFound, err, "the relay has no such record")
	case errors.Is(err, tppclient.ErrNoEntry):
		return localui.Errorf(http.StatusNotFound, err, "the group has no entry")
	}
	return localui.Errorf(http.StatusBadGateway, err, "the service could not %s", op)
}

// failureText is the short, content-free reason relayError would give.
func (s *Service) failureText(err error) string {
	var se *localui.StatusError
	if errors.As(s.relayError("reach the relay", err), &se) {
		return se.Message
	}
	return "the relay could not be reached"
}

func planID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("service: generate a revocation plan id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (s *Service) entryView(m tppclient.EntryMeta) *localui.EntryView {
	s.mu.Lock()
	limit := s.settings.AutoDownloadLimit()
	s.mu.Unlock()
	return entryView(m, limit)
}

// entryView describes an entry for the screen. Whether it can be previewed is
// decided here, from its header, so the history never fetches an entry it
// would not show: only text and images are previewed, and only within the
// auto-download limit — a preview is a download, and one the user did not
// ask for past that limit is the same download the limit holds back.
func entryView(m tppclient.EntryMeta, downloadLimit int64) *localui.EntryView {
	v := &localui.EntryView{
		ID:        m.ID,
		Epoch:     m.Epoch,
		Size:      m.Size,
		CreatedAt: m.CreatedAt,
		ExpiresAt: m.ExpiresAt,
	}
	h := m.Header
	if h == nil {
		v.NoPreview = "type unknown"
		return v
	}
	size := h.Size
	v.ContentType = h.ContentType
	v.Bytes = &size
	if h.Filename != "" {
		v.Filename = clipboard.SafeName(h.Filename)
	}
	v.Kind = string(headerKind(*h))
	switch {
	case v.Kind == string(clipboard.KindFile):
		v.NoPreview = "no preview for this type"
	case downloadLimit > 0 && size > downloadLimit:
		v.NoPreview = "larger than the auto-download limit"
	default:
		v.Previewable = true
	}
	return v
}

// headerKind classifies an entry from its header. Stricter than
// clipboard.Content.Kind, which calls anything unnamed text: here only a
// text/* type is text, because a preview of it renders the bytes.
func headerKind(h tppclient.EntryHeader) clipboard.Kind {
	switch {
	case h.Filename != "":
		return clipboard.KindFile
	case strings.HasPrefix(h.ContentType, "image/"):
		return clipboard.KindImage
	case strings.HasPrefix(h.ContentType, "text/"):
		return clipboard.KindText
	default:
		return clipboard.KindFile
	}
}

func deviceView(d tppclient.Device) localui.DeviceView {
	return localui.DeviceView{
		ID:        d.ID,
		Name:      d.Name,
		CreatedAt: d.CreatedAt,
		LastSeen:  localui.Optional(d.LastSeen),
		This:      d.This,
	}
}

func rosterView(r tppclient.Roster) localui.RosterView {
	out := localui.RosterView{Epoch: r.Epoch, Devices: make([]localui.DeviceView, 0, len(r.Devices))}
	for _, d := range r.Devices {
		out.Devices = append(out.Devices, deviceView(d))
	}
	return out
}

// describe names a payload for a status message. It never includes content.
func describe(c clipboard.Content) string {
	switch c.Kind() {
	case clipboard.KindFile:
		return fmt.Sprintf("a file (%s, %d bytes)", c.Filename, len(c.Body))
	case clipboard.KindImage:
		return fmt.Sprintf("an image (%d bytes)", len(c.Body))
	default:
		return fmt.Sprintf("%d bytes of text", len(c.Body))
	}
}
