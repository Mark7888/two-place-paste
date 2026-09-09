// Package autostart manages the login item that starts the desktop service
// when the user signs in.
//
// It is off by default and stays off until the user turns it on (SPEC §7.2). A
// background agent that installs itself into a login session without being
// asked is the behaviour of something the user would rather uninstall.
//
// The implementations are a launchd agent on macOS and a registry Run key on
// Windows; every other platform gets a manager that reports the feature as
// unavailable rather than silently doing nothing.
package autostart

import (
	"errors"
	"fmt"
	"os"
)

// ErrUnsupported means this platform has no login-item mechanism in this
// build. A UI shows the toggle as unavailable; it never reports success.
var ErrUnsupported = errors.New("autostart: not supported on this platform")

// Label identifies the login item. It is the launchd label on macOS and the
// registry value name on Windows.
const Label = "com.twoplacepaste.desktop"

// Manager installs and removes the login item.
type Manager interface {
	// Enabled reports whether the login item exists.
	Enabled() (bool, error)

	// Enable installs the login item for the given executable.
	Enable(exePath string) error

	// Disable removes it. Removing one that is not there is not an error.
	Disable() error

	// Available reports whether this platform can manage a login item at all.
	Available() bool
}

// New returns this platform's manager.
func New() Manager { return newManager() }

// Executable returns the path to install as the login item.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", errors.Join(errors.New("autostart: locate this executable"), err)
	}
	return exe, nil
}

// Apply brings the login item into the requested state, which is what a
// settings toggle needs: it is idempotent, and it reports what the platform
// actually did rather than what was asked for.
func Apply(m Manager, want bool) (bool, error) {
	if !m.Available() {
		return false, ErrUnsupported
	}
	if !want {
		if err := m.Disable(); err != nil {
			return false, fmt.Errorf("autostart: remove the login item: %w", err)
		}
		return false, nil
	}
	exe, err := Executable()
	if err != nil {
		return false, err
	}
	if err := m.Enable(exe); err != nil {
		return false, fmt.Errorf("autostart: install the login item: %w", err)
	}
	on, err := m.Enabled()
	if err != nil {
		return false, fmt.Errorf("autostart: read back the login item: %w", err)
	}
	return on, nil
}
