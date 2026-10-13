// Package buildinfo says which build of the desktop service this is.
//
// The values are stamped in at link time by CI (scripts/version.sh, and the
// -X flags in .github/workflows/desktop.yml); a plain `go build` from a
// checkout keeps the defaults below. The auto-updater treats the "local"
// channel as a build it must never replace, so a developer's binary is not
// swapped for a downloaded one under their feet.
package buildinfo

import "strconv"

// LocalChannel is the channel of a binary built outside CI.
const LocalChannel = "local"

// Set with -ldflags "-X github.com/Mark7888/two-place-paste/desktop/internal/buildinfo.<Name>=…".
// They are variables, not constants, only because the linker can set nothing else.
var (
	// Version is the human-readable version: 1.2.3 for a release,
	// 1.2.4-dev.20261008161500+abc1234 for any other CI build.
	Version = "0.0.0-local"

	// Channel is stable, beta, nightly, or LocalChannel.
	Channel = LocalChannel

	// Commit is the full SHA of the commit the build was made from.
	Commit = ""

	// Stamp orders builds: seconds since 2025-01-01T00:00:00Z when CI built
	// this one. A larger stamp is a newer build.
	Stamp = "0"
)

// StampValue is Stamp as a number, or zero when it is missing or malformed —
// which sorts a build of unknown age before every real one.
func StampValue() int64 {
	n, err := strconv.ParseInt(Stamp, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// Local reports whether this binary was built outside CI.
func Local() bool { return Channel == LocalChannel }
