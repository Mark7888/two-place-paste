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

	switch _, _, err := k.GetStringValue(Label); {
	case err == nil:
		return true, nil
	case errors.Is(err, registry.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("autostart: read HKCU\\%s\\%s: %w", runKey, Label, err)
	}
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

	if err := k.DeleteValue(Label); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("autostart: delete HKCU\\%s\\%s: %w", runKey, Label, err)
	}
	return nil
}
