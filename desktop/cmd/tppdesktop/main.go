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
	"strings"
	"syscall"

	"github.com/Mark7888/two-place-paste/desktop/internal/autostart"
	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
	"github.com/Mark7888/two-place-paste/desktop/internal/config"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/desktop/internal/service"
	"github.com/Mark7888/two-place-paste/desktop/internal/tray"
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
)

func main() {
	logger := newLogger()
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("the service stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	if client.InGroup() {
		if err := client.Connect(runCtx); err != nil {
			// A relay that is down is not a reason to refuse to start: the
			// client reconnects on its own, and the UI must be reachable
			// meanwhile to show why nothing is syncing.
			logger.Warn("could not connect to the relay yet", "error", err)
		}
	} else {
		logger.Info("this device is not in a group yet; create one or pair from the UI")
	}

	if err := openUI(runCtx, srv.URL(), logger); err != nil {
		logger.Warn("could not open the browser", "error", err)
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

// openUI opens the app on first launch. The URL carries the launch token and
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

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(os.Getenv(envLogLevel))); err != nil {
		level = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
