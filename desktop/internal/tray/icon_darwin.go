package tray

import (
	"context"

	"fyne.io/systray"
)

// setIcon uses a template image on macOS, so the glyph inverts with the menu
// bar rather than staying black on a dark bar.
func setIcon(s Status) {
	set := icons(inkDark, s)
	if len(set.png) == 0 {
		return
	}
	systray.SetTemplateIcon(set.png, set.png)
}

// watchTheme has nothing to do on macOS: the template image follows the menu
// bar by itself.
func watchTheme(context.Context, func()) {}
