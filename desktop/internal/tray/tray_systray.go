//go:build windows || darwin

package tray

import (
	"context"
	"fmt"

	"fyne.io/systray"
)

// title is the tooltip and, where a platform shows one, the menu title.
const title = "TwoPlacePaste"

// reveal opens the settings file's location, which is what the bind-failure
// menu offers: SPEC §7.2 says the port is overridden in the config file, so
// the tray's job is to put the user in front of it.
func reveal(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("tray: no settings file to open")
	}
	return launch(ctx, path)
}

// run builds the menu and hands control to the platform's event loop.
func run(ctx context.Context, opts Options) {
	onReady := func() {
		pngIcon, icoIcon := icons()
		setIcon(pngIcon, icoIcon)
		systray.SetTitle("")
		systray.SetTooltip(title)

		if opts.Err != nil {
			runErrorMenu(ctx, opts)
			return
		}
		runMenu(ctx, opts)
	}
	systray.Run(onReady, func() {})
}

func runMenu(ctx context.Context, opts Options) {
	syncItem := systray.AddMenuItem("Sync now", "Sync the clipboard with the group")
	openItem := systray.AddMenuItem("Open TwoPlacePaste", "Open the app in your browser")
	systray.AddSeparator()
	quitItem := systray.AddMenuItem("Quit", "Stop the service")

	go func() {
		for {
			select {
			case <-ctx.Done():
				systray.Quit()
				return
			case <-syncItem.ClickedCh:
				if opts.SyncNow != nil {
					go opts.SyncNow()
				}
			case <-openItem.ClickedCh:
				if err := OpenURL(ctx, opts.URL); err != nil {
					// The URL carries the launch token, so the failure is
					// logged without it.
					opts.Logger.Error("could not open the browser", "error", err)
				}
			case <-quitItem.ClickedCh:
				if opts.Quit != nil {
					opts.Quit()
				}
				systray.Quit()
				return
			}
		}
	}()
}

// runErrorMenu is the SPEC §7.2 bind failure: visible, and with the one action
// that fixes it.
func runErrorMenu(ctx context.Context, opts Options) {
	systray.SetTooltip(title + " — not running")
	problem := systray.AddMenuItem("TwoPlacePaste could not start", opts.Err.Error())
	problem.Disable()
	reason := systray.AddMenuItem(opts.Err.Error(), opts.Err.Error())
	reason.Disable()
	settingsItem := systray.AddMenuItem("Open the settings file…", opts.SettingsPath)
	systray.AddSeparator()
	quitItem := systray.AddMenuItem("Quit", "Stop the service")

	go func() {
		for {
			select {
			case <-ctx.Done():
				systray.Quit()
				return
			case <-settingsItem.ClickedCh:
				if err := reveal(ctx, opts.SettingsPath); err != nil {
					opts.Logger.Error("could not open the settings file", "error", err)
				}
			case <-quitItem.ClickedCh:
				if opts.Quit != nil {
					opts.Quit()
				}
				systray.Quit()
				return
			}
		}
	}()
}
