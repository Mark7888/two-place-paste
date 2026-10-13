package clipboard

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
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
