package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchdManager(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	m := &launchdManager{dir: dir}

	if on, err := m.Enabled(); err != nil || on {
		t.Fatalf("Enabled() on a fresh install = %v, %v; want false, nil", on, err)
	}

	exe := "/Applications/TwoPlacePaste.app/Contents/MacOS/tppdesktop"
	if err := m.Enable(exe); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if on, err := m.Enabled(); err != nil || !on {
		t.Fatalf("Enabled() after Enable() = %v, %v; want true, nil", on, err)
	}

	b, err := os.ReadFile(filepath.Join(dir, Label+".plist"))
	if err != nil {
		t.Fatalf("reading the plist: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, exe) {
		t.Errorf("plist does not name the executable:\n%s", got)
	}
	if !strings.Contains(got, "<key>RunAtLoad</key>") {
		t.Errorf("plist has no RunAtLoad:\n%s", got)
	}
	if strings.Contains(got, "KeepAlive") {
		t.Errorf("plist has KeepAlive: quitting from the tray must stay quit:\n%s", got)
	}

	if err := m.Disable(); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if on, err := m.Enabled(); err != nil || on {
		t.Fatalf("Enabled() after Disable() = %v, %v; want false, nil", on, err)
	}
	// Removing a login item that is not there is not an error.
	if err := m.Disable(); err != nil {
		t.Errorf("Disable() on an absent login item = %v, want nil", err)
	}
}

func TestPlistEscapesThePath(t *testing.T) {
	t.Parallel()

	got := plist(`/Users/a&b/<tpp>/tppdesktop`)
	if strings.Contains(got, "<tpp>") || !strings.Contains(got, "&amp;") {
		t.Errorf("plist() did not escape the path:\n%s", got)
	}
}
