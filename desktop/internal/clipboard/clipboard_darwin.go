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

// macOS clipboard access without cgo.
//
// AppKit would be the direct route, and it is not taken: an NSPasteboard
// binding drags cgo into a module that otherwise cross-compiles from any
// machine, and the two tools used here — pbpaste/pbcopy for text and osascript
// for everything else — are present on every supported macOS and cover the
// three payload kinds SPEC §7.2 asks for. The cost is a process per operation,
// which at a 750ms poll is not a cost anyone can measure.
type darwinClipboard struct{}

func newClipboard() Clipboard { return darwinClipboard{} }

func (darwinClipboard) Available() bool { return true }

// Sequence has no cheap equivalent here: NSPasteboard's changeCount is not
// exposed by any stock command-line tool, and spawning osascript to read it
// would cost more than the content read it saves.
func (darwinClipboard) Sequence(context.Context) (uint64, error) { return 0, ErrNoSequence }

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

func (darwinClipboard) readFilePath(ctx context.Context) (string, error) {
	out, err := run(ctx, "/usr/bin/osascript", "-e", `POSIX path of (the clipboard as «class furl»)`)
	if err != nil {
		// osascript exits non-zero when the clipboard holds no file, which is
		// the ordinary case rather than a failure worth reporting.
		return "", ErrEmpty
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", ErrEmpty
	}
	return path, nil
}

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

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
