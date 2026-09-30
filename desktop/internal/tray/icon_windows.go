package tray

import (
	"context"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows/registry"
)

// themeCheckInterval is how often the taskbar theme is re-read. Windows sends
// the change as a window message to a window systray owns, so polling one
// registry value is the simplest way to follow it; the read is cheap.
const themeCheckInterval = 2 * time.Second

// showIcon sets the icon for the taskbar's current theme and keeps it in step
// when the user switches. Windows has no template images: a black glyph stays
// black on a dark taskbar, so this chooses the ink itself.
//
// It uses the .ico container: the Windows notification area does not read a
// bare PNG.
func showIcon(ctx context.Context) {
	dark := taskbarIsDark()
	applyIcon(dark)

	go func() {
		ticker := time.NewTicker(themeCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if now := taskbarIsDark(); now != dark {
					dark = now
					applyIcon(dark)
				}
			}
		}
	}()
}

func applyIcon(dark bool) {
	ink := inkDark
	if dark {
		ink = inkLight
	}
	if ico := icons(ink).ico; len(ico) > 0 {
		systray.SetIcon(ico)
	}
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
