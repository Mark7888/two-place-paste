//go:build !windows && !darwin

package autostart

// unsupportedManager is what a Linux build gets: SPEC §7.4 defers the Linux
// desktop, and the choice there between a systemd user unit and a .desktop
// autostart entry is part of that deferred work.
type unsupportedManager struct{}

func newManager() Manager { return unsupportedManager{} }

func (unsupportedManager) Available() bool        { return false }
func (unsupportedManager) Enabled() (bool, error) { return false, ErrUnsupported }
func (unsupportedManager) Enable(string) error    { return ErrUnsupported }
func (unsupportedManager) Disable() error         { return ErrUnsupported }
