package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
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
// leaves this machine; it is bounded because a 10 MB paste in a status
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

	// Logger receives service-level logs. It never sees clipboard content.
	Logger *slog.Logger

	// Now supplies UTC timestamps; tests replace it.
	Now func() time.Time
}

// Service is the desktop shell: it answers the UI, drives the clipboard, and
// owns the sync direction decision of SPEC §6.
type Service struct {
	clip      clipboard.Clipboard
	watcher   *clipboard.Watcher
	auto      autostart.Manager
	hub       *hub
	logger    *slog.Logger
	now       func() time.Time
	configDir string
	backend   string
	listen    int

	mu          sync.Mutex
	relay       Relay
	settings    config.Settings
	connected   bool
	revoked     bool
	lastSync    time.Time
	lastError   string
	plans       map[string]*plan
	offerPlans  map[string]*offerPlan
	offer       Offer
	offerCancel context.CancelFunc
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
		logger:     opts.Logger,
		now:        opts.Now,
		configDir:  opts.ConfigDir,
		backend:    opts.KeystoreBackend,
		listen:     opts.ListenPort,
		settings:   opts.Settings,
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
	defer s.mu.Unlock()
	s.relay = r
}

// SetListenPort records the port the localhost server actually bound, which is
// only known after it binds: the settings screen shows it, and compares it
// with the configured one to say whether a restart is needed.
func (s *Service) SetListenPort(port int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listen = port
}

// Run watches the clipboard until ctx is done, then ends every UI event
// stream.
func (s *Service) Run(ctx context.Context) {
	defer s.hub.close()
	s.watcher.Run(ctx)
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
		OnRevoked: func() {
			s.mu.Lock()
			s.revoked, s.connected = true, false
			s.mu.Unlock()
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
	defer s.mu.Unlock()
	s.connected = on
	s.lastError = msg
	if on {
		s.revoked = false
	}
}

// Status implements localui.API.
func (s *Service) Status(ctx context.Context) localui.Status {
	s.mu.Lock()
	relay, connected, revoked, lastSync, lastErr := s.relay, s.connected, s.revoked, s.lastSync, s.lastError
	s.mu.Unlock()

	st := localui.Status{
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
func (s *Service) Sync(ctx context.Context, d localui.Direction) (localui.SyncResult, error) {
	relay, err := s.ready()
	if err != nil {
		return localui.SyncResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	if d == localui.DirectionAuto {
		latest, err := s.latest(ctx, relay)
		if err != nil {
			return localui.SyncResult{}, err
		}
		chosen, known := s.suggest(latest)
		if !known {
			return localui.SyncResult{}, localui.Errorf(http.StatusConflict, nil,
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
	meta, err := relay.PutEntry(ctx, tppclient.Item{
		ContentType: c.ContentType,
		Filename:    c.Filename,
		Body:        c.Body,
		CreatedAt:   now,
	})
	if err != nil {
		return localui.SyncResult{}, s.relayError("upload the clipboard", err)
	}
	s.watcher.MarkLocal(c, now)
	s.noteSync(now)

	s.logger.InfoContext(ctx, "clipboard uploaded", "entry_id", meta.ID, "epoch", meta.Epoch, "bytes", meta.Size)
	s.hub.publish(localui.Event{Kind: localui.EventSync, Message: "uploaded"})
	return localui.SyncResult{
		Direction: localui.DirectionUpload,
		Changed:   true,
		Message:   "uploaded " + describe(c),
		Entry:     entryView(meta),
	}, nil
}

func (s *Service) download(ctx context.Context, relay Relay) (localui.SyncResult, error) {
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
	s.hub.publish(localui.Event{Kind: localui.EventSync, Message: "copied to the clipboard"})
	return localui.SyncResult{
		Direction: d,
		Changed:   true,
		Message:   "copied " + describe(c) + " to the clipboard",
		Entry:     entryView(item.Meta),
	}, nil
}

// CopyEntry implements localui.API.
func (s *Service) CopyEntry(ctx context.Context, entryID string) (localui.SyncResult, error) {
	relay, err := s.ready()
	if err != nil {
		return localui.SyncResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

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
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

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
		out.Entries = append(out.Entries, *entryView(m))
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

	return localui.OfferView{Code: offer.Code(), ExpiresAt: offer.ExpiresAt()}, nil
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
	return localui.Invite{Payload: inv.Payload, ExpiresAt: inv.ExpiresAt}, nil
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
	s.lastError = ""
	s.lastSync = time.Time{}
	s.plans = map[string]*plan{}
	s.offerPlans = map[string]*offerPlan{}
	s.mu.Unlock()

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
		Port:               set.Port,
		ListenPort:         s.listenPort(),
		AutoWatch:          s.watcher.Enabled(),
		Autostart:          on,
		AutostartSupported: s.auto.Available(),
		ClipboardSupported: s.clip.Available(),
		DeviceName:         set.DeviceName,
		RestartRequired:    set.ListenPort() != s.listenPort(),
	}
}

// UpdateSettings implements localui.API.
//
// The port takes effect at the next launch and says so: the service binds once
// at startup, and silently rebinding underneath an open UI would break the
// page that asked for it.
func (s *Service) UpdateSettings(_ context.Context, p localui.SettingsPatch) (localui.SettingsView, error) {
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

	s.logger.Info("settings updated",
		"auto_watch", next.AutoWatch, "autostart", next.Autostart, "port", next.Port)
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

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	meta, err := relay.PutEntry(ctx, tppclient.Item{
		ContentType: c.ContentType,
		Filename:    c.Filename,
		Body:        c.Body,
		CreatedAt:   at,
	})
	if err != nil {
		s.logger.WarnContext(ctx, "auto-upload of a clipboard change failed", "error", err)
		return
	}
	s.noteSync(at)
	s.logger.InfoContext(ctx, "clipboard change uploaded", "entry_id", meta.ID, "epoch", meta.Epoch, "bytes", meta.Size)
	s.hub.publish(localui.Event{Kind: localui.EventSync, Message: "uploaded"})
}

func (s *Service) noteSync(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSync = at
}

// latest returns the relay's newest entry as metadata only.
//
// Metadata, not content: deciding a direction needs a timestamp, and pulling a
// 10 MB ciphertext to read one would be a download the user never asked for.
func (s *Service) latest(ctx context.Context, relay Relay) (*localui.EntryView, error) {
	page, err := relay.GetHistory(ctx, tppclient.HistoryQuery{Limit: 1})
	if err != nil {
		return nil, s.relayError("read the group's latest entry", err)
	}
	if len(page.Entries) == 0 {
		return nil, nil
	}
	return entryView(page.Entries[0]), nil
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
			"that is larger than the 10 MB the relay accepts")
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

func planID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("service: generate a revocation plan id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func entryView(m tppclient.EntryMeta) *localui.EntryView {
	return &localui.EntryView{
		ID:        m.ID,
		Epoch:     m.Epoch,
		Size:      m.Size,
		CreatedAt: m.CreatedAt,
		ExpiresAt: m.ExpiresAt,
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
