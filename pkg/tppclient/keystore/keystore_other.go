//go:build !darwin

package keystore

// openOSStore reports that this platform has no credential store this package
// can drive without cgo. Open falls back to the file backend, whose Backend()
// tells the caller what it got.
//
// Linux is deliberately here rather than wired to Secret Service over D-Bus:
// the Linux desktop is a deferred item (ROADMAP backlog), and a half-working
// keyring integration would be worse than an honest owner-only file.
func openOSStore(Options) (Store, error) { return nil, ErrUnavailable }
