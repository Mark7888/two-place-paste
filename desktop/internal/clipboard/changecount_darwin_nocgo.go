//go:build darwin && !cgo

package clipboard

// pasteboardChangeCount is unavailable without cgo. The watcher then falls
// back to reading the content on every poll, which is correct and costs a
// process per flavour per poll.
func pasteboardChangeCount() (uint64, bool) { return 0, false }
