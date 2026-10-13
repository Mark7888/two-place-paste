// Package update keeps the desktop service up to date
// (docs/plans/versioning-releases-and-updates.md §3).
//
// It finds the newest build on the user's channel, downloads it, refuses it
// unless its checksum is in a manifest signed by the key compiled into this
// binary, and swaps it in place of the running one:
//
//   - Stable reads the latest GitHub Release, Beta the rolling channel-beta
//     prerelease. Both are plain release-download URLs: no API, no token.
//   - Nightly installs the build of one commit the user names, through the
//     Actions API, with a token the user provides.
//
// A build is only ever installed by Manager.Install, which a person or the
// auto-update schedule asks for; nothing here runs on import.
package update
