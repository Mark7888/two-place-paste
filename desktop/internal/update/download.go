package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// maxPayloadBytes bounds a download whose manifest gave no size. The builds
// are tens of megabytes; this is a backstop against an endless response.
const maxPayloadBytes = 512 << 20

// ErrChecksum reports a download whose bytes are not the ones the manifest
// vouches for. It is deleted, never installed.
var ErrChecksum = errors.New("update: the download does not match its manifest")

// download fetches url into a new file in dir and checks it against want. On
// any failure nothing is left behind. The returned file is the caller's to
// remove.
func download(ctx context.Context, client *http.Client, url, dir string, want File) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("update: build the request for %s: %w", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("update: download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("update: download %s: %s", url, resp.Status)
	}
	return saveVerified(resp.Body, dir, want)
}

// saveVerified writes r to a new file in dir, checking its size and SHA-256
// against want as it goes.
func saveVerified(r io.Reader, dir string, want File) (path string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("update: create %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, "download-*-"+filepath.Base(want.Name))
	if err != nil {
		return "", fmt.Errorf("update: create a file in %s: %w", dir, err)
	}
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()

	limit := int64(maxPayloadBytes)
	if want.Size > 0 {
		limit = want.Size
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, limit+1))
	if err != nil {
		return "", fmt.Errorf("update: write %s: %w", f.Name(), err)
	}
	switch {
	case n > limit:
		return "", fmt.Errorf("%w: %s is larger than %d bytes", ErrChecksum, want.Name, limit)
	case want.Size > 0 && n != want.Size:
		return "", fmt.Errorf("%w: %s is %d bytes, want %d", ErrChecksum, want.Name, n, want.Size)
	case hex.EncodeToString(h.Sum(nil)) != want.SHA256:
		return "", fmt.Errorf("%w: %s has the wrong SHA-256", ErrChecksum, want.Name)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("update: close %s: %w", f.Name(), err)
	}
	return f.Name(), nil
}
