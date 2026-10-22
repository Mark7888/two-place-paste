//go:build darwin && !cgo

package clipboard

// pasteboardChangeCount is unavailable without cgo. The watcher then falls
// back to reading the content on every poll, which is correct and costs a
// process per flavour per poll.
func pasteboardChangeCount() (uint64, bool) { return 0, false }

// pasteboardTypes is unavailable without cgo, and so is the privacy marker:
// a build without cgo cannot tell a password manager's copy from any other.
// Release builds use cgo.
func pasteboardTypes() ([]string, uint64, bool) { return nil, 0, false }
