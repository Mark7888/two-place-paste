//go:build linux || darwin || freebsd

package blob

import "syscall"

// freeBytes reports the bytes available to an unprivileged writer on the
// volume holding path.
func freeBytes(path string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	// The field types differ by platform (int64 Bavail on FreeBSD, uint32
	// Bsize on macOS), so both are widened explicitly; a volume over 8 EiB is
	// not a case worth handling.
	return int64(uint64(st.Bavail) * uint64(st.Bsize)), true //nolint:unconvert,gosec // see above
}
