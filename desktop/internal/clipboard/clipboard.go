// Package clipboard reads and writes the OS clipboard on Windows and macOS.
//
// It exists in this module and not in pkg/tppclient on purpose: the client
// core cannot reach a clipboard at all, which is what makes "a rekey never
// touches the local clipboard" (SPEC §3.3) a property of the design rather
// than a rule someone has to remember. Everything platform-specific is behind
// the Clipboard interface, and the sync logic above it never learns which
// platform it is on.
//
// Three kinds of payload cross the wire (SPEC §7.2): text, an image, and file
// references. A file reference is carried as the file's bytes plus its bare
// name, never as a path: a path from another machine means nothing on this
// one, and treating a received name as a path would be a directory-traversal
// bug waiting to happen.
package clipboard

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Media types this package produces and understands. Anything else that
// arrives from another device is written as a file when it has a filename and
// refused otherwise.
const (
	// TypeText is UTF-8 text, the common case.
	TypeText = "text/plain; charset=utf-8"

	// TypeImagePNG is the one image encoding both platforms can round-trip
	// without a colour-space argument.
	TypeImagePNG = "image/png"

	// TypeOctetStream is the fallback for a file whose type is unknown
	// (/spec/crypto.md §6).
	TypeOctetStream = "application/octet-stream"
)

// Kind is the coarse shape of a clipboard payload, for display.
type Kind string

// The kinds a UI distinguishes.
const (
	KindText  Kind = "text"
	KindImage Kind = "image"
	KindFile  Kind = "file"
)

// Sentinel conditions a caller branches on.
var (
	// ErrEmpty means the clipboard holds nothing this service can carry. It is
	// not a failure: a freshly booted machine has an empty clipboard.
	ErrEmpty = errors.New("clipboard: nothing to read")

	// ErrUnsupported means this platform has no clipboard implementation. The
	// Linux desktop is deferred (SPEC §7.4) and builds against this.
	ErrUnsupported = errors.New("clipboard: not supported on this platform")

	// ErrNoSequence means the platform exposes no cheap change counter, so a
	// watcher must compare content instead.
	ErrNoSequence = errors.New("clipboard: no change counter on this platform")
)

// Content is one clipboard payload in plaintext. It maps one-to-one onto the
// encrypted frame of /spec/crypto.md §6, which is why the server never learns
// any of these three fields.
type Content struct {
	// ContentType is an IANA media type. Never empty for a non-empty payload.
	ContentType string

	// Filename is a bare filename for a file payload and empty otherwise. It
	// is untrusted display text: a consumer must not join it onto a path
	// without sanitising it first — Base is what this package offers for that.
	Filename string

	// Body is the payload bytes.
	Body []byte
}

// Empty reports whether there is nothing to carry.
func (c Content) Empty() bool { return len(c.Body) == 0 && c.ContentType == "" }

// Kind classifies the payload for a UI.
func (c Content) Kind() Kind {
	switch {
	case c.Filename != "":
		return KindFile
	case strings.HasPrefix(c.ContentType, "image/"):
		return KindImage
	default:
		return KindText
	}
}

// Text returns the payload as text when it is text, and "" otherwise.
func (c Content) Text() string {
	if c.Kind() == KindText && strings.HasPrefix(c.ContentType, "text/") {
		return string(c.Body)
	}
	return ""
}

// Digest identifies the payload for change detection and self-write
// suppression. It covers the type and the filename as well as the bytes, so
// re-copying the same bytes as a different kind still reads as a change.
func (c Content) Digest() [32]byte {
	h := sha256.New()
	h.Write([]byte(c.ContentType))
	h.Write([]byte{0})
	h.Write([]byte(c.Filename))
	h.Write([]byte{0})
	h.Write(c.Body)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// usableFile reports whether a path from the clipboard names a regular file
// whose bytes this service can carry.
//
// Both platforms hand out paths that are not payloads. A folder copied in
// Finder or Explorer is a file reference like any other; a file dragged to the
// clipboard and then moved leaves a path that no longer resolves; and on macOS
// AppleScript will coerce plain text into a file reference if it is asked to,
// which is how "/Group" once reached os.ReadFile. None of those is a failure
// worth refusing the whole read for, so the platform reads fall through to the
// image and text flavours instead.
func usableFile(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// Clipboard is the OS clipboard. Implementations are per-GOOS and are chosen
// at build time, never at run time.
type Clipboard interface {
	// Read returns what is on the clipboard now, or ErrEmpty.
	Read(ctx context.Context) (Content, error)

	// Write puts content on the clipboard, replacing what was there.
	Write(ctx context.Context, c Content) error

	// Sequence returns the platform's clipboard change counter, or
	// ErrNoSequence when the platform has none. It is an optimisation: a
	// watcher that has one avoids reading the whole payload on every tick.
	Sequence(ctx context.Context) (uint64, error)

	// Available reports whether this build can use the clipboard at all.
	Available() bool
}

// New returns this platform's clipboard.
func New() Clipboard { return newClipboard() }
