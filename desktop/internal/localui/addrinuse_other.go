//go:build !windows

package localui

import (
	"errors"
	"syscall"
)

// isAddrInUse reports whether a listen failed because the port was taken.
func isAddrInUse(err error) bool { return errors.Is(err, syscall.EADDRINUSE) }
