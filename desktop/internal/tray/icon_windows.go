package tray

import "fyne.io/systray"

// setIcon uses the .ico container: the Windows notification area does not read
// a bare PNG.
func setIcon(_, icoIcon []byte) {
	if len(icoIcon) == 0 {
		return
	}
	systray.SetIcon(icoIcon)
}
