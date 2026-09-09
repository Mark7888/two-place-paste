package localui

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// isAddrInUse reports whether a listen failed because the port was taken.
//
// Windows needs its own answer: Winsock reports a taken port as
// WSAEADDRINUSE (10048), which is a different value from the POSIX-shaped
// syscall.EADDRINUSE that the syscall package also defines and that a listen
// here never returns. Checking only the latter made this the one platform
// where the fixed port of SPEC §7.2 could be occupied and the tray would show
// a generic failure instead of the message that names the port override.
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, windows.WSAEADDRINUSE)
}
