//go:build !windows

package keystore

// fileProtector returns the protection a file backend uses on this platform.
// Everywhere but Windows that is the caller's passphrase, or nothing but file
// permissions when there is no passphrase.
func fileProtector(opts Options) (protector, error) {
	return defaultFileProtector(opts), nil
}
