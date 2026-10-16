package tray

import (
	"context"
	"image/color"
	"sync/atomic"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows/registry"
)

// themeCheckInterval is how often the taskbar theme is re-read. Windows sends
// the change as a window message to a window systray owns, so polling one
// registry value is the simplest way to follow it; the read is cheap.
const themeCheckInterval = 2 * time.Second

// inkLight is the glyph's ink on a dark taskbar.
var inkLight = color.NRGBA{R: 255, G: 255, B: 255, A: 255}

// darkTaskbar is the taskbar theme last read.
var darkTaskbar atomic.Bool

// setIcon sets the glyph for a status in the ink that contrasts with the
// taskbar. Windows has no template images: a black glyph stays black on a
// dark taskbar, so this chooses the ink itself.
//
// It uses the .ico container: the Windows notification area does not read a
// bare PNG.
func setIcon(s Status) {
	ink := inkDark
	if darkTaskbar.Load() {
		ink = inkLight
	}
	if ico := icons(ink, s).ico; len(ico) > 0 {
		systray.SetIcon(ico)
	}
}

// watchTheme reads the taskbar theme now, and calls repaint whenever the user
// switches it, until ctx is done.
func watchTheme(ctx context.Context, repaint func()) {
	darkTaskbar.Store(taskbarIsDark())
	go func() {
		ticker := time.NewTicker(themeCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if now := taskbarIsDark(); now != darkTaskbar.Load() {
					darkTaskbar.Store(now)
					repaint()
				}
			}
		}
	}()
}

// taskbarIsDark reports whether the taskbar uses the dark theme.
// SystemUsesLightTheme is the taskbar's setting, separate from the apps'
// AppsUseLightTheme. Windows 10 before 1903 has no light taskbar and no such
// value, which is also the dark answer.
func taskbarIsDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("SystemUsesLightTheme")
	if err != nil {
		return true
	}
	return v == 0
}
