package tray

import (
	"context"

	"fyne.io/systray"
)

// showIcon uses a template image on macOS, so the glyph inverts with the menu
// bar rather than staying black on a dark bar.
func showIcon(context.Context) {
	set := icons(inkDark)
	if len(set.png) == 0 {
		return
	}
	systray.SetTemplateIcon(set.png, set.png)
}
