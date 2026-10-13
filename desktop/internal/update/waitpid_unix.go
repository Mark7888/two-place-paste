//go:build !windows

package update

import (
	"errors"
	"syscall"
	"time"
)

func waitForExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		// Signal 0 checks for the process without touching it: ESRCH means
		// it is gone. Any other answer (EPERM included) means it exists.
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
