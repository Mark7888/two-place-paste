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

// TestLaunchdManagerLegacyLabel covers a login item an older build installed
// under the previous app identifier: it counts as enabled, Enable replaces it
// rather than adding a second agent, and Disable removes it.
func TestLaunchdManagerLegacyLabel(t *testing.T) {
	t.Parallel()

	exe := "/Applications/TwoPlacePaste.app/Contents/MacOS/tppdesktop"
	tests := []struct {
		name string
		act  func(m *launchdManager) error
		want []string // plist files left in the directory
	}{
		{"enable replaces it", func(m *launchdManager) error { return m.Enable(exe) }, []string{Label + ".plist"}},
		{"disable removes it", func(m *launchdManager) error { return m.Disable() }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			legacy := filepath.Join(dir, LegacyLabel+".plist")
			if err := os.WriteFile(legacy, []byte(plist(exe)), 0o600); err != nil {
				t.Fatalf("writing the legacy plist: %v", err)
			}
			m := &launchdManager{dir: dir}
			if on, err := m.Enabled(); err != nil || !on {
				t.Fatalf("Enabled() with a legacy plist = %v, %v; want true, nil", on, err)
			}
			if err := tt.act(m); err != nil {
				t.Fatalf("error = %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("reading the directory: %v", err)
			}
			var got []string
			for _, e := range entries {
				got = append(got, e.Name())
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("files = %v, want %v", got, tt.want)
			}
		})
	}
}
