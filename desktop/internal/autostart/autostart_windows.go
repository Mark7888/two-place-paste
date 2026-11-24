package autostart

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// runKey is the per-user Run key. HKEY_CURRENT_USER, never
// HKEY_LOCAL_MACHINE: the machine-wide key needs administrator rights and
// would start this user's clipboard agent for every account on the computer.
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

type registryManager struct{}

func newManager() Manager { return registryManager{} }

func (registryManager) Available() bool { return true }

func (registryManager) Enabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("autostart: open HKCU\\%s: %w", runKey, err)
	}
	defer func() { _ = k.Close() }()

	for _, label := range []string{Label, LegacyLabel} {
		switch _, _, err := k.GetStringValue(label); {
		case err == nil:
			return true, nil
		case errors.Is(err, registry.ErrNotExist):
		default:
			return false, fmt.Errorf("autostart: read HKCU\\%s\\%s: %w", runKey, label, err)
		}
	}
	return false, nil
}

func (registryManager) Enable(exePath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("autostart: open HKCU\\%s: %w", runKey, err)
	}
	defer func() { _ = k.Close() }()

	// Quoted: a path under "Program Files" is otherwise read as a command and
	// a first argument.
	if err := k.SetStringValue(Label, `"`+exePath+`"`); err != nil {
		return fmt.Errorf("autostart: write HKCU\\%s\\%s: %w", runKey, Label, err)
	}
	// One login item, not two: an older build's value would start the
	// service a second time at sign-in.
	if err := k.DeleteValue(LegacyLabel); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("autostart: delete HKCU\\%s\\%s: %w", runKey, LegacyLabel, err)
	}
	return nil
}

func (registryManager) Disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("autostart: open HKCU\\%s: %w", runKey, err)
	}
	defer func() { _ = k.Close() }()

	var errs []error
	for _, label := range []string{Label, LegacyLabel} {
		if err := k.DeleteValue(label); err != nil && !errors.Is(err, registry.ErrNotExist) {
			errs = append(errs, fmt.Errorf("autostart: delete HKCU\\%s\\%s: %w", runKey, label, err))
		}
	}
	return errors.Join(errs...)
}
