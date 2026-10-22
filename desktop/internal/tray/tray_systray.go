//go:build windows || darwin

package tray

import (
	"context"
	"fmt"

	"fyne.io/systray"
)

// reveal opens the settings file's location, which is what the bind-failure
// menu offers: SPEC §7.2 says the port is overridden in the config file, so
// the tray's job is to put the user in front of it.
func reveal(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("tray: no settings file to open")
	}
	return launch(ctx, path)
}

// The words of the direction prompt, shared by the notification and the menu.
const (
	promptTitle   = "Which way should this sync go?"
	promptBody    = "TwoPlacePaste has not seen this clipboard change, so it cannot tell which side is newer."
	uploadLabel   = "Upload this clipboard"
	downloadLabel = "Download the latest"
)

// repaint draws the current status again, for a platform whose glyph depends
// on something other than the status — the Windows taskbar theme.
func (t *Tray) repaint() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.paint != nil {
		t.paint(t.shown)
	}
}

// notifyFailed is a notification the platform accepted and then did not
// show: on macOS, most often, because the user said no to notifications.
func (t *Tray) notifyFailed(err error) {
	t.logger.Warn("a notification was not shown; the tray menu offers the choice", "error", err)
}

// run builds the menu and hands control to the platform's event loop.
func run(ctx context.Context, t *Tray, opts Options) {
	onReady := func() {
		systray.SetTitle("")
		watchTheme(ctx, t.repaint)
		if opts.Err != nil {
			runErrorMenu(ctx, t, opts)
			return
		}
		if err := setupNotifications(ctx, t.chooseAction, t.notifyFailed); err != nil {
			t.logger.Warn("notifications are unavailable; the tray menu will ask instead", "error", err)
		}
		runMenu(ctx, t, opts)
	}
	systray.Run(onReady, func() {})
}

// paintStatus is the icon, the tooltip and the menu's status line for one
// status. The function it returns runs with t.mu held, like every paint.
func (t *Tray) paintStatus(statusItem *systray.MenuItem) func(Status) {
	return func(s Status) {
		setIcon(s)
		systray.SetTooltip(t.tooltipLocked(s))
		if statusItem != nil {
			statusItem.SetTitle(s.label())
		}
	}
}

func runMenu(ctx context.Context, t *Tray, opts Options) {
	statusItem := systray.AddMenuItem("", "")
	statusItem.Disable()
	systray.AddSeparator()
	syncItem := systray.AddMenuItem("Sync now", "Sync the clipboard with the group")
	// The direction items stay hidden until a sync has had to ask.
	uploadItem := systray.AddMenuItem(uploadLabel, "Send this machine's clipboard to the group")
	downloadItem := systray.AddMenuItem(downloadLabel, "Put the group's latest entry on this clipboard")
	uploadItem.Hide()
	downloadItem.Hide()
	// Hidden until an upload from the tray has had to ask about a
	// protected entry.
	pushItem := systray.AddMenuItem(pushMenuLabel, "Upload the protected clipboard to the group anyway")
	pushItem.Hide()
	openItem := systray.AddMenuItem("Open TwoPlacePaste", "Open the app in your browser")
	updateItem := systray.AddMenuItem("Check for updates…", "Check the update channel for a newer build")
	if opts.CheckUpdates == nil {
		updateItem.Hide()
	}
	systray.AddSeparator()
	quitItem := systray.AddMenuItem("Quit", "Stop the service")

	t.setChoiceItems(func(show bool) {
		if show {
			uploadItem.Show()
			downloadItem.Show()
			return
		}
		uploadItem.Hide()
		downloadItem.Hide()
	})
	t.setPushItem(func(show bool) {
		if show {
			pushItem.Show()
			return
		}
		pushItem.Hide()
	})
	t.attach(t.paintStatus(statusItem))

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
			case <-uploadItem.ClickedCh:
				t.choose(Upload)
			case <-downloadItem.ClickedCh:
				t.choose(Download)
			case <-pushItem.ClickedCh:
				t.pushAnyway()
			case <-updateItem.ClickedCh:
				if opts.CheckUpdates != nil {
					go opts.CheckUpdates()
				}
			case <-openItem.ClickedCh:
				if err := OpenURL(ctx, opts.URL); err != nil {
					// The URL carries the launch token, so the failure is
					// logged without it.
					t.logger.Error("could not open the browser", "error", err)
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
func runErrorMenu(ctx context.Context, t *Tray, opts Options) {
	t.attach(t.paintStatus(nil))
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
					t.logger.Error("could not open the settings file", "error", err)
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
