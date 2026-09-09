package tray

import "fyne.io/systray"

// setIcon uses a template image on macOS, so the glyph inverts with the menu
// bar rather than staying black on a dark bar.
func setIcon(pngIcon, _ []byte) {
	if len(pngIcon) == 0 {
		return
	}
	systray.SetTemplateIcon(pngIcon, pngIcon)
}
