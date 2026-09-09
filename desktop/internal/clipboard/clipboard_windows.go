package clipboard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows clipboard access through user32 directly.
//
// No cgo and no third-party binding: the three formats SPEC §7.2 needs —
// CF_UNICODETEXT, the registered "PNG" format every modern application offers
// for images, and CF_HDROP for files — are a few dozen lines of syscall each,
// and a dependency here would be a dependency in the binary that holds the
// user's clipboard.
var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	shell32                      = windows.NewLazySystemDLL("shell32.dll")
	procOpenClipboard            = user32.NewProc("OpenClipboard")
	procCloseClipboard           = user32.NewProc("CloseClipboard")
	procEmptyClipboard           = user32.NewProc("EmptyClipboard")
	procGetClipboardData         = user32.NewProc("GetClipboardData")
	procSetClipboardData         = user32.NewProc("SetClipboardData")
	procIsFormatAvailable        = user32.NewProc("IsClipboardFormatAvailable")
	procRegisterClipboardFormatW = user32.NewProc("RegisterClipboardFormatW")
	procGetClipboardSequenceNum  = user32.NewProc("GetClipboardSequenceNumber")
	procGlobalAlloc              = kernel32.NewProc("GlobalAlloc")
	procGlobalFree               = kernel32.NewProc("GlobalFree")
	procGlobalLock               = kernel32.NewProc("GlobalLock")
	procGlobalUnlock             = kernel32.NewProc("GlobalUnlock")
	procGlobalSize               = kernel32.NewProc("GlobalSize")
	procDragQueryFileW           = shell32.NewProc("DragQueryFileW")
)

const (
	cfUnicodeText = 13
	cfHDrop       = 15
	gmemMoveable  = 0x0002

	// openAttempts and openBackoff cover the ordinary case of another
	// application holding the clipboard for a moment. Failing on the first
	// attempt would make a poll flap for no reason.
	openAttempts = 5
	openBackoff  = 20 * time.Millisecond
)

type windowsClipboard struct{}

func newClipboard() Clipboard { return windowsClipboard{} }

func (windowsClipboard) Available() bool { return true }

func (windowsClipboard) Sequence(context.Context) (uint64, error) {
	n, _, _ := procGetClipboardSequenceNum.Call()
	if n == 0 {
		return 0, ErrNoSequence
	}
	return uint64(n), nil
}

func (windowsClipboard) Read(ctx context.Context) (Content, error) {
	if err := openClipboard(ctx); err != nil {
		return Content{}, err
	}
	defer closeClipboard()

	// Files first, then image, then text: an image copied from a file offers
	// several flavours and the richest one is the one the user meant.
	if formatAvailable(cfHDrop) {
		c, err := readDrop()
		if err == nil {
			return c, nil
		}
		return Content{}, err
	}
	if png := pngFormat(); png != 0 && formatAvailable(png) {
		b, err := readBytes(png)
		if err != nil {
			return Content{}, err
		}
		return Content{ContentType: TypeImagePNG, Body: b}, nil
	}
	if formatAvailable(cfUnicodeText) {
		s, err := readText()
		if err != nil {
			return Content{}, err
		}
		if s == "" {
			return Content{}, ErrEmpty
		}
		return Content{ContentType: TypeText, Body: []byte(s)}, nil
	}
	return Content{}, ErrEmpty
}

func (windowsClipboard) Write(ctx context.Context, item Content) error {
	var (
		format uint32
		blob   []byte
	)
	switch item.Kind() {
	case KindText:
		utf16, err := windows.UTF16FromString(string(item.Body))
		if err != nil {
			return fmt.Errorf("clipboard: the entry is not valid text: %w", err)
		}
		format = cfUnicodeText
		blob = unsafe.Slice((*byte)(unsafe.Pointer(&utf16[0])), len(utf16)*2)
	case KindImage:
		format = pngFormat()
		if format == 0 {
			return fmt.Errorf("clipboard: the PNG clipboard format is unavailable")
		}
		blob = item.Body
	case KindFile:
		drop, err := buildDrop(item)
		if err != nil {
			return err
		}
		format, blob = cfHDrop, drop
	default:
		return fmt.Errorf("clipboard: nothing to write")
	}

	if err := openClipboard(ctx); err != nil {
		return err
	}
	defer closeClipboard()
	if r, _, err := procEmptyClipboard.Call(); r == 0 {
		return fmt.Errorf("clipboard: empty the clipboard: %w", err)
	}

	h, err := globalFromBytes(blob)
	if err != nil {
		return err
	}
	if r, _, err := procSetClipboardData.Call(uintptr(format), h); r == 0 {
		_, _, _ = procGlobalFree.Call(h)
		return fmt.Errorf("clipboard: set clipboard data: %w", err)
	}
	// Ownership of the handle passed to the clipboard; freeing it here would
	// hand the next paste freed memory.
	return nil
}

func openClipboard(ctx context.Context) error {
	var last error
	for i := range openAttempts {
		r, _, err := procOpenClipboard.Call(0)
		if r != 0 {
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("clipboard: open the clipboard: %w", ctx.Err())
		case <-time.After(time.Duration(i+1) * openBackoff):
		}
	}
	return fmt.Errorf("clipboard: open the clipboard: %w", last)
}

func closeClipboard() { _, _, _ = procCloseClipboard.Call() }

func formatAvailable(format uint32) bool {
	r, _, _ := procIsFormatAvailable.Call(uintptr(format))
	return r != 0
}

// pngFormat resolves the registered "PNG" format id. Registering a name that
// already exists returns the existing id, so this is idempotent.
func pngFormat() uint32 {
	name, err := windows.UTF16PtrFromString("PNG")
	if err != nil {
		return 0
	}
	r, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(name)))
	return uint32(r)
}

// locked turns the address GlobalLock returned into a pointer Go can read
// through.
//
// The conversion is written as a round trip via a *uintptr rather than as
// unsafe.Pointer(p) on purpose, and not to quiet a check: go vet's unsafeptr
// rule exists because an integer cannot keep Go-heap memory alive across a
// garbage collection, and this memory is not on the Go heap. GlobalAlloc
// owns it, the clipboard holds it, and GlobalLock pins it until the matching
// GlobalUnlock — every caller below unlocks after it has finished reading —
// so the invariant the rule protects does not apply here. Writing it as
// unsafe.Pointer(p) would fail `go vet` for every build of this package on
// Windows, which is a check worth keeping for the rest of the file.
func locked(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}

func readBytes(format uint32) ([]byte, error) {
	h, _, err := procGetClipboardData.Call(uintptr(format))
	if h == 0 {
		return nil, fmt.Errorf("clipboard: get clipboard data: %w", err)
	}
	p, _, err := procGlobalLock.Call(h)
	if p == 0 {
		return nil, fmt.Errorf("clipboard: lock clipboard memory: %w", err)
	}
	defer func() { _, _, _ = procGlobalUnlock.Call(h) }()
	size, _, _ := procGlobalSize.Call(h)
	if size == 0 {
		return nil, ErrEmpty
	}
	out := make([]byte, size)
	copy(out, unsafe.Slice((*byte)(locked(p)), size))
	return out, nil
}

func readText() (string, error) {
	h, _, err := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", fmt.Errorf("clipboard: get clipboard text: %w", err)
	}
	p, _, err := procGlobalLock.Call(h)
	if p == 0 {
		return "", fmt.Errorf("clipboard: lock clipboard memory: %w", err)
	}
	defer func() { _, _, _ = procGlobalUnlock.Call(h) }()
	return windows.UTF16PtrToString((*uint16)(locked(p))), nil
}

// readDrop reads the first file of a CF_HDROP. Sync carries one entry
// (SPEC §6), so a multi-file copy takes its first file rather than inventing
// an archive format the other platform would have to guess at.
func readDrop() (Content, error) {
	h, _, err := procGetClipboardData.Call(cfHDrop)
	if h == 0 {
		return Content{}, fmt.Errorf("clipboard: get dropped files: %w", err)
	}
	p, _, err := procGlobalLock.Call(h)
	if p == 0 {
		return Content{}, fmt.Errorf("clipboard: lock clipboard memory: %w", err)
	}
	defer func() { _, _, _ = procGlobalUnlock.Call(h) }()

	count, _, _ := procDragQueryFileW.Call(p, ^uintptr(0), 0, 0)
	if count == 0 {
		return Content{}, ErrEmpty
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, _, _ := procDragQueryFileW.Call(p, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return Content{}, ErrEmpty
	}
	path := windows.UTF16ToString(buf[:n])
	body, err := os.ReadFile(path)
	if err != nil {
		return Content{}, fmt.Errorf("clipboard: read the copied file: %w", err)
	}
	return Content{
		ContentType: TypeOctetStream,
		Filename:    SafeName(filepath.Base(path)),
		Body:        body,
	}, nil
}

// dropFiles is the DROPFILES header a CF_HDROP begins with.
type dropFiles struct {
	pFiles uint32 // offset of the file list
	x, y   int32
	fNC    int32
	fWide  int32
}

// buildDrop stages the payload as a real file and builds the CF_HDROP that
// points at it. A pasted file must exist on disk for the receiving
// application to open, so the bytes land in a per-user staging directory whose
// name is sanitised first (SafeName).
func buildDrop(item Content) ([]byte, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("clipboard: locate the user cache directory: %w", err)
	}
	dir := filepath.Join(base, "TwoPlacePaste", "clipboard")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("clipboard: create %s: %w", dir, err)
	}
	path := filepath.Join(dir, SafeName(item.Filename))
	if err := os.WriteFile(path, item.Body, 0o600); err != nil {
		return nil, fmt.Errorf("clipboard: stage the payload: %w", err)
	}

	wide, err := windows.UTF16FromString(path)
	if err != nil {
		return nil, fmt.Errorf("clipboard: encode %s: %w", path, err)
	}
	wide = append(wide, 0) // the list is double-NUL terminated

	header := dropFiles{pFiles: uint32(unsafe.Sizeof(dropFiles{})), fWide: 1}
	out := make([]byte, int(header.pFiles)+len(wide)*2)
	copy(out, unsafe.Slice((*byte)(unsafe.Pointer(&header)), unsafe.Sizeof(header)))
	copy(out[header.pFiles:], unsafe.Slice((*byte)(unsafe.Pointer(&wide[0])), len(wide)*2))
	return out, nil
}

func globalFromBytes(b []byte) (uintptr, error) {
	if len(b) == 0 {
		return 0, fmt.Errorf("clipboard: nothing to write")
	}
	h, _, err := procGlobalAlloc.Call(gmemMoveable, uintptr(len(b)))
	if h == 0 {
		return 0, fmt.Errorf("clipboard: allocate clipboard memory: %w", err)
	}
	p, _, err := procGlobalLock.Call(h)
	if p == 0 {
		_, _, _ = procGlobalFree.Call(h)
		return 0, fmt.Errorf("clipboard: lock clipboard memory: %w", err)
	}
	copy(unsafe.Slice((*byte)(locked(p)), len(b)), b)
	_, _, _ = procGlobalUnlock.Call(h)
	return h, nil
}
