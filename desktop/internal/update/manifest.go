package update

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrBadSignature reports a manifest whose signature does not verify against
// the key compiled into this binary. Its build is never installed.
var ErrBadSignature = errors.New("update: the manifest's signature does not verify")

// Manifest is what scripts/manifest.sh writes for one platform's build.
type Manifest struct {
	Schema     int    `json:"schema"`
	Platform   string `json:"platform"`
	Repository string `json:"repository"`
	Version    string `json:"version"`
	Numeric    string `json:"numeric"`
	Stamp      int64  `json:"stamp"`
	Channel    string `json:"channel"`
	Commit     string `json:"commit"`
	RunID      string `json:"run_id"`
	Files      []File `json:"files"`
}

// File is one file a manifest vouches for.
type File struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// VerifyManifest checks the signature over the manifest's exact bytes and
// only then parses them. Nothing is read out of a manifest before its
// signature has been checked.
func VerifyManifest(data, sig []byte, key ed25519.PublicKey) (Manifest, error) {
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(key, data, sig) {
		return Manifest{}, ErrBadSignature
	}
	return parseManifest(data)
}

// parseManifest reads a manifest without checking any signature: for a build
// that has none, which only the Nightly channel accepts, after a person
// confirms it.
func parseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("update: parse the manifest: %w", err)
	}
	if m.Schema != 1 {
		return Manifest{}, fmt.Errorf("update: manifest schema %d is not one this build reads", m.Schema)
	}
	if m.Platform != "desktop" {
		return Manifest{}, fmt.Errorf("update: the manifest is for %q, not the desktop app", m.Platform)
	}
	if m.Stamp <= 0 || m.Version == "" {
		return Manifest{}, errors.New("update: the manifest has no version")
	}
	return m, nil
}

// File returns the entry for the named file.
func (m Manifest) File(name string) (File, bool) {
	for _, f := range m.Files {
		if f.Name == name {
			return f, true
		}
	}
	return File{}, false
}
