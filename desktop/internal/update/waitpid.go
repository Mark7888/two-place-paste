package update

import (
	"strconv"
	"strings"
	"time"
)

// waitPIDFlag is how a relaunched build learns which process it replaces.
// The old process still holds the localhost port; the new one must not try
// to bind it until the old one is gone, or it would start in the tray's
// "port in use" failure state.
const waitPIDFlag = "--wait-pid"

// WaitPIDTimeout bounds the wait. The old process shuts down within about a
// second of relaunching; one that is still alive after this is stuck, and the
// new one goes ahead rather than wait forever.
const WaitPIDTimeout = 15 * time.Second

// ParseWaitPID returns the PID given with --wait-pid N or --wait-pid=N, or 0.
func ParseWaitPID(args []string) int {
	for i, a := range args {
		var v string
		switch {
		case a == waitPIDFlag && i+1 < len(args):
			v = args[i+1]
		case strings.HasPrefix(a, waitPIDFlag+"="):
			v = strings.TrimPrefix(a, waitPIDFlag+"=")
		default:
			continue
		}
		if pid, err := strconv.Atoi(v); err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}

// WaitForExit waits until process pid has exited, or timeout has passed. It
// reports whether the process is gone.
func WaitForExit(pid int, timeout time.Duration) bool {
	return waitForExit(pid, timeout)
}
