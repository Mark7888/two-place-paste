package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/config"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
)

const testAsset = "TwoPlacePaste-Windows-x64.exe"

// release is one channel's published files, as a fake GitHub serves them.
type release struct {
	manifest []byte
	sig      []byte
	files    map[string][]byte
}

// fakeGitHub serves releases/latest/download/… and
// releases/download/channel-beta/… from memory.
type fakeGitHub struct {
	mu       sync.Mutex
	channels map[string]*release
	requests []string
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.URL.Path)
	var rel *release
	var name string
	switch {
	case strings.HasPrefix(r.URL.Path, "/releases/latest/download/"):
		rel, name = f.channels["stable"], strings.TrimPrefix(r.URL.Path, "/releases/latest/download/")
	case strings.HasPrefix(r.URL.Path, "/releases/download/channel-beta/"):
		rel, name = f.channels["beta"], strings.TrimPrefix(r.URL.Path, "/releases/download/channel-beta/")
	}
	if rel == nil {
		http.NotFound(w, r)
		return
	}
	switch name {
	case "manifest-desktop.json":
		_, _ = w.Write(rel.manifest)
	case "manifest-desktop.json.sig":
		_, _ = w.Write(rel.sig)
	default:
		b, ok := rel.files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}
}

// publish signs a manifest for payload and puts it on channel.
func (f *fakeGitHub) publish(t *testing.T, key ed25519.PrivateKey, channel string, stamp int64, payload []byte) {
	t.Helper()
	sum := sha256.Sum256(payload)
	m := Manifest{
		Schema: 1, Platform: "desktop", Repository: Repository,
		Version: "1.0." + channel, Numeric: "1.0.0", Stamp: stamp, Channel: channel,
		Commit: strings.Repeat("a", 40),
		Files:  []File{{Name: testAsset, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(payload))}},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.channels == nil {
		f.channels = map[string]*release{}
	}
	f.channels[channel] = &release{
		manifest: b,
		sig:      ed25519.Sign(key, b),
		files:    map[string][]byte{testAsset: payload},
	}
}

// fakeApplier records what the manager asks of it.
type fakeApplier struct {
	mu         sync.Mutex
	notReady   error
	installed  [][]byte
	relaunched []int
	previous   bool
	rolledBack bool
}

func (a *fakeApplier) Ready() error { return a.notReady }

func (a *fakeApplier) Install(_ context.Context, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.installed = append(a.installed, b)
	a.previous = true
	return nil
}

func (a *fakeApplier) HasPrevious() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.previous
}

func (a *fakeApplier) Rollback(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rolledBack = true
	a.previous = false
	return nil
}

func (a *fakeApplier) Relaunch(pid int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.relaunched = append(a.relaunched, pid)
	return nil
}

type harness struct {
	gh      *fakeGitHub
	key     ed25519.PrivateKey
	applier *fakeApplier
	quit    chan struct{}
	m       *Manager
}

func newHarness(t *testing.T, current localui.BuildView) *harness {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{gh: &fakeGitHub{}, key: priv, applier: &fakeApplier{}, quit: make(chan struct{}, 1)}
	srv := httptest.NewServer(h.gh)
	t.Cleanup(srv.Close)
	h.m, err = New(Options{
		ConfigDir:   t.TempDir(),
		CacheDir:    t.TempDir(),
		Applier:     h.applier,
		Quit:        func() { h.quit <- struct{}{} },
		Current:     current,
		Asset:       testAsset,
		ReleaseBase: srv.URL + "/releases",
		Key:         pub,
		Client:      srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func stableBuild(stamp int64) localui.BuildView {
	return localui.BuildView{Version: "0.9.0", Channel: "stable", Stamp: stamp}
}

func TestCheckFindsANewerStableBuild(t *testing.T) {
	h := newHarness(t, stableBuild(100))
	h.gh.publish(t, h.key, "stable", 200, []byte("new build"))
	h.m.SetPreferences(config.Settings{})

	v, err := h.m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.State != localui.UpdateStateAvailable || v.Available == nil {
		t.Fatalf("state %q, available %+v; want an available update", v.State, v.Available)
	}
	if v.Available.Stamp != 200 || v.Available.Older || !v.Available.Signed {
		t.Fatalf("available = %+v", v.Available)
	}
	if v.LastChecked == nil {
		t.Fatal("LastChecked was not set")
	}
}

func TestCheckIsUpToDateOnTheSameBuild(t *testing.T) {
	h := newHarness(t, stableBuild(200))
	h.gh.publish(t, h.key, "stable", 200, []byte("same"))
	v, err := h.m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.State != localui.UpdateStateUpToDate || v.Available != nil {
		t.Fatalf("state %q, available %+v; want up to date", v.State, v.Available)
	}
}

func TestCheckOnAnEmptyChannelIsNotAnError(t *testing.T) {
	h := newHarness(t, stableBuild(100))
	h.m.SetPreferences(config.Settings{UpdateChannel: "beta"})
	v, err := h.m.Check(context.Background())
	if err != nil {
		t.Fatalf("Check on an empty channel: %v", err)
	}
	if v.State != localui.UpdateStateUpToDate || !strings.Contains(v.Message, "nothing has been published") {
		t.Fatalf("state %q, message %q", v.State, v.Message)
	}
}

func TestSwitchingChannelOffersAnOlderBuildOnlyOnRequest(t *testing.T) {
	h := newHarness(t, localui.BuildView{Version: "1.1.0-dev", Channel: "beta", Stamp: 300})
	h.gh.publish(t, h.key, "stable", 200, []byte("older stable"))
	h.m.SetPreferences(config.Settings{UpdateChannel: "stable"})

	v, err := h.m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.Available == nil || !v.Available.Older {
		t.Fatalf("available = %+v; want an older build after the switch", v.Available)
	}
	if _, err := h.m.Install(context.Background(), localui.InstallRequest{}); !isStatus(err, http.StatusConflict) {
		t.Fatalf("Install without AllowOlder: %v; want a 409", err)
	}
	if _, err := h.m.Install(context.Background(), localui.InstallRequest{AllowOlder: true}); err != nil {
		t.Fatalf("Install with AllowOlder: %v", err)
	}
	<-h.quit
}

func TestAnOlderBuildOnTheSameChannelIsNotOffered(t *testing.T) {
	h := newHarness(t, localui.BuildView{Version: "1.0.1-dev", Channel: "beta", Stamp: 300})
	h.gh.publish(t, h.key, "beta", 250, []byte("re-run"))
	h.m.SetPreferences(config.Settings{UpdateChannel: "beta"})
	v, err := h.m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.Available != nil {
		t.Fatalf("available = %+v; want nothing", v.Available)
	}
}

func TestABadSignatureIsRefused(t *testing.T) {
	h := newHarness(t, stableBuild(100))
	_, otherKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h.gh.publish(t, otherKey, "stable", 200, []byte("forged"))
	v, err := h.m.Check(context.Background())
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Check: %v; want ErrBadSignature", err)
	}
	if v.Available != nil || v.State != localui.UpdateStateError {
		t.Fatalf("state %q, available %+v", v.State, v.Available)
	}
}

func TestAManifestFromAnotherChannelIsRefused(t *testing.T) {
	h := newHarness(t, stableBuild(100))
	h.gh.publish(t, h.key, "beta", 200, []byte("beta build"))
	h.gh.mu.Lock()
	h.gh.channels["stable"] = h.gh.channels["beta"]
	h.gh.mu.Unlock()
	if _, err := h.m.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "beta manifest") {
		t.Fatalf("Check: %v; want a channel mismatch", err)
	}
}

func TestInstallVerifiesSwapsAndRestarts(t *testing.T) {
	h := newHarness(t, stableBuild(100))
	h.gh.publish(t, h.key, "stable", 200, []byte("new build"))
	if _, err := h.m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	v, err := h.m.Install(context.Background(), localui.InstallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if v.State != localui.UpdateStateRestarting {
		t.Fatalf("state %q; want restarting", v.State)
	}
	select {
	case <-h.quit:
	case <-time.After(5 * time.Second):
		t.Fatal("the process was never told to quit")
	}
	if len(h.applier.installed) != 1 || string(h.applier.installed[0]) != "new build" {
		t.Fatalf("installed %q", h.applier.installed)
	}
	if len(h.applier.relaunched) != 1 || h.applier.relaunched[0] != os.Getpid() {
		t.Fatalf("relaunched %v; want this pid", h.applier.relaunched)
	}
	if v.Previous == nil || v.Previous.Stamp != 100 {
		t.Fatalf("previous = %+v; want the replaced build", v.Previous)
	}
	// The download is gone once installed.
	entries, _ := os.ReadDir(h.m.opts.CacheDir)
	if len(entries) != 0 {
		t.Fatalf("the cache still holds %d files", len(entries))
	}
}

func TestATamperedDownloadIsNeverInstalled(t *testing.T) {
	h := newHarness(t, stableBuild(100))
	h.gh.publish(t, h.key, "stable", 200, []byte("new build"))
	if _, err := h.m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.gh.mu.Lock()
	h.gh.channels["stable"].files[testAsset] = []byte("new bui1d")
	h.gh.mu.Unlock()

	v, err := h.m.Install(context.Background(), localui.InstallRequest{})
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("Install: %v; want ErrChecksum", err)
	}
	if len(h.applier.installed) != 0 {
		t.Fatal("a tampered download was installed")
	}
	if v.State != localui.UpdateStateError {
		t.Fatalf("state %q; want error", v.State)
	}
	entries, _ := os.ReadDir(h.m.opts.CacheDir)
	if len(entries) != 0 {
		t.Fatalf("the rejected download was left behind (%d files)", len(entries))
	}
}

func TestALocalBuildNeverInstalls(t *testing.T) {
	h := newHarness(t, localui.BuildView{Version: "0.0.0-local", Channel: "local"})
	h.gh.publish(t, h.key, "stable", 200, []byte("new build"))
	v, err := h.m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.CanInstall || !strings.Contains(v.CannotInstall, "development build") {
		t.Fatalf("CanInstall %v, CannotInstall %q", v.CanInstall, v.CannotInstall)
	}
	if _, err := h.m.Install(context.Background(), localui.InstallRequest{}); !isStatus(err, http.StatusConflict) {
		t.Fatalf("Install: %v; want a 409", err)
	}
}

func TestInstallNeedsAnApplierThatIsReady(t *testing.T) {
	h := newHarness(t, stableBuild(100))
	h.applier.notReady = errors.New("the app folder is read-only")
	h.gh.publish(t, h.key, "stable", 200, []byte("new build"))
	v, err := h.m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.CanInstall || v.CannotInstall != "the app folder is read-only" {
		t.Fatalf("CanInstall %v, CannotInstall %q", v.CanInstall, v.CannotInstall)
	}
	if _, err := h.m.Install(context.Background(), localui.InstallRequest{}); err == nil {
		t.Fatal("Install succeeded with an applier that is not ready")
	}
}

func TestStatePersistsAcrossRestarts(t *testing.T) {
	h := newHarness(t, stableBuild(100))
	h.gh.publish(t, h.key, "stable", 200, []byte("new build"))
	if _, err := h.m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.Install(context.Background(), localui.InstallRequest{}); err != nil {
		t.Fatal(err)
	}
	<-h.quit

	again, err := New(h.m.opts)
	if err != nil {
		t.Fatal(err)
	}
	v := again.Status(context.Background())
	if v.LastChecked == nil || v.Previous == nil || v.Previous.Stamp != 100 {
		t.Fatalf("after a restart: last checked %v, previous %+v", v.LastChecked, v.Previous)
	}
}

func TestRollbackRestoresAndRestarts(t *testing.T) {
	h := newHarness(t, stableBuild(200))
	if _, err := h.m.Rollback(context.Background()); !isStatus(err, http.StatusConflict) {
		t.Fatalf("Rollback with nothing to restore: %v; want a 409", err)
	}
	h.applier.previous = true
	if _, err := h.m.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-h.quit
	if !h.applier.rolledBack || len(h.applier.relaunched) != 1 {
		t.Fatalf("rolled back %v, relaunched %v", h.applier.rolledBack, h.applier.relaunched)
	}
}

func TestChangingChannelClearsTheOffer(t *testing.T) {
	h := newHarness(t, stableBuild(100))
	h.gh.publish(t, h.key, "stable", 200, []byte("new build"))
	if _, err := h.m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	changes := 0
	h.m.opts.OnChange = func() { changes++ }
	h.m.SetPreferences(config.Settings{UpdateChannel: "beta"})
	if v := h.m.Status(context.Background()); v.Available != nil || v.Channel != "beta" {
		t.Fatalf("after switching: channel %q, available %+v", v.Channel, v.Available)
	}
	if changes == 0 {
		t.Fatal("switching channel did not notify the UI")
	}
}

func isStatus(err error, code int) bool {
	var se *localui.StatusError
	return errors.As(err, &se) && se.Code == code
}
