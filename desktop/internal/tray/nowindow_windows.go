package tray

import "syscall"

// hideWindow keeps a helper process from flashing a console.
//
// The service is built with -H=windowsgui and has no console of its own, so a
// child started without this gets one allocated for it: `rundll32` opening a
// URL, or `explorer` opening the settings folder, would each blink a black
// window onto the user's screen for the moment they live.
func hideWindow() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// createNoWindow is CREATE_NO_WINDOW. It is written out rather than taken from
// x/sys/windows so this package keeps its dependency-free build.
const createNoWindow = 0x08000000
