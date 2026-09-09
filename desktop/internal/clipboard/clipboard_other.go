//go:build !windows && !darwin

package clipboard

import "context"

// unsupportedClipboard is what a Linux build gets. The Linux desktop is
// deferred (SPEC §7.4) — X11 and Wayland are two more implementations, and on
// X11 the service would have to hold the selection for as long as it wants the
// content to survive — so this build compiles, runs, and says plainly that it
// cannot reach a clipboard rather than pretending the clipboard is empty.
type unsupportedClipboard struct{}

func newClipboard() Clipboard { return unsupportedClipboard{} }

func (unsupportedClipboard) Available() bool { return false }

func (unsupportedClipboard) Read(context.Context) (Content, error) {
	return Content{}, ErrUnsupported
}

func (unsupportedClipboard) Write(context.Context, Content) error { return ErrUnsupported }

func (unsupportedClipboard) Sequence(context.Context) (uint64, error) { return 0, ErrNoSequence }
