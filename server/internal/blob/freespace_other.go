//go:build !(linux || darwin || freebsd)

package blob

// freeBytes cannot read free space on this platform; the floor check is
// skipped. The server ships for Linux only (deploy/Dockerfile).
func freeBytes(string) (int64, bool) { return 0, false }
