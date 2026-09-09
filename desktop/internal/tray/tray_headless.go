//go:build !windows && !darwin

package tray

import (
	"context"
	"fmt"
	"os"
)

// run is the tray on a platform that has none in this build (SPEC §7.4 defers
// the Linux desktop, where there is no universal tray standard).
//
// It is not a stub that does nothing: the service is running and its UI is
// reachable, so the one thing a tray would have given the user — the URL with
// its launch token — is printed, and the process then behaves exactly as it
// does elsewhere.
func run(ctx context.Context, opts Options) {
	if opts.Err != nil {
		opts.Logger.Error("the localhost UI could not start",
			"error", opts.Err, "settings", opts.SettingsPath)
	} else {
		printURL(opts.URL)
		opts.Logger.Info("no tray on this platform; open the URL printed above")
	}
	<-ctx.Done()
	if opts.Quit != nil {
		opts.Quit()
	}
}

// printURL is this build's substitute for a tray menu item: the URL carries
// the launch token, so it goes to the terminal the user started the service
// in and to no log.
func printURL(url string) {
	fmt.Fprintf(os.Stdout, "TwoPlacePaste is running. Open:\n\n  %s\n\n", url)
}
