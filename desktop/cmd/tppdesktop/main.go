// Command tppdesktop is the TwoPlacePaste desktop service: a tray icon, a
// localhost web UI, and a clipboard, wired to the client core in
// pkg/tppclient (SPEC §7.2).
//
// It holds no crypto and speaks no protocol of its own. What it decides is
// which way a sync goes and what the user is shown; everything else is the
// client core's.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/autostart"
	"github.com/Mark7888/two-place-paste/desktop/internal/buildinfo"
	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
	"github.com/Mark7888/two-place-paste/desktop/internal/config"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/desktop/internal/power"
	"github.com/Mark7888/two-place-paste/desktop/internal/service"
	"github.com/Mark7888/two-place-paste/desktop/internal/tray"
	"github.com/Mark7888/two-place-paste/desktop/internal/update"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/keystore"
)

// Environment overrides. Deliberately few: the settings the user changes live
// in the settings file and the UI, and these are for a developer running from
// a checkout.
const (
	envConfigDir = "TPP_DESKTOP_CONFIG_DIR"
	envUIDev     = "TPP_DESKTOP_UI_DEV"
	envLogLevel  = "TPP_DESKTOP_LOG"

	// envUpdateAllowLocal lets a build from a checkout install updates, to
	// test the updater itself. Off, a local build never replaces itself.
	envUpdateAllowLocal = "TPP_DESKTOP_UPDATE_ALLOW_LOCAL"
)

// idleAfter is how long after the last sync an automatic update may restart
// the service, when the screen is not locked.
const idleAfter = 10 * time.Minute

// main exists only to turn start's exit code into a process exit.
//
// The split is not ceremony: os.Exit does not run deferred functions, so an
// os.Exit inside the body would skip closing the log — on precisely the path
// where the service failed to start and the log is the only account of why.
func main() {
	os.Exit(start())
}

// start runs the service and reports the process's exit code, with every
// deferred cleanup guaranteed to have run by the time it returns.
func start() int {
	logger, closeLog := newLogger(os.Getenv(envConfigDir))
	defer closeLog()
	slog.SetDefault(logger)
	logger.Info("starting", "version", buildinfo.Version, "channel", buildinfo.Channel, "commit", buildinfo.Commit)

	if err := run(logger); err != nil {
		logger.Error("the service stopped", "error", err)
		return 1
	}
	return 0
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// An update relaunches the new build while the old one still holds the
	// localhost port (internal/update). Binding before it has gone would land
	// this process in the "port in use" failure, so it waits first.
	if pid := update.ParseWaitPID(os.Args[1:]); pid > 0 {
		logger.Info("waiting for the build this one replaces to exit", "pid", pid)
		if !update.WaitForExit(pid, update.WaitPIDTimeout) {
			logger.Warn("the previous build is still running; starting anyway", "pid", pid)
		}
	}

	configDir := os.Getenv(envConfigDir)
	settings, err := config.Load(configDir)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		//nolint:wrapcheck // config.Load already names the file and the operation.
		// A settings file that exists and cannot be parsed is not something to
		// paper over with defaults: the user wrote it, most likely to override
		// the port, and silently ignoring it would leave them puzzled.
		return err
	}
	if settings.DeviceName == "" {
		settings.DeviceName = defaultDeviceName()
	}
	settingsPath, err := config.Path(configDir)
	if err != nil {
		return fmt.Errorf("locate the settings file: %w", err)
	}

	store, err := keystore.Open(keystore.Options{Service: keystore.DefaultService})
	if err != nil {
		return fmt.Errorf("open the key store: %w", err)
	}
	logger.Info("key store opened", "backend", string(store.Backend()))

	svc, err := service.New(service.Options{
		Clipboard:       clipboard.New(),
		Autostart:       autostart.New(),
		Settings:        settings,
		ConfigDir:       configDir,
		ListenPort:      settings.ListenPort(),
		KeystoreBackend: string(store.Backend()),
		WatchPaused:     power.ScreenLocked,
		Logger:          logger,
	})
	if err != nil {
		return fmt.Errorf("build the service: %w", err)
	}

	client, err := tppclient.New(tppclient.Options{
		DeviceName: settings.DeviceName,
		Keystore:   store,
		Logger:     logger,
		Handlers:   svc.ClientHandlers(),
	})
	if err != nil {
		return fmt.Errorf("build the client core: %w", err)
	}
	defer func() { _ = client.Close() }()
	svc.Attach(service.NewRelay(client))

	srv, listenErr := localui.Listen(ctx, localui.Options{
		API:      svc,
		Port:     settings.ListenPort(),
		DevProxy: os.Getenv(envUIDev),
		Logger:   logger,
	})
	if listenErr != nil {
		// SPEC §7.2: never fail silently. The tray shows the failure and
		// offers the settings file, which is where the port is overridden.
		logger.Error("the localhost UI could not start",
			"error", listenErr, "port", settings.ListenPort(), "settings", settingsPath)
		tray.Run(ctx, tray.Options{
			Err:          fmt.Errorf("port %d: %w", settings.ListenPort(), listenErr),
			SettingsPath: settingsPath,
			Quit:         stop,
			Logger:       logger,
		})
		return fmt.Errorf("start the localhost UI: %w", listenErr)
	}
	svc.SetListenPort(srv.Port())

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		if err := srv.Serve(runCtx); err != nil {
			logger.Error("the localhost UI stopped", "error", err)
			cancel()
		}
	}()
	go svc.Run(runCtx)

	updater, err := update.New(update.Options{
		ConfigDir:  configDir,
		Quit:       cancel,
		AllowLocal: os.Getenv(envUpdateAllowLocal) == "1",
		Logger:     logger,
		Idle: func() bool {
			return power.ScreenLocked() || time.Since(svc.LastSync()) > idleAfter
		},
	})
	if err != nil {
		return fmt.Errorf("build the updater: %w", err)
	}
	updater.SetPreferences(settings)
	go updater.Run(runCtx)

	go power.WatchWake(runCtx, power.DefaultWakeCheck, func(slept time.Duration) {
		svc.Resumed(slept)
		updater.Woke()
	})

	if client.InGroup() {
		if err := client.Connect(runCtx); err != nil {
			// A relay that is down is not a reason to refuse to start: the
			// client reconnects on its own, and the UI must be reachable
			// meanwhile to show why nothing is syncing.
			logger.Warn("could not connect to the relay yet", "error", err)
		}
	} else {
		// Nothing works until this device is in a group, and the only place to
		// fix that is the UI — so this is the one launch that opens it. A
		// paired device starts quietly into the tray instead: it is a
		// background service, and a service that throws a browser tab at the
		// user on every login is one they turn off.
		logger.Info("this device is not in a group yet; opening the UI to pair or create one")
		if err := openUI(runCtx, srv.URL(), logger); err != nil {
			logger.Warn("could not open the browser", "error", err)
		}
	}

	// The tray owns the main goroutine: the macOS menu bar is only addressable
	// from the process's first thread.
	tray.Run(runCtx, tray.Options{
		URL:          srv.URL(),
		SettingsPath: settingsPath,
		SyncNow: func() {
			res, err := svc.Sync(runCtx, localui.DirectionAuto)
			if err != nil {
				logger.Warn("sync from the tray failed", "error", err)
				return
			}
			logger.Info("sync from the tray", "direction", string(res.Direction), "changed", res.Changed)
		},
		Quit:   cancel,
		Logger: logger,
	})
	cancel()
	return nil
}

// openUI opens the app in the browser. The URL carries the launch token and
// is therefore never logged.
func openUI(ctx context.Context, url string, logger *slog.Logger) error {
	logger.Info("opening the app in the default browser")
	if err := tray.OpenURL(ctx, url); err != nil {
		return fmt.Errorf("open the app: %w", err)
	}
	return nil
}

func defaultDeviceName() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "desktop"
	}
	return host
}

// maxLogBytes caps the log file. It is truncated at start-up rather than
// rotated: this is a desktop service whose log exists to explain the launch
// that just failed, and a rotation scheme is a feature to maintain.
const maxLogBytes = 2 << 20

// logFileName is the log beside the settings file.
const logFileName = "tppdesktop.log"

// newLogger builds the logger and returns a function that closes it.
//
// It writes to a file as well as to stderr, and the file is the part that
// matters: the Windows build is linked with -H=windowsgui so that it does not
// drag a console window onto the user's desktop, and a process with no console
// has no usable stderr. Without a file, every diagnostic this service produces
// on the platform where it most needs one would go nowhere.
func newLogger(configDir string) (*slog.Logger, func()) {
	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(os.Getenv(envLogLevel))); err != nil {
		level = slog.LevelInfo
	}

	file, err := openLogFile(configDir)
	if err != nil || file == nil {
		// No log file is a degraded state, not a fatal one: the service still
		// runs and the tray still reports what it can.
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})), func() {}
	}
	handler := slog.NewTextHandler(tee{file}, &slog.HandlerOptions{Level: level})
	return slog.New(handler), func() { _ = file.Close() }
}

func openLogFile(configDir string) (*os.File, error) {
	settingsPath, err := config.Path(configDir)
	if err != nil {
		return nil, fmt.Errorf("locate the log directory: %w", err)
	}
	dir := filepath.Dir(settingsPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the log directory: %w", err)
	}
	path := filepath.Join(dir, logFileName)

	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > maxLogBytes {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open the log file: %w", err)
	}
	return file, nil
}

// tee writes to the log file and to stderr, and never fails.
//
// stderr is deliberately best-effort: under -H=windowsgui it is an invalid
// handle and every write to it errors. An io.MultiWriter would stop at that
// first error and the file — the whole point of this — would stay empty.
type tee struct{ file *os.File }

func (t tee) Write(p []byte) (int, error) {
	if _, err := t.file.Write(p); err != nil {
		return 0, fmt.Errorf("write the log file: %w", err)
	}
	_, _ = os.Stderr.Write(p)
	return len(p), nil
}
