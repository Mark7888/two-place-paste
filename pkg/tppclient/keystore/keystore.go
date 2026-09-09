// Package keystore stores the two secrets a TwoPlacePaste client holds: its
// X25519 device private key and the current group key (/spec/crypto.md §10).
//
// The rule the spec states is normative: those bytes go where other local
// applications cannot read them — the OS keychain or credential store where
// one exists, and an owner-only file otherwise. Open picks the strongest
// backend the platform offers and falls back in that order:
//
//	macOS    the login keychain, through /usr/bin/security
//	Windows  a file sealed with DPAPI under the current user account
//	other    a file encrypted with a caller-supplied passphrase, or, with no
//	         passphrase, a plain file created with mode 0600
//
// The fallback file is never silently chosen: Store.Backend names what is
// actually in use so a client can tell the user.
//
// Nothing in this package logs a secret, embeds one in an error, or hands one
// to a subprocess argument list (docs/conventions.md §1, §2).
package keystore

import (
	"errors"
	"fmt"
	"regexp"
)

// ErrNotFound is returned by Load when the named secret is not stored.
var ErrNotFound = errors.New("keystore: secret not found")

// ErrUnavailable is returned by a backend that this platform or this process
// cannot use. It is what makes Open fall back rather than fail.
var ErrUnavailable = errors.New("keystore: backend unavailable")

// Backend names a concrete storage mechanism, for display and for tests.
type Backend string

const (
	// BackendKeychain is the macOS login keychain.
	BackendKeychain Backend = "macos-keychain"

	// BackendDPAPI is a file whose contents are sealed with the Windows Data
	// Protection API under the current user account.
	BackendDPAPI Backend = "windows-dpapi"

	// BackendEncryptedFile is a file encrypted with a key derived from a
	// caller-supplied passphrase.
	BackendEncryptedFile Backend = "encrypted-file"

	// BackendFile is a plain file with owner-only permissions: the weakest
	// option the spec allows, and only used when nothing else is available.
	BackendFile Backend = "file"
)

// Store holds named secrets.
//
// A name identifies one secret within one service — "device-key",
// "group-state" — and is not itself secret. Implementations must treat a
// missing secret as ErrNotFound rather than as an empty value, so a caller
// never mistakes "no key yet" for "a zero key".
type Store interface {
	// Load returns the named secret, or ErrNotFound.
	Load(name string) ([]byte, error)

	// Save writes the named secret, replacing any previous value.
	Save(name string, secret []byte) error

	// Delete removes the named secret. Deleting a secret that is not there is
	// not an error: the post-condition is the same.
	Delete(name string) error

	// Backend names the mechanism actually in use.
	Backend() Backend
}

// Options configures Open.
type Options struct {
	// Service groups this client's secrets in the OS store and names the
	// directory used by the file backends. Defaults to DefaultService.
	Service string

	// Dir is where a file backend keeps its files. Defaults to a per-user
	// application directory under os.UserConfigDir.
	Dir string

	// Passphrase encrypts the file backend. It is used only when no OS store
	// is available; an empty passphrase there leaves an owner-only plain file,
	// which /spec/crypto.md §10 permits as the floor.
	Passphrase []byte

	// ForceFile skips the OS store. Tests use it; so does a headless install
	// where a keychain prompt would block.
	ForceFile bool
}

// DefaultService is the service name secrets are filed under.
const DefaultService = "TwoPlacePaste"

// nameRE bounds a secret name to characters every backend accepts, including a
// filesystem path segment and a keychain account argument.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func checkName(name string) error {
	if !nameRE.MatchString(name) || name == "." || name == ".." {
		return fmt.Errorf("keystore: %q is not a usable secret name", name)
	}
	return nil
}

// Open returns the strongest store this platform offers.
//
// It never returns a store it could not actually use: an OS backend that
// reports ErrUnavailable is discarded and the file backend takes over, so a
// client on a machine with no keychain still runs.
func Open(opts Options) (Store, error) {
	if opts.Service == "" {
		opts.Service = DefaultService
	}
	if !opts.ForceFile {
		st, err := openOSStore(opts)
		switch {
		case err == nil:
			return st, nil
		case !errors.Is(err, ErrUnavailable):
			return nil, err
		}
	}
	return openFileStore(opts)
}
