// Package config holds the desktop service's non-secret settings: the port it
// binds, whether it watches the clipboard, and whether it starts at login.
//
// Nothing secret is stored here. The device private key, the group key and the
// group identifiers live in the keystore (/spec/crypto.md §10); this file is
// an ordinary user-readable JSON file and must stay that way, because SPEC
// §7.2 requires the port to be overridable by hand after a bind failure — a
// user cannot edit what they cannot read.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// DefaultPort is the fixed localhost port of SPEC §7.2: dynamic/private range,
// low collision risk. It is overridable precisely because a fixed port can
// still be taken.
const DefaultPort = 47821

// DefaultDir names the per-user directory the settings file lives in, under
// os.UserConfigDir.
const DefaultDir = "TwoPlacePaste"

// FileName is the settings file inside DefaultDir.
const FileName = "desktop.json"

// Settings is the whole of the service's configuration.
//
// Both toggles default to false and stay false until the user says otherwise
// (SPEC §7.2): a clipboard watcher and a login item are things a user opts
// into, never things an installer decides for them.
type Settings struct {
	// Port overrides DefaultPort. Zero means the default.
	Port int `json:"port"`

	// AutoWatch enables clipboard polling. Off by default.
	AutoWatch bool `json:"auto_watch"`

	// Autostart records the user's intent for the login item. The truth lives
	// in launchd or the registry; this is what the UI shows and what a
	// reinstall restores.
	Autostart bool `json:"autostart"`

	// DeviceName is what this device is called in another device's revocation
	// dialog (SPEC §3.3 step 2). Not a secret.
	DeviceName string `json:"device_name"`
}

// ErrNotFound reports that no settings file exists yet, which is the state of
// a fresh install rather than a failure.
var ErrNotFound = errors.New("config: no settings file")

// Path returns the settings file's location. An empty dir resolves the
// per-user default.
func Path(dir string) (string, error) {
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("config: locate the user configuration directory: %w", err)
		}
		dir = filepath.Join(base, DefaultDir)
	}
	return filepath.Join(dir, FileName), nil
}

// Load reads the settings file. A missing file is ErrNotFound; the caller
// starts from Settings{} rather than refusing to run.
func Load(dir string) (Settings, error) {
	path, err := Path(dir)
	if err != nil {
		return Settings{}, err
	}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Settings{}, ErrNotFound
	case err != nil:
		return Settings{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	var s Settings
	if err := json.Unmarshal(b, &s); err != nil {
		return Settings{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return Settings{}, fmt.Errorf("config: %s: %w", path, err)
	}
	return s, nil
}

// Save writes the settings file, creating its directory. The write goes
// through a temporary file in the same directory so a crash cannot leave half
// a settings file behind.
func Save(dir string, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	path, err := Path(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: create %s: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode settings: %w", err)
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), FileName+".*")
	if err != nil {
		return fmt.Errorf("config: create a temporary file next to %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(b); err != nil {
		return fmt.Errorf("config: write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("config: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("config: replace %s: %w", path, err)
	}
	return nil
}

// Validate rejects a settings file a human edited into something unusable.
func (s Settings) Validate() error {
	if s.Port < 0 || s.Port > 65535 {
		return fmt.Errorf("config: port %d is outside 0-65535", s.Port)
	}
	if s.Port > 0 && s.Port < 1024 {
		// A privileged port would need the service to run as root, which a
		// per-user clipboard agent never should.
		return fmt.Errorf("config: port %d is privileged; use 1024-65535", s.Port)
	}
	return nil
}

// ListenPort resolves the port to bind.
func (s Settings) ListenPort() int {
	if s.Port == 0 {
		return DefaultPort
	}
	return s.Port
}
