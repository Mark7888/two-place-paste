package tray

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

// openTimeout bounds the helper that launches the browser. The helper returns
// immediately in the normal case; a hang must not take the tray with it.
const openTimeout = 15 * time.Second

// OpenURL opens the app in the user's default browser. It is exported because
// the service opens the UI once at startup, before the tray exists.
func OpenURL(ctx context.Context, url string) error { return launch(ctx, url) }

func launch(ctx context.Context, target string) error {
	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "/usr/bin/open", target)
	case "windows":
		// rundll32 rather than "cmd /c start": start would parse the target as
		// a shell argument, and a URL with a query string is exactly where
		// that goes wrong.
		cmd = exec.CommandContext(ctx, "rundll32.exe", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", target)
	}
	// On Windows the service has no console of its own, so a child process
	// would be given one — a black window blinking on screen every time the UI
	// is opened. Everywhere else this is nil.
	cmd.SysProcAttr = hideWindow()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tray: open %s: %w", kindOf(target), err)
	}
	return nil
}

// kindOf keeps the launch token out of the error. A failure names what could
// not be opened, never the URL that carries the credential.
func kindOf(target string) string {
	if len(target) > 4 && target[:4] == "http" {
		return "the app in a browser"
	}
	return target
}
