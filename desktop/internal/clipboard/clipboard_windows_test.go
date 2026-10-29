package clipboard

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Concurrent reads of a large image used to crash the process: Windows ties an
// open clipboard to an OS thread, Go moves goroutines between threads, and one
// read's CloseClipboard could land while another was still copying the image
// out of the clipboard's memory — an access violation, not an error. A status
// request from each of two browser tabs was enough.
func TestConcurrentReadsOfALargeImage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c := newClipboard()

	// 5 MB, the size that crashed it. Not a valid PNG, which the clipboard
	// does not check: only the bytes and their length matter here.
	img := bytes.Repeat([]byte("TwoPlacePaste "), 5<<20/14)
	if err := c.Write(ctx, Content{ContentType: TypeImagePNG, Body: img}); err != nil {
		t.Skipf("this session has no usable clipboard: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for range 8 {
		wg.Go(func() {
			for range 8 {
				got, err := c.Read(ctx)
				if err != nil {
					errs <- err
					return
				}
				// A clipboard format may be padded to an allocation boundary.
				if !bytes.HasPrefix(got.Body, img) {
					t.Errorf("read %d bytes that are not the image written", len(got.Body))
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Read: %v", err)
	}
}

// writeMarked puts text on the clipboard together with extra registered
// formats, the way a password manager does.
func writeMarked(ctx context.Context, text string, extra map[string][]byte) error {
	utf16, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	release, err := acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if r, _, err := procEmptyClipboard.Call(); r == 0 {
		return err
	}
	set := func(format uint32, b []byte) error {
		h, err := globalFromBytes(b)
		if err != nil {
			return err
		}
		if r, _, err := procSetClipboardData.Call(uintptr(format), h); r == 0 {
			_, _, _ = procGlobalFree.Call(h)
			return err
		}
		return nil
	}
	if err := set(cfUnicodeText, unsafe.Slice((*byte)(unsafe.Pointer(&utf16[0])), len(utf16)*2)); err != nil {
		return err
	}
	for name, b := range extra {
		if err := set(registeredFormat(name), b); err != nil {
			return err
		}
	}
	return nil
}

func TestReadReportsPrivacyFormats(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c := newClipboard()
	tests := []struct {
		name  string
		extra map[string][]byte
		want  string
	}{
		{"plain copy", nil, ""},
		{"excluded from monitors", map[string][]byte{winExcludeFromMonitors: {0}}, ReasonConcealed},
		{"not for history", map[string][]byte{winCanIncludeInHistory: {0, 0, 0, 0}}, ReasonConcealed},
		{"fine for history", map[string][]byte{winCanIncludeInHistory: {1, 0, 0, 0}}, ""},
	}
	for _, tt := range tests {
		if err := writeMarked(ctx, "s3cret", tt.extra); err != nil {
			t.Skipf("this session has no usable clipboard: %v", err)
		}
		got, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("%s: Read: %v", tt.name, err)
		}
		if got.Text() != "s3cret" || got.Concealed != tt.want {
			t.Errorf("%s: Read = %q, concealed %q; want s3cret, %q", tt.name, got.Text(), got.Concealed, tt.want)
		}
	}
}
