package update

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/buildinfo"
	"github.com/Mark7888/two-place-paste/desktop/internal/config"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
)

// stateFile keeps what the manager must remember across restarts: when it
// last checked, and which build the last update replaced. It lives next to
// the settings file but is the manager's alone, so the two never race.
const stateFile = "update-state.json"

// httpTimeout bounds one request; a download gets downloadTimeout.
const (
	httpTimeout     = 30 * time.Second
	downloadTimeout = 15 * time.Minute
)

// quitDelay is how long the old process keeps running after a relaunch, so
// the HTTP response that asked for the install reaches the page first. A
// variable only so tests need not wait for it.
var quitDelay = time.Second

// Applier replaces the running build. Each platform has its own
// (apply_windows.go, apply_darwin.go); everywhere else nothing can be applied.
type Applier interface {
	// Ready returns nil if this installation can replace itself, or an error
	// saying why not, in words for a person.
	Ready() error
	// Install swaps the verified download at path in for the running build.
	Install(ctx context.Context, path string) error
	// HasPrevious reports whether Rollback has something to restore.
	HasPrevious() bool
	// Rollback restores the build the last Install replaced.
	Rollback(ctx context.Context) error
	// Relaunch starts the installed build, telling it to wait for pid to exit
	// before it binds the localhost port.
	Relaunch(pid int) error
}

// Options configures a Manager.
type Options struct {
	// ConfigDir holds update-state.json. Empty means the per-user default.
	ConfigDir string

	// CacheDir holds downloads. Empty means <user cache>/TwoPlacePaste/update.
	CacheDir string

	// Applier swaps a download in. Empty means this platform's own.
	Applier Applier

	// Quit stops this process after a relaunch. Required to install.
	Quit func()

	// OnChange is called whenever the state the UI shows changes.
	OnChange func()

	// Current is the running build. Empty means buildinfo's.
	Current localui.BuildView

	// AllowLocal lets a build from a checkout install updates, which it
	// otherwise refuses: a developer's binary is not swapped out from under
	// them (TPP_DESKTOP_UPDATE_ALLOW_LOCAL=1).
	AllowLocal bool

	// Tests replace the endpoints, the key, the HTTP client, the clock, and
	// the file this platform installs.
	Asset       string
	ReleaseBase string
	APIBase     string
	Key         ed25519.PublicKey
	Client      *http.Client
	Now         func() time.Time
	Logger      *slog.Logger
}

// Manager is the updater: the localhost UI's update API, and the thing the
// schedule asks to check and install.
type Manager struct {
	opts    Options
	release releaseSource
	asset   string

	mu          sync.Mutex
	prefs       config.Settings
	state       string
	message     string
	avail       *candidate
	lastChecked time.Time
	previous    *localui.BuildView
	busy        bool
}

// candidate is an offered build and how to fetch it.
type candidate struct {
	view localui.UpdateCandidate
	// fetch downloads the build into dir and verifies it, returning the path
	// of the file to install.
	fetch func(ctx context.Context, dir string) (string, error)
}

// persisted is the shape of update-state.json.
type persisted struct {
	LastChecked time.Time          `json:"last_checked"`
	Previous    *localui.BuildView `json:"previous,omitempty"`
}

// New builds a Manager. It does no I/O beyond reading its state file.
func New(opts Options) (*Manager, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: httpTimeout}
	}
	if opts.ReleaseBase == "" {
		opts.ReleaseBase = DefaultReleaseBase
	}
	if opts.Key == nil {
		key, err := SigningKey()
		if err != nil {
			return nil, err
		}
		opts.Key = key
	}
	if opts.Current == (localui.BuildView{}) {
		opts.Current = localui.BuildView{
			Version: buildinfo.Version,
			Channel: buildinfo.Channel,
			Commit:  buildinfo.Commit,
			Stamp:   buildinfo.StampValue(),
		}
	}
	if opts.CacheDir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("update: locate the user cache directory: %w", err)
		}
		opts.CacheDir = filepath.Join(base, config.DefaultDir, "update")
	}
	if opts.Applier == nil {
		opts.Applier = platformApplier()
	}

	m := &Manager{
		opts:    opts,
		release: releaseSource{client: opts.Client, base: opts.ReleaseBase, key: opts.Key},
		state:   localui.UpdateStateIdle,
	}
	m.asset = opts.Asset
	if m.asset == "" {
		m.asset, _ = AssetName()
	}
	m.load()
	return m, nil
}

// SetPreferences gives the manager the user's update settings. The service
// owns the settings file and calls this after loading it and after every save.
func (m *Manager) SetPreferences(s config.Settings) {
	m.mu.Lock()
	changed := s.Channel() != m.prefs.Channel() || s.NightlyCommit != m.prefs.NightlyCommit
	m.prefs = s
	if changed && !m.busy {
		// What was on offer belonged to the old channel.
		m.avail = nil
		m.state = localui.UpdateStateIdle
		m.message = ""
	}
	m.mu.Unlock()
	if changed {
		m.changed()
	}
}

// Status implements the UI's update API.
func (m *Manager) Status(context.Context) localui.UpdateView {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.viewLocked()
}

func (m *Manager) viewLocked() localui.UpdateView {
	v := localui.UpdateView{
		Current:       m.opts.Current,
		Channel:       m.prefs.Channel(),
		AutoUpdate:    m.prefs.AutoUpdate,
		NightlyCommit: m.prefs.NightlyCommit,
		State:         m.state,
		Message:       m.message,
		Previous:      m.previous,
	}
	if m.avail != nil {
		c := m.avail.view
		v.Available = &c
	}
	if !m.lastChecked.IsZero() {
		t := m.lastChecked
		v.LastChecked = &t
	}
	if err := m.readyLocked(); err != nil {
		v.CannotInstall = err.Error()
	} else {
		v.CanInstall = true
	}
	return v
}

// readyLocked says why this installation cannot replace itself, if it cannot.
func (m *Manager) readyLocked() error {
	if m.opts.Current.Channel == buildinfo.LocalChannel && !m.opts.AllowLocal {
		return errors.New("this is a development build, which never replaces itself")
	}
	if m.asset == "" {
		return errors.New("no builds are published for this platform")
	}
	return m.opts.Applier.Ready()
}

// Check asks the channel for its newest build.
func (m *Manager) Check(ctx context.Context) (localui.UpdateView, error) {
	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return localui.UpdateView{}, localui.Errorf(http.StatusConflict, nil, "an update is already in progress")
	}
	m.busy = true
	m.state = localui.UpdateStateChecking
	m.message = ""
	prefs := m.prefs
	m.mu.Unlock()
	m.changed()

	cand, err := m.find(ctx, prefs)

	m.mu.Lock()
	m.busy = false
	m.lastChecked = m.opts.Now()
	switch {
	case errors.Is(err, ErrNoRelease):
		m.avail = nil
		m.state = localui.UpdateStateUpToDate
		m.message = fmt.Sprintf("nothing has been published on the %s channel yet", prefs.Channel())
		err = nil
	case err != nil:
		m.state = localui.UpdateStateError
		m.message = err.Error()
	case cand == nil:
		m.avail = nil
		m.state = localui.UpdateStateUpToDate
	default:
		m.avail = cand
		m.state = localui.UpdateStateAvailable
		if cand.view.Building {
			m.state = localui.UpdateStateBuilding
		}
	}
	m.saveLocked()
	v := m.viewLocked()
	m.mu.Unlock()
	m.changed()

	if err != nil {
		m.opts.Logger.Warn("update check failed", "channel", prefs.Channel(), "error", err)
		return v, localui.Errorf(http.StatusBadGateway, err, "the update check failed: %v", err)
	}
	if cand != nil {
		m.opts.Logger.Info("update available", "channel", prefs.Channel(),
			"version", cand.view.Version, "older", cand.view.Older)
	}
	return v, nil
}

// find returns the build the channel offers, or nil when that is the running
// one.
func (m *Manager) find(ctx context.Context, prefs config.Settings) (*candidate, error) {
	if m.asset == "" {
		return nil, errors.New("no builds are published for this platform")
	}
	switch ch := prefs.Channel(); ch {
	case config.ChannelStable, config.ChannelBeta:
		return m.findRelease(ctx, ch)
	case config.ChannelNightly:
		return m.findNightly(ctx, prefs.NightlyCommit)
	default:
		return nil, fmt.Errorf("unknown update channel %q", ch)
	}
}

func (m *Manager) findRelease(ctx context.Context, channel string) (*candidate, error) {
	man, dir, err := m.release.latest(ctx, channel)
	if err != nil {
		return nil, err
	}
	file, ok := man.File(m.asset)
	if !ok {
		return nil, fmt.Errorf("the %s build %s has no %s", channel, man.Version, m.asset)
	}
	cur := m.opts.Current
	older := man.Stamp < cur.Stamp
	switch {
	case man.Stamp == cur.Stamp:
		return nil, nil
	case older && channel == cur.Channel:
		// The channel's newest is behind this build: a re-run of an older
		// commit. Nothing to do.
		return nil, nil
	}
	url := dir + "/" + file.Name
	return &candidate{
		view: localui.UpdateCandidate{
			BuildView: localui.BuildView{
				Version: man.Version, Channel: man.Channel, Commit: man.Commit, Stamp: man.Stamp,
			},
			Older:  older,
			Signed: true,
		},
		fetch: func(ctx context.Context, dir string) (string, error) {
			return download(ctx, m.downloadClient(), url, dir, file)
		},
	}, nil
}

// findNightly is replaced in a later step; until then Nightly offers nothing.
func (m *Manager) findNightly(context.Context, string) (*candidate, error) {
	return nil, errors.New("the Nightly channel is not available in this build")
}

// downloadClient is the HTTP client with a timeout long enough for a build.
func (m *Manager) downloadClient() *http.Client {
	c := *m.opts.Client
	c.Timeout = downloadTimeout
	return &c
}

// Install downloads, verifies and installs the offered build, then restarts
// into it.
func (m *Manager) Install(ctx context.Context, req localui.InstallRequest) (localui.UpdateView, error) {
	m.mu.Lock()
	cand, err := m.beginInstallLocked(req)
	if err != nil {
		m.mu.Unlock()
		return localui.UpdateView{}, err
	}
	m.state = localui.UpdateStateDownloading
	m.message = ""
	m.mu.Unlock()
	m.changed()

	if err := m.install(ctx, cand); err != nil {
		m.mu.Lock()
		m.busy = false
		m.state = localui.UpdateStateError
		m.message = err.Error()
		v := m.viewLocked()
		m.mu.Unlock()
		m.changed()
		m.opts.Logger.Error("update failed", "version", cand.view.Version, "error", err)
		return v, localui.Errorf(http.StatusInternalServerError, err, "the update could not be installed: %v", err)
	}

	m.mu.Lock()
	v := m.viewLocked()
	m.mu.Unlock()
	return v, nil
}

func (m *Manager) beginInstallLocked(req localui.InstallRequest) (*candidate, error) {
	switch {
	case m.busy:
		return nil, localui.Errorf(http.StatusConflict, nil, "an update is already in progress")
	case m.avail == nil:
		return nil, localui.Errorf(http.StatusConflict, nil, "there is no update to install; check for one first")
	case m.avail.view.Building:
		return nil, localui.Errorf(http.StatusConflict, nil, "that commit's build has not finished yet")
	case m.avail.view.Older && !req.AllowOlder:
		return nil, localui.Errorf(http.StatusConflict, nil,
			"%s is older than the running build; confirm to install it anyway", m.avail.view.Version)
	case m.avail.view.NeedsConfirmation && !req.Confirm:
		return nil, localui.Errorf(http.StatusConflict, nil,
			"this build is unsigned; review where it came from and confirm to install it")
	case m.opts.Quit == nil:
		return nil, localui.Errorf(http.StatusNotImplemented, nil, "this process cannot restart itself")
	}
	if err := m.readyLocked(); err != nil {
		return nil, localui.Errorf(http.StatusConflict, err, "%v", err)
	}
	m.busy = true
	return m.avail, nil
}

func (m *Manager) install(ctx context.Context, cand *candidate) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), downloadTimeout)
	defer cancel()

	path, err := cand.fetch(ctx, m.opts.CacheDir)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(path) }()

	m.setState(localui.UpdateStateInstalling)
	if err := m.opts.Applier.Install(ctx, path); err != nil {
		return err
	}
	m.opts.Logger.Info("update installed", "from", m.opts.Current.Version, "to", cand.view.Version)

	m.mu.Lock()
	prev := m.opts.Current
	m.previous = &prev
	m.saveLocked()
	m.mu.Unlock()

	return m.restart()
}

// Rollback restores the build the last update replaced, and restarts into it.
func (m *Manager) Rollback(ctx context.Context) (localui.UpdateView, error) {
	m.mu.Lock()
	switch {
	case m.busy:
		m.mu.Unlock()
		return localui.UpdateView{}, localui.Errorf(http.StatusConflict, nil, "an update is already in progress")
	case !m.opts.Applier.HasPrevious():
		m.mu.Unlock()
		return localui.UpdateView{}, localui.Errorf(http.StatusConflict, nil, "there is no previous build to go back to")
	case m.opts.Quit == nil:
		m.mu.Unlock()
		return localui.UpdateView{}, localui.Errorf(http.StatusNotImplemented, nil, "this process cannot restart itself")
	}
	m.busy = true
	m.state = localui.UpdateStateInstalling
	m.mu.Unlock()
	m.changed()

	err := m.opts.Applier.Rollback(ctx)
	if err == nil {
		m.mu.Lock()
		m.previous = nil
		m.saveLocked()
		m.mu.Unlock()
		m.opts.Logger.Info("rolled back to the previous build")
		err = m.restart()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.busy = false
		m.state = localui.UpdateStateError
		m.message = err.Error()
		return m.viewLocked(), localui.Errorf(http.StatusInternalServerError, err, "could not roll back: %v", err)
	}
	return m.viewLocked(), nil
}

// restart starts the installed build and stops this one shortly after, so
// the response that asked for it still reaches the page.
func (m *Manager) restart() error {
	if err := m.opts.Applier.Relaunch(os.Getpid()); err != nil {
		return fmt.Errorf("the new build is installed but could not be started; quit and reopen TwoPlacePaste to finish: %w", err)
	}
	m.setState(localui.UpdateStateRestarting)
	go func() {
		time.Sleep(quitDelay)
		m.opts.Quit()
	}()
	return nil
}

func (m *Manager) setState(state string) {
	m.mu.Lock()
	m.state = state
	m.mu.Unlock()
	m.changed()
}

func (m *Manager) changed() {
	if m.opts.OnChange != nil {
		m.opts.OnChange()
	}
}

func (m *Manager) statePath() (string, error) {
	settings, err := config.Path(m.opts.ConfigDir)
	if err != nil {
		return "", fmt.Errorf("update: locate the state file: %w", err)
	}
	return filepath.Join(filepath.Dir(settings), stateFile), nil
}

// load reads update-state.json. A missing or unreadable file is a fresh start.
func (m *Manager) load() {
	path, err := m.statePath()
	if err != nil {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		m.opts.Logger.Warn("ignoring an unreadable update state file", "path", path, "error", err)
		return
	}
	m.lastChecked = p.LastChecked
	if p.Previous != nil && m.opts.Applier.HasPrevious() {
		m.previous = p.Previous
	}
}

func (m *Manager) saveLocked() {
	path, err := m.statePath()
	if err != nil {
		m.opts.Logger.Warn("could not save the update state", "error", err)
		return
	}
	b, err := json.MarshalIndent(persisted{LastChecked: m.lastChecked, Previous: m.previous}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		err = os.WriteFile(path, append(b, '\n'), 0o600)
	}
	if err != nil {
		m.opts.Logger.Warn("could not save the update state", "path", path, "error", err)
	}
}
