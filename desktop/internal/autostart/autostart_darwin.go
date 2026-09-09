package autostart

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// launchdManager writes a per-user LaunchAgent.
//
// A user agent, never a daemon: the service holds one person's clipboard and
// runs as that person. ~/Library/LaunchAgents needs no privileges to write and
// is read at login, which is exactly the lifetime wanted.
type launchdManager struct {
	dir string // overridden in tests
}

func newManager() Manager { return &launchdManager{} }

func (m *launchdManager) Available() bool { return true }

func (m *launchdManager) plistPath() (string, error) {
	dir := m.dir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("autostart: locate the home directory: %w", err)
		}
		dir = filepath.Join(home, "Library", "LaunchAgents")
	}
	return filepath.Join(dir, Label+".plist"), nil
}

func (m *launchdManager) Enabled() (bool, error) {
	path, err := m.plistPath()
	if err != nil {
		return false, err
	}
	switch _, err := os.Stat(path); {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("autostart: stat %s: %w", path, err)
	}
}

func (m *launchdManager) Enable(exePath string) error {
	path, err := m.plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("autostart: create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(plist(exePath)), 0o600); err != nil {
		return fmt.Errorf("autostart: write %s: %w", path, err)
	}
	return nil
}

func (m *launchdManager) Disable() error {
	path, err := m.plistPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("autostart: remove %s: %w", path, err)
	}
	return nil
}

// plist renders the LaunchAgent.
//
// KeepAlive is deliberately absent: a service the user quits from the tray
// must stay quit until the next login. RunAtLoad plus no KeepAlive is exactly
// "start me when I sign in", and nothing more.
func plist(exePath string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + escapeXML(exePath) + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`
}

func escapeXML(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		// EscapeText fails only when the writer does, and a Builder cannot.
		return ""
	}
	return b.String()
}
