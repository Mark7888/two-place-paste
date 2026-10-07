package clipboard

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// commandTimeout bounds every helper process. A clipboard read that hangs must
// not take the poll loop with it.
const commandTimeout = 10 * time.Second

// macOS clipboard access.
//
// Content goes through two tools present on every supported macOS:
// pbpaste/pbcopy for text and osascript for everything else. An NSPasteboard
// binding for the content would mean marshalling three payload kinds through
// cgo; the tools cover the three SPEC §7.2 asks for already.
//
// The cost is a process per flavour per read, which is why the watcher must
// not read on every poll. Sequence is how it avoids that: the pasteboard's
// change counter, read through AppKit (changecount_darwin.go), tells it when
// there is anything new to read at all.
type darwinClipboard struct{}

func newClipboard() Clipboard { return darwinClipboard{} }

func (darwinClipboard) Available() bool { return true }

// Sequence is NSPasteboard's changeCount, or ErrNoSequence in a build without
// cgo.
func (darwinClipboard) Sequence(context.Context) (uint64, error) {
	n, ok := pasteboardChangeCount()
	if !ok {
		return 0, ErrNoSequence
	}
	return n, nil
}

func (c darwinClipboard) Read(ctx context.Context) (Content, error) {
	// File first: a copied file also offers a text flavour holding its name,
	// and answering with the name instead of the file would be wrong.
	if path, err := c.readFilePath(ctx); err == nil {
		body, err := os.ReadFile(path)
		if err != nil {
			return Content{}, fmt.Errorf("clipboard: read the copied file: %w", err)
		}
		return Content{
			ContentType: TypeOctetStream,
			Filename:    SafeName(filepath.Base(path)),
			Body:        body,
		}, nil
	} else if !errors.Is(err, ErrEmpty) {
		return Content{}, err
	}
	// ErrEmpty here means "no file on the pasteboard", not "nothing on the
	// pasteboard": the image and text flavours below are still to come.

	if png, err := c.readPNG(ctx); err == nil {
		return Content{ContentType: TypeImagePNG, Body: png}, nil
	} else if !errors.Is(err, ErrEmpty) {
		return Content{}, err
	}

	out, err := run(ctx, "/usr/bin/pbpaste")
	if err != nil {
		return Content{}, fmt.Errorf("clipboard: pbpaste: %w", err)
	}
	if len(out) == 0 {
		return Content{}, ErrEmpty
	}
	return Content{ContentType: TypeText, Body: out}, nil
}

func (c darwinClipboard) Write(ctx context.Context, item Content) error {
	switch item.Kind() {
	case KindText:
		cmd := exec.CommandContext(ctx, "/usr/bin/pbcopy")
		cmd.Env = helperEnv()
		cmd.Stdin = bytes.NewReader(item.Body)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("clipboard: pbcopy: %w", err)
		}
		return nil
	case KindImage:
		return c.writeVia(ctx, "clipboard.png", item.Body,
			`set the clipboard to (read (POSIX file %s) as «class PNGf»)`)
	case KindFile:
		return c.writeVia(ctx, SafeName(item.Filename), item.Body,
			`set the clipboard to (POSIX file %s)`)
	default:
		return fmt.Errorf("clipboard: nothing to write")
	}
}

// writeVia stages the payload in a private directory and hands osascript its
// path. A file put on the clipboard must outlive the write — the pasteboard
// holds a reference, not the bytes — so the staging directory is per-user and
// deliberately not removed here; the next write of the same name replaces it.
func (darwinClipboard) writeVia(ctx context.Context, name string, body []byte, script string) error {
	dir, err := stagingDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("clipboard: stage the payload: %w", err)
	}
	if _, err := run(ctx, "/usr/bin/osascript", "-e", fmt.Sprintf(script, appleScriptString(path))); err != nil {
		return fmt.Errorf("clipboard: osascript: %w", err)
	}
	return nil
}

// readFilePath returns the path of a file on the pasteboard, or ErrEmpty when
// there is none.
//
// The flavour check in front of the coercion is the whole point of this
// function. `the clipboard as «class furl»` does not fail when the pasteboard
// holds only text: AppleScript coerces the text to a file reference, reads it
// as an HFS path, and hands back a path that was never a file — copying the
// word "Group" yielded "/Group", and every read after that failed with "open
// /Group: no such file or directory" until the clipboard changed. `clipboard
// info for` asks what is actually on the pasteboard and answers {} rather than
// inventing a flavour, so the coercion only ever runs on something that really
// is a file. Both flavours count: an application that offers only the older
// «class hfs » has a file on the pasteboard just as much as one that offers a
// file URL, and the coercion reaches a path from either.
func (darwinClipboard) readFilePath(ctx context.Context) (string, error) {
	out, err := run(ctx, "/usr/bin/osascript",
		"-e", `if (clipboard info for «class furl») is {} and (clipboard info for «class hfs ») is {} then return ""`,
		"-e", `POSIX path of (the clipboard as «class furl»)`)
	if err != nil {
		// osascript exits non-zero when the clipboard holds no file, which is
		// the ordinary case rather than a failure worth reporting.
		return "", ErrEmpty
	}
	path := strings.TrimSpace(string(out))
	if !usableFile(path) {
		// A relative path, a directory or something that has since been moved
		// is not a payload this service can carry, and it is not a reason to
		// refuse to read the pasteboard at all: the caller falls through to
		// the image and text flavours.
		return "", ErrEmpty
	}
	return path, nil
}

// readPNG returns the pasteboard's image as PNG, or ErrEmpty when it holds no
// image.
//
// Unlike readFilePath this one coerces unguarded, deliberately. A screenshot
// arrives as TIFF and nothing else, and the coercion is what turns it into the
// one encoding both platforms round-trip (SPEC §7.2); requiring the PNGf
// flavour first would quietly stop carrying screenshots. It is also safe to
// ask for: AppleScript cannot make raw image data out of text the way it makes
// a file reference out of it, so text on the pasteboard fails here rather than
// coercing into something plausible.
func (darwinClipboard) readPNG(ctx context.Context) ([]byte, error) {
	out, err := run(ctx, "/usr/bin/osascript", "-e", `the clipboard as «class PNGf»`)
	if err != nil {
		return nil, ErrEmpty
	}
	// osascript renders raw data as «data PNGf89504E47...».
	s := strings.TrimSpace(string(out))
	const prefix = "«data PNGf"
	if !strings.HasPrefix(s, prefix) || !strings.HasSuffix(s, "»") {
		return nil, ErrEmpty
	}
	raw, err := hex.DecodeString(strings.TrimSuffix(strings.TrimPrefix(s, prefix), "»"))
	if err != nil {
		return nil, fmt.Errorf("clipboard: decode the pasteboard image: %w", err)
	}
	if len(raw) == 0 {
		return nil, ErrEmpty
	}
	return raw, nil
}

func stagingDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("clipboard: locate the user cache directory: %w", err)
	}
	dir := filepath.Join(base, "TwoPlacePaste", "clipboard")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("clipboard: create %s: %w", dir, err)
	}
	return dir, nil
}

// appleScriptString quotes a path for AppleScript. Only the backslash and the
// double quote are special inside an AppleScript string literal.
func appleScriptString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// helperEnv is the environment every helper process runs with: this one's,
// with the character encoding pinned to UTF-8.
//
// pbcopy and pbpaste encode text by the locale, and a service started by
// launchd or from Finder has none — no LANG, no LC_*. They then fall back to
// Mac Roman, so "á" sent as UTF-8 was pasted as "√°", and a copy with accents
// was uploaded as bytes no other device could read as text. The entry on the
// relay was right all along, which is why the history preview showed it
// correctly. LC_ALL wins over anything the user's environment does set, and
// overriding only the encoding category would not be enough for a value set
// in LC_ALL already.
func helperEnv() []string {
	return append(os.Environ(), "LC_ALL=en_US.UTF-8", "LANG=en_US.UTF-8")
}

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = helperEnv()
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
