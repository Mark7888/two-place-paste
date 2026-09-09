// Package tray is the desktop service's only native UI: an icon in the menu
// bar or the notification area, with the three items SPEC §7.2 asks for — sync
// now, open the UI, quit — and one more that only appears when it must.
//
// That extra item is the bind failure. The port is fixed by SPEC §7.2 and a
// fixed port can be taken; a service that failed to bind and exited quietly
// would look to the user like a service that is running. When the localhost
// server cannot start, the tray says so and offers the settings file where the
// port is overridden.
package tray

import (
	"context"
	"log/slog"
)

// Options configures the tray.
type Options struct {
	// URL is the address the "Open TwoPlacePaste" item opens. It carries the
	// per-launch token, so it is never logged and never written to a file.
	URL string

	// SyncNow runs a manual sync. It must not block: it is called on the
	// tray's own goroutine.
	SyncNow func()

	// Quit stops the service. The tray calls it before returning.
	Quit func()

	// SettingsPath is the settings file the error item opens.
	SettingsPath string

	// Err, when set, is a startup failure to display instead of the ordinary
	// menu — a taken port, most often. The tray still runs: an error the user
	// cannot see is an error they cannot fix.
	Err error

	// Logger receives tray-level logs.
	Logger *slog.Logger
}

// Run shows the tray and blocks until the user quits or ctx is done.
//
// It must be called from the main goroutine: the macOS menu bar is only
// addressable from the process's first thread, and the systray library
// enforces it.
func Run(ctx context.Context, opts Options) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	run(ctx, opts)
}
