//go:build !windows

package tray

import "syscall"

// hideWindow has nothing to hide anywhere but Windows.
func hideWindow() *syscall.SysProcAttr { return nil }
