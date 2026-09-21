package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/autostart"
	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
	"github.com/Mark7888/two-place-paste/desktop/internal/config"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// fakeRelay is the client core reduced to a map. The flows it stands in for
// are tested against a real relay in pkg/tppclient's integration test; what is
// tested here is this package's own decisions.
type fakeRelay struct {
	mu      sync.Mutex
	entries []tppclient.Item
	inGroup bool
	putErr  error
	getErr  error
	now     func() time.Time
	rev     *fakeRevocation
	offer   *fakeOffer
	accept  *fakeAcceptance
	forgot  bool
}

func newFakeRelay(now func() time.Time) *fakeRelay {
	return &fakeRelay{inGroup: true, now: now}
}

func (f *fakeRelay) State() tppclient.State {
	return tppclient.State{DeviceID: "d1", DeviceName: "laptop", GroupID: "g1", ServerURL: "https://relay.example"}
}
func (f *fakeRelay) InGroup() bool                             { return f.inGroup }
func (f *fakeRelay) Epoch() uint64                             { return 3 }
func (f *fakeRelay) Connect(context.Context) error             { return nil }
func (f *fakeRelay) CreateGroup(context.Context, string) error { return nil }
func (f *fakeRelay) JoinPairing(context.Context, string) error { return nil }

// Forget mirrors the real client: the group goes, and the device is left as a
// first launch would leave it.
func (f *fakeRelay) Forget() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgot = true
	f.inGroup = false
	f.entries = nil
	return nil
}

func (f *fakeRelay) StartPairing(context.Context) (*tppclient.Invitation, error) {
	return &tppclient.Invitation{Payload: "tpp-pair-payload"}, nil
}

func (f *fakeRelay) Devices(context.Context) (tppclient.Roster, error) {
	return tppclient.Roster{Epoch: 3, Devices: []tppclient.Device{
		{ID: "d1", Name: "laptop", This: true},
		{ID: "d2", Name: "phone"},
	}}, nil
}

func (f *fakeRelay) Revoke(_ context.Context, deviceID string) (Revocation, error) {
	roster, _ := f.Devices(context.Background())
	target, ok := roster.Find(deviceID)
	if !ok {
		return nil, tppclient.ErrNotFound
	}
	var remaining []tppclient.Device
	for _, d := range roster.Devices {
		if d.ID != deviceID {
			remaining = append(remaining, d)
		}
	}
	f.rev = &fakeRevocation{roster: roster, target: target, remaining: remaining}
	return f.rev, nil
}

func (f *fakeRelay) PutEntry(_ context.Context, item tppclient.Item) (tppclient.EntryMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return tppclient.EntryMeta{}, f.putErr
	}
	item.Meta = tppclient.EntryMeta{
		ID:        fmt.Sprintf("e%d", len(f.entries)),
		Epoch:     3,
		Size:      int64(len(item.Body)),
		CreatedAt: item.CreatedAt,
	}
	f.entries = append(f.entries, item)
	return item.Meta, nil
}

func (f *fakeRelay) GetLatest(context.Context) (tppclient.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return tppclient.Item{}, f.getErr
	}
	if len(f.entries) == 0 {
		return tppclient.Item{}, tppclient.ErrNoEntry
	}
	return f.entries[len(f.entries)-1], nil
}

func (f *fakeRelay) GetHistory(_ context.Context, q tppclient.HistoryQuery) (tppclient.History, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return tppclient.History{}, f.getErr
	}
	var out tppclient.History
	for i := len(f.entries) - 1; i >= 0; i-- {
		if q.Limit > 0 && uint32(len(out.Entries)) >= q.Limit {
			break
		}
		out.Entries = append(out.Entries, f.entries[i].Meta)
	}
	return out, nil
}

func (f *fakeRelay) GetEntry(_ context.Context, entryID string) (tppclient.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.entries {
		if e.Meta.ID == entryID {
			return e, nil
		}
	}
	return tppclient.Item{}, tppclient.ErrNotFound
}

func (f *fakeRelay) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries)
}

func (f *fakeRelay) push(item tppclient.Item) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item.Meta = tppclient.EntryMeta{ID: "pushed", Epoch: 3, Size: int64(len(item.Body)), CreatedAt: item.CreatedAt}
	f.entries = append(f.entries, item)
}

func (f *fakeRelay) StartOffer(_ context.Context, serverURL string) (Offer, error) {
	if serverURL == "" {
		return nil, fmt.Errorf("a relay URL is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offer = &fakeOffer{code: "tpp-offer-code", accepted: make(chan struct{})}
	return f.offer, nil
}

func (f *fakeRelay) PrepareAcceptOffer(_ context.Context, code string) (OfferAcceptance, error) {
	if code == "" {
		return nil, tppclient.ErrNotFound
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accept = &fakeAcceptance{name: "phone", fingerprint: "6668 7AAD F862 BD77"}
	return f.accept, nil
}

// fakeOffer stands in for a code this device is showing. Wait blocks until the
// test either accepts it or closes it, which is what the real one does.
type fakeOffer struct {
	code     string
	accepted chan struct{}
	closed   atomic.Bool
}

func (o *fakeOffer) Code() string         { return o.code }
func (o *fakeOffer) ExpiresAt() time.Time { return time.Time{} }

func (o *fakeOffer) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-o.accepted:
		return nil
	}
}

func (o *fakeOffer) Close() {
	if o.closed.CompareAndSwap(false, true) {
		close(o.accepted)
	}
}

type fakeAcceptance struct {
	name        string
	fingerprint string
	confirmed   atomic.Int32
}

func (a *fakeAcceptance) DeviceName() string  { return a.name }
func (a *fakeAcceptance) Fingerprint() string { return a.fingerprint }

func (a *fakeAcceptance) Confirm(context.Context) (tppclient.Device, error) {
	a.confirmed.Add(1)
	return tppclient.Device{ID: "d3", Name: a.name}, nil
}

type fakeRevocation struct {
	roster    tppclient.Roster
	target    tppclient.Device
	remaining []tppclient.Device
	confirmed int
}

func (r *fakeRevocation) Roster() tppclient.Roster      { return r.roster }
func (r *fakeRevocation) Target() tppclient.Device      { return r.target }
func (r *fakeRevocation) Remaining() []tppclient.Device { return r.remaining }

func (r *fakeRevocation) Confirm(context.Context) (tppclient.Roster, error) {
	r.confirmed++
	return tppclient.Roster{Epoch: r.roster.Epoch + 1, Devices: r.remaining}, nil
}

// memClipboard is an in-memory clipboard with no change counter, which is the
// harder of the two platforms to get right.
type memClipboard struct {
	mu      sync.Mutex
	content clipboard.Content
	empty   bool
	writes  int
}

func newMemClipboard() *memClipboard { return &memClipboard{empty: true} }

func (m *memClipboard) Available() bool { return true }

func (m *memClipboard) Read(context.Context) (clipboard.Content, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.empty {
		return clipboard.Content{}, clipboard.ErrEmpty
	}
	return m.content, nil
}

func (m *memClipboard) Write(_ context.Context, c clipboard.Content) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.content, m.empty = c, false
	m.writes++
	return nil
}

func (m *memClipboard) Sequence(context.Context) (uint64, error) {
	return 0, clipboard.ErrNoSequence
}

func (m *memClipboard) set(c clipboard.Content) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.content, m.empty = c, false
}

type noAutostart struct{}

func (noAutostart) Available() bool        { return false }
func (noAutostart) Enabled() (bool, error) { return false, nil }
func (noAutostart) Enable(string) error    { return autostart.ErrUnsupported }
func (noAutostart) Disable() error         { return autostart.ErrUnsupported }

func textContent(s string) clipboard.Content {
	return clipboard.Content{ContentType: clipboard.TypeText, Body: []byte(s)}
}

type fixture struct {
	svc   *Service
	relay *fakeRelay
	clip  *memClipboard
	now   func() time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	clock := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	clip := newMemClipboard()
	svc, err := New(Options{
		Clipboard:     clip,
		Autostart:     noAutostart{},
		Settings:      config.Settings{},
		ConfigDir:     t.TempDir(),
		ListenPort:    config.DefaultPort,
		WatchInterval: time.Millisecond,
		Now:           now,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	relay := newFakeRelay(now)
	svc.Attach(relay)
	svc.ClientHandlers().OnConnected()
	return &fixture{svc: svc, relay: relay, clip: clip, now: now}
}

// TestSyncLoopIsClosed is the loop test of ROADMAP P6 at the service level: a
// download writes the clipboard, and the watcher that is running at the same
// time must not turn that write into an upload.
func TestSyncLoopIsClosed(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()

	// Another device wrote an entry.
	f.relay.push(tppclient.Item{
		ContentType: clipboard.TypeText,
		Body:        []byte("from the phone"),
		CreatedAt:   f.now(),
	})

	// Auto-watch is on: this is the configuration where a loop would run away.
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{AutoWatch: boolPtr(true)}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); f.svc.Run(runCtx) }()

	res, err := f.svc.Sync(ctx, localui.DirectionDownload)
	if err != nil {
		t.Fatalf("Sync(download) error = %v", err)
	}
	if !res.Changed {
		t.Fatalf("Sync(download) = %+v, want a change", res)
	}
	if got := string(f.clip.content.Body); got != "from the phone" {
		t.Fatalf("clipboard = %q, want the downloaded entry", got)
	}

	// Give the watcher many ticks to do the wrong thing.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if n := f.relay.count(); n != 1 {
			t.Fatalf("the service uploaded its own clipboard write: %d entries, want 1", n)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done

	if n := f.relay.count(); n != 1 {
		t.Fatalf("entries = %d, want 1: a service write became an upload", n)
	}
}

func TestSyncDirection(t *testing.T) {
	t.Parallel()

	entryAt := time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		localChange  time.Time
		hasEntry     bool
		wantKnown    bool
		wantDirected localui.Direction
	}{
		{
			name:         "a clipboard change this service saw is newer",
			localChange:  entryAt.Add(time.Minute),
			hasEntry:     true,
			wantKnown:    true,
			wantDirected: localui.DirectionUpload,
		},
		{
			name:         "an entry newer than the last observed copy wins",
			localChange:  entryAt.Add(-time.Minute),
			hasEntry:     true,
			wantKnown:    true,
			wantDirected: localui.DirectionDownload,
		},
		{
			name:      "no observed change and an entry is not a comparison",
			hasEntry:  true,
			wantKnown: false,
			// SPEC §6: two explicit buttons rather than a guess.
			wantDirected: localui.DirectionDownload,
		},
		{
			name:         "no entry at all means upload",
			localChange:  entryAt,
			wantKnown:    true,
			wantDirected: localui.DirectionUpload,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			if !tt.localChange.IsZero() {
				f.svc.watcher.MarkLocal(textContent("local"), tt.localChange)
			}
			var latest *localui.EntryView
			if tt.hasEntry {
				latest = &localui.EntryView{ID: "e1", CreatedAt: entryAt}
			}

			got, known := f.svc.suggest(latest)
			if known != tt.wantKnown {
				t.Errorf("suggest() known = %v, want %v", known, tt.wantKnown)
			}
			if got != tt.wantDirected {
				t.Errorf("suggest() = %v, want %v", got, tt.wantDirected)
			}
		})
	}
}

// TestAutoSyncRefusesToGuess covers the case SPEC §6 singles out: with no
// reliable local timestamp the service must ask rather than pick a direction,
// because picking wrongly destroys whichever side it overwrites.
func TestAutoSyncRefusesToGuess(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.clip.set(textContent("something the user copied before the service started"))
	f.relay.push(tppclient.Item{ContentType: clipboard.TypeText, Body: []byte("from the phone"), CreatedAt: f.now()})

	_, err := f.svc.Sync(context.Background(), localui.DirectionAuto)
	var se *localui.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("Sync(auto) error = %v, want a StatusError", err)
	}
	if se.Code != http.StatusConflict {
		t.Errorf("Sync(auto) status = %d, want %d", se.Code, http.StatusConflict)
	}
	if f.relay.count() != 1 {
		t.Errorf("Sync(auto) wrote to the relay while refusing to choose a direction")
	}
	if got := string(f.clip.content.Body); got != "something the user copied before the service started" {
		t.Errorf("Sync(auto) overwrote the clipboard while refusing to choose a direction: %q", got)
	}
}

func TestUploadThenDownloadRoundTrip(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	f.clip.set(textContent("copied here"))

	res, err := f.svc.Sync(ctx, localui.DirectionUpload)
	if err != nil {
		t.Fatalf("Sync(upload) error = %v", err)
	}
	if !res.Changed || res.Entry == nil {
		t.Fatalf("Sync(upload) = %+v, want an entry", res)
	}

	// After an upload the service knows when the local clipboard was current,
	// so auto has a comparison to make and chooses to keep it.
	status := f.svc.Status(ctx)
	if !status.DirectionKnown || status.Suggested != localui.DirectionUpload {
		t.Errorf("Status() direction = %v (known %v), want upload", status.Suggested, status.DirectionKnown)
	}

	f.clip.set(textContent("replaced locally"))
	if _, err := f.svc.Sync(ctx, localui.DirectionDownload); err != nil {
		t.Fatalf("Sync(download) error = %v", err)
	}
	if got := string(f.clip.content.Body); got != "copied here" {
		t.Errorf("clipboard after download = %q, want the uploaded entry", got)
	}
}

func TestDownloadOfAStaleEntryIsSilent(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.clip.set(textContent("what the user has now"))
	f.relay.getErr = tppclient.ErrStaleEntry

	res, err := f.svc.Sync(context.Background(), localui.DirectionDownload)
	if err != nil {
		t.Fatalf("Sync(download) with a stale entry = %v, want no error", err)
	}
	if res.Changed {
		t.Errorf("Sync(download) = %+v, want no change", res)
	}
	if f.clip.writes != 0 {
		t.Errorf("a stale entry touched the clipboard %d times, want 0", f.clip.writes)
	}
}

// TestRevocationNeedsAConfirmedPlan is SPEC §3.3 step 2: nothing is revoked
// until the user has been shown the roster and said yes.
func TestRevocationNeedsAConfirmedPlan(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.svc.ConfirmRevoke(ctx, "not-a-plan"); err == nil {
		t.Fatal("ConfirmRevoke() with an unknown plan succeeded")
	}

	plan, err := f.svc.PrepareRevoke(ctx, "d2")
	if err != nil {
		t.Fatalf("PrepareRevoke() error = %v", err)
	}
	if plan.Target.Name != "phone" || len(plan.Remaining) != 1 || plan.Remaining[0].Name != "laptop" {
		t.Fatalf("PrepareRevoke() = %+v, want the named roster the dialog shows", plan)
	}
	if f.relay.rev.confirmed != 0 {
		t.Fatal("PrepareRevoke() carried out the revocation")
	}

	if _, err := f.svc.ConfirmRevoke(ctx, plan.ID); err != nil {
		t.Fatalf("ConfirmRevoke() error = %v", err)
	}
	if f.relay.rev.confirmed != 1 {
		t.Fatalf("the revocation ran %d times, want 1", f.relay.rev.confirmed)
	}

	// A plan is single-use: a second confirmation must not rekey again.
	if _, err := f.svc.ConfirmRevoke(ctx, plan.ID); err == nil {
		t.Error("ConfirmRevoke() succeeded twice for one plan")
	}
	if f.relay.rev.confirmed != 1 {
		t.Errorf("the revocation ran %d times, want 1", f.relay.rev.confirmed)
	}
}

func TestExpiredPlanCannotBeConfirmed(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()

	plan, err := f.svc.PrepareRevoke(ctx, "d2")
	if err != nil {
		t.Fatalf("PrepareRevoke() error = %v", err)
	}
	// The dialog was left open past the plan's lifetime.
	f.svc.now = func() time.Time { return time.Date(2026, 9, 9, 11, 0, 0, 0, time.UTC) }

	if _, err := f.svc.ConfirmRevoke(ctx, plan.ID); err == nil {
		t.Fatal("ConfirmRevoke() accepted a plan whose roster is out of date")
	}
	if f.relay.rev.confirmed != 0 {
		t.Errorf("the revocation ran %d times, want 0", f.relay.rev.confirmed)
	}
}

func TestSettingsPersistAndToggleTheWatcher(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()

	if f.svc.watcher.Enabled() {
		t.Fatal("auto-watch is on by default; SPEC §7.2 says it must be off")
	}

	view, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{AutoWatch: boolPtr(true)})
	if err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if !view.AutoWatch || !f.svc.watcher.Enabled() {
		t.Errorf("UpdateSettings() = %+v, want auto-watch on", view)
	}

	saved, err := config.Load(f.svc.configDir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !saved.AutoWatch {
		t.Error("the settings file did not record auto-watch")
	}

	// An unsupported login item is reported as unsupported, not as success.
	_, err = f.svc.UpdateSettings(ctx, localui.SettingsPatch{Autostart: boolPtr(true)})
	var se *localui.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusNotImplemented {
		t.Errorf("UpdateSettings(autostart) error = %v, want a 501", err)
	}

	// A port change is accepted and flagged as needing a restart: the service
	// binds once, at startup.
	view, err = f.svc.UpdateSettings(ctx, localui.SettingsPatch{Port: intPtr(50000)})
	if err != nil {
		t.Fatalf("UpdateSettings(port) error = %v", err)
	}
	if !view.RestartRequired || view.Port != 50000 {
		t.Errorf("UpdateSettings(port) = %+v, want port 50000 and a restart flag", view)
	}

	// A privileged port is refused rather than saved and failing at the next
	// launch.
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{Port: intPtr(80)}); err == nil {
		t.Error("UpdateSettings() accepted a privileged port")
	}
}

func TestOperationsRefuseWithoutAGroup(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.relay.inGroup = false
	ctx := context.Background()

	if _, err := f.svc.Sync(ctx, localui.DirectionUpload); err == nil {
		t.Error("Sync() succeeded without a group")
	}
	if _, err := f.svc.Devices(ctx); err == nil {
		t.Error("Devices() succeeded without a group")
	}
	if _, err := f.svc.History(ctx, localui.HistoryQuery{}); err == nil {
		t.Error("History() succeeded without a group")
	}
}

// TestRevokedDeviceStopsOperating covers SPEC §3.3 step 5: once the relay says
// this device is gone, the UI is told plainly rather than shown failures.
func TestRevokedDeviceStopsOperating(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.svc.ClientHandlers().OnRevoked()

	status := f.svc.Status(context.Background())
	if !status.Revoked {
		t.Error("Status() does not report the device as revoked")
	}
	_, err := f.svc.Sync(context.Background(), localui.DirectionUpload)
	var se *localui.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusForbidden {
		t.Errorf("Sync() after revocation = %v, want a 403", err)
	}
}

func TestEventsReachSubscribers(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	events, stop := f.svc.Subscribe()

	f.svc.ClientHandlers().OnEpoch(4)
	select {
	case ev := <-events:
		if ev.Kind != localui.EventEpoch || ev.Epoch != 4 {
			t.Errorf("event = %+v, want an epoch event at 4", ev)
		}
		if ev.At.IsZero() {
			t.Error("event carries no timestamp")
		}
	case <-time.After(time.Second):
		t.Fatal("no event arrived")
	}

	stop()
	if _, ok := <-events; ok {
		t.Error("the channel stayed open after the subscription ended")
	}
}

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

// TestAcceptingAnOfferNeedsTheDialog is the consent gate of
// docs/plans/joiner-emitted-pairing.md §5 at the service level: nothing is
// wrapped until a plan the dialog produced comes back, and each plan admits a
// device exactly once.
func TestAcceptingAnOfferNeedsTheDialog(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()

	// A plan id nobody prepared is refused, and reaches no acceptance at all.
	if _, err := f.svc.ConfirmAcceptOffer(ctx, "made-up"); err == nil {
		t.Fatal("confirming an unprepared acceptance succeeded, want it refused")
	}
	if f.relay.accept != nil {
		t.Fatal("a code the user never read was decoded")
	}

	plan, err := f.svc.PrepareAcceptOffer(ctx, "tpp-offer-code")
	if err != nil {
		t.Fatalf("PrepareAcceptOffer() error = %v", err)
	}
	if plan.DeviceName == "" || plan.Fingerprint == "" {
		t.Fatalf("the plan does not carry a dialog to show: %+v", plan)
	}
	// Preparing changes nothing: the whole point is that the user can still
	// decide against it after reading the name and the fingerprint.
	if got := f.relay.accept.confirmed.Load(); got != 0 {
		t.Fatalf("preparing admitted %d devices, want 0", got)
	}

	device, err := f.svc.ConfirmAcceptOffer(ctx, plan.ID)
	if err != nil {
		t.Fatalf("ConfirmAcceptOffer() error = %v", err)
	}
	if device.Name != "phone" {
		t.Errorf("admitted device = %+v, want the offered name", device)
	}
	if got := f.relay.accept.confirmed.Load(); got != 1 {
		t.Errorf("confirming admitted %d devices, want 1", got)
	}

	// The plan is spent. A double-submitted dialog admits one device, not two.
	if _, err := f.svc.ConfirmAcceptOffer(ctx, plan.ID); err == nil {
		t.Error("confirming the same plan twice succeeded, want it refused")
	}
	if got := f.relay.accept.confirmed.Load(); got != 1 {
		t.Errorf("after a repeat confirm, %d devices were admitted, want 1", got)
	}
}

// TestShowingASecondOfferWithdrawsTheFirst: the socket an offer holds open is
// the relay's only route to a device with no identity, so an offer that is no
// longer on screen must not stay live. Two live codes for one device would be
// two ways in, and only one of them is being shown to anybody.
func TestShowingASecondOfferWithdrawsTheFirst(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.svc.StartOffer(ctx, "https://relay.example"); err != nil {
		t.Fatalf("StartOffer() error = %v", err)
	}
	first := f.relay.offer

	if _, err := f.svc.StartOffer(ctx, "https://relay.example"); err != nil {
		t.Fatalf("second StartOffer() error = %v", err)
	}
	if f.relay.offer == first {
		t.Fatal("the second call returned the first offer")
	}
	if !first.closed.Load() {
		t.Error("the first offer is still live after a second was shown")
	}

	// And withdrawing gives up the one that is showing.
	if err := f.svc.CancelOffer(ctx); err != nil {
		t.Fatalf("CancelOffer() error = %v", err)
	}
	if !f.relay.offer.closed.Load() {
		t.Error("the offer is still live after being withdrawn")
	}
}

// TestShowingAnOfferNeedsARelayURL is the one real cost of this direction: a
// device with no group has no relay URL either, so the user supplies it.
func TestShowingAnOfferNeedsARelayURL(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	if _, err := f.svc.StartOffer(context.Background(), "   "); err == nil {
		t.Error("StartOffer with no relay URL succeeded, want it refused")
	}
}

// TestForgetLeavesTheGroupAndTidiesUpAfterIt covers the Settings panel's
// "leave the group" button at the service level.
//
// The client core is what deletes the keys, and it has its own test. What this
// package owes the user is the tidying either side of that: a pairing code
// still on screen is withdrawn, because the socket holding it open is a way
// into a device that is about to have no identity; a prepared revocation is
// dropped, because it describes a group this device has left; and the UI is
// told, because nothing else would tell it.
func TestForgetLeavesTheGroupAndTidiesUpAfterIt(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	events, stop := f.svc.Subscribe()
	defer stop()

	plan, err := f.svc.PrepareRevoke(ctx, "d2")
	if err != nil {
		t.Fatalf("PrepareRevoke() error = %v", err)
	}
	if _, err := f.svc.StartOffer(ctx, "https://relay.example"); err != nil {
		t.Fatalf("StartOffer() error = %v", err)
	}
	offer := f.relay.offer

	if err := f.svc.Forget(ctx); err != nil {
		t.Fatalf("Forget() error = %v", err)
	}

	if !f.relay.forgot {
		t.Error("Forget() did not reach the client core")
	}
	if !offer.closed.Load() {
		t.Error("the pairing code this device was showing is still live after leaving")
	}
	if _, err := f.svc.ConfirmRevoke(ctx, plan.ID); err == nil {
		t.Error("a revocation prepared before leaving is still confirmable")
	}

	status := f.svc.Status(ctx)
	switch {
	case status.InGroup:
		t.Error("Status() still reports a group after leaving")
	case status.Connected:
		t.Error("Status() still reports a connection after leaving")
	case status.LastSync != nil:
		t.Errorf("Status() still reports a last sync at %s", status.LastSync)
	}

	// The UI is pushed to, not left to notice: the panel it is showing is
	// about a group that no longer exists on this machine.
	select {
	case ev := <-events:
		if ev.Kind != localui.EventDisconnected {
			t.Errorf("Forget() published a %q event, want %q", ev.Kind, localui.EventDisconnected)
		}
	default:
		t.Error("Forget() published no event")
	}
}

// TestForgetBeforeTheRelayIsAttached is the service starting up: the UI is
// reachable before Attach, and a button press then must say so rather than
// panic on a nil relay.
func TestForgetBeforeTheRelayIsAttached(t *testing.T) {
	t.Parallel()

	svc, err := New(Options{
		Clipboard: newMemClipboard(),
		Autostart: noAutostart{},
		ConfigDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var se *localui.StatusError
	if err := svc.Forget(context.Background()); !errors.As(err, &se) || se.Code != http.StatusServiceUnavailable {
		t.Errorf("Forget() before Attach = %v, want a 503", err)
	}
}

// TestDownloadUpdatesTheClipboardTimestamp is the bug the status screen showed:
// after a download the clipboard holds something new, and "changed …" went on
// reporting whenever the watcher had last seen the user copy something — or
// nothing at all, on a fresh start.
func TestDownloadUpdatesTheClipboardTimestamp(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()

	// Another device wrote the group's latest entry; this one has seen no copy
	// of its own, so it has no local timestamp at all.
	if _, err := f.relay.PutEntry(ctx, tppclient.Item{
		ContentType: clipboard.TypeText,
		Body:        []byte("from the phone"),
		CreatedAt:   f.now(),
	}); err != nil {
		t.Fatalf("PutEntry() error = %v", err)
	}
	if before := f.svc.Status(ctx); before.Clipboard.ChangedAt != nil {
		t.Fatalf("Status().Clipboard.ChangedAt = %v before any change, want nil", before.Clipboard.ChangedAt)
	}

	if _, err := f.svc.Sync(ctx, localui.DirectionDownload); err != nil {
		t.Fatalf("Sync(download) error = %v", err)
	}

	after := f.svc.Status(ctx)
	if after.Clipboard.ChangedAt == nil {
		t.Fatal("Status().Clipboard.ChangedAt is nil after a download; the screen cannot say when this clipboard was set")
	}
	if got, want := *after.Clipboard.ChangedAt, after.Latest.CreatedAt; !got.Equal(want) {
		t.Errorf("Status().Clipboard.ChangedAt = %v, want the downloaded entry's time %v", got, want)
	}
	// And the direction is now a comparison rather than a shrug: the clipboard
	// holds the group's latest entry, which is something this device knows.
	if !after.DirectionKnown {
		t.Error("Status().DirectionKnown = false after a download, want true")
	}
}

// TestEntryPreviewReadsWithoutCopying is what makes the history list usable:
// an entry can be looked at without becoming what the user has copied.
func TestEntryPreviewReadsWithoutCopying(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	f.clip.set(textContent("what the user has now"))

	meta, err := f.relay.PutEntry(ctx, tppclient.Item{
		ContentType: clipboard.TypeText,
		Body:        []byte("an older note"),
		CreatedAt:   f.now(),
	})
	if err != nil {
		t.Fatalf("PutEntry() error = %v", err)
	}

	view, err := f.svc.EntryPreview(ctx, meta.ID)
	if err != nil {
		t.Fatalf("EntryPreview() error = %v", err)
	}
	if view.Kind != "text" || view.Text != "an older note" {
		t.Errorf("EntryPreview() = %+v, want the entry's text", view)
	}
	if view.Truncated {
		t.Error("EntryPreview() reports a short entry as truncated")
	}
	if f.clip.writes != 0 {
		t.Errorf("a preview wrote the clipboard %d times, want 0", f.clip.writes)
	}
	if got := string(f.clip.content.Body); got != "what the user has now" {
		t.Errorf("clipboard after a preview = %q, want it untouched", got)
	}
}

// TestEntryPreviewRefusesAStaleEntry: an entry from before a rekey cannot be
// read at all, so the history list must be told rather than shown an empty box.
func TestEntryPreviewRefusesAStaleEntry(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.relay.getErr = tppclient.ErrStaleEntry

	if _, err := f.svc.EntryPreview(context.Background(), "e1"); err == nil {
		t.Fatal("EntryPreview() of a stale entry = nil error, want a refusal")
	}
}

// TestPairingCodesCarryALink is what the desktop was missing: it built the
// codes but showed them bare, so a phone scanning a desktop's QR got a string
// its camera app could only offer to copy (SPEC §6a).
func TestPairingCodesCarryALink(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()

	inv, err := f.svc.StartPairing(ctx)
	if err != nil {
		t.Fatalf("StartPairing() error = %v", err)
	}
	if inv.Payload == "" {
		t.Fatal("StartPairing() returned no payload")
	}
	// The bare code stays in the response: anything that can already read one
	// has to keep working.
	want := tppclient.PairingLink(f.relay.State().ServerURL, inv.Payload)
	if inv.Link != want {
		t.Errorf("StartPairing() link = %q, want %q", inv.Link, want)
	}
	if !strings.Contains(inv.Link, "#") {
		t.Error("the link does not carry the code in a fragment, so the relay would receive it")
	}
	// And it unwraps back to exactly the code it wrapped, which is what makes
	// the link safe to hand to a decoder on the other side.
	if got := tppclient.StripCodeEnvelope(inv.Link); got != inv.Payload {
		t.Errorf("StripCodeEnvelope(link) = %q, want the payload %q", got, inv.Payload)
	}
}
