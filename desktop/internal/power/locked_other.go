//go:build !darwin || !cgo

package power

// screenLocked has no implementation here: the Windows service does not need
// one, since its clipboard check is already a single call, and the Linux
// desktop is deferred (SPEC §7.4).
func screenLocked() bool { return false }
