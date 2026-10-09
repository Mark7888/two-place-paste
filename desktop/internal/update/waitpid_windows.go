package update

import (
	"time"

	"golang.org/x/sys/windows"
)

func waitForExit(pid int, timeout time.Duration) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// No such process (or none this account may see): it is gone.
		return true
	}
	defer func() { _ = windows.CloseHandle(h) }()
	ev, err := windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
	return err == nil && ev == windows.WAIT_OBJECT_0
}
