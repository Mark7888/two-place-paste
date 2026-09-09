package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Disk is the MVP backend: blobs on the local filesystem, laid out as
// <root>/<YYYYMMDDHH>/<entry_id>.bin where the directory is the UTC hour in
// which the blob expires (SPEC §4.3, §4.5).
type Disk struct {
	root    string
	maxSize int64

	// readDir and removeAll are the only filesystem operations the sweep
	// performs. They are fields so that a test can count them and prove the
	// sweep costs one listing plus one removal per expired bucket, whatever
	// the blob count (SPEC §4.5).
	readDir   func(name string) ([]fs.DirEntry, error)
	removeAll func(path string) error
}

var _ Backend = (*Disk)(nil)

// NewDisk returns a disk backend rooted at root, rejecting any body larger
// than maxSize bytes.
func NewDisk(root string, maxSize int64) *Disk {
	return &Disk{
		root:      root,
		maxSize:   maxSize,
		readDir:   os.ReadDir,
		removeAll: os.RemoveAll,
	}
}

// Put implements Backend.
//
// The blob is written before the Redis entry that references it (SPEC §4.5,
// "write ordering"): a crash in between leaves an orphan in a bucket that is
// deleted within 24 hours regardless, so there is no recovery logic to get
// wrong.
func (d *Disk) Put(ctx context.Context, entryID string, expiresAt time.Time, r io.Reader) (string, error) {
	if err := validEntryID(entryID); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("put blob for entry %s: %w", entryID, err)
	}

	bucket := Bucket(expiresAt)
	dir := filepath.Join(d.root, bucket)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create blob bucket %s: %w", bucket, err)
	}

	// O_EXCL rather than O_TRUNC: the filename is the entry id, and the id
	// comes from the writing client (/spec/crypto.md §5.3), so replacing an
	// existing file would let one client destroy another's ciphertext. The
	// entry record is refused for a duplicate id too; this is the half that
	// protects the bytes.
	path := filepath.Join(dir, entryID+".bin")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("create blob for entry %s: %w", entryID, ErrExists)
	}
	if err != nil {
		return "", fmt.Errorf("create blob for entry %s: %w", entryID, err)
	}

	// The cap is enforced at the reader, never after buffering (SPEC §4.3):
	// one byte past the limit is read solely to tell "exactly at the cap" from
	// "over it", and the partial file is removed before returning.
	n, copyErr := io.Copy(f, io.LimitReader(r, d.maxSize+1))
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		_ = os.Remove(path)
		return "", fmt.Errorf("write blob for entry %s: %w", entryID, copyErr)
	case closeErr != nil:
		_ = os.Remove(path)
		return "", fmt.Errorf("write blob for entry %s: %w", entryID, closeErr)
	case n > d.maxSize:
		_ = os.Remove(path)
		return "", fmt.Errorf("write blob for entry %s: %w", entryID, ErrTooLarge)
	}

	return bucket + "/" + entryID + ".bin", nil
}

// Get implements Backend.
func (d *Disk) Get(ctx context.Context, ref string) (io.ReadCloser, error) {
	path, err := d.path(ref)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open blob %s: %w", ref, err)
	}

	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open blob %s: %w", ref, err)
	}
	return f, nil
}

// Delete implements Backend.
func (d *Disk) Delete(_ context.Context, ref string) error {
	path, err := d.path(ref)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete blob %s: %w", ref, err)
	}
	return nil
}

// Sweep implements Backend: list the immediate child directories of the root,
// parse each as an hour bucket, and RemoveAll the ones whose hour has fully
// passed (SPEC §4.5).
//
// It issues no Redis query and stats no individual file, so its cost is one
// directory listing plus one removal per expired bucket — roughly 25 names
// regardless of how many blobs are stored.
func (d *Disk) Sweep(ctx context.Context, now time.Time) error {
	entries, err := d.readDir(d.root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // nothing written yet
	}
	if err != nil {
		return fmt.Errorf("list blob buckets: %w", err)
	}

	var errs []error
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("sweep blob buckets: %w", err)
		}
		if !e.IsDir() || !Expired(e.Name(), now) {
			continue
		}
		if err := d.removeAll(filepath.Join(d.root, e.Name())); err != nil {
			errs = append(errs, fmt.Errorf("remove expired bucket %s: %w", e.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// ListRefs implements Lister: every reference currently on disk. It is used
// only by `tpp gc --verify`, never by the sweep (SPEC §4.5).
func (d *Disk) ListRefs(ctx context.Context) ([]string, error) {
	buckets, err := d.readDir(d.root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list blob buckets: %w", err)
	}

	var refs []string
	for _, b := range buckets {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("list blob references: %w", err)
		}
		if !b.IsDir() {
			continue
		}
		if _, ok := BucketEnd(b.Name()); !ok {
			continue
		}
		files, err := d.readDir(filepath.Join(d.root, b.Name()))
		if err != nil {
			return nil, fmt.Errorf("list blob bucket %s: %w", b.Name(), err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".bin") {
				continue
			}
			refs = append(refs, b.Name()+"/"+f.Name())
		}
	}
	return refs, nil
}

// path resolves a reference to an absolute filesystem path, rejecting anything
// that is not the exact <bucket>/<entry_id>.bin layout this package writes.
func (d *Disk) path(ref string) (string, error) {
	bucket, file, ok := strings.Cut(ref, "/")
	if !ok || strings.Contains(file, "/") {
		return "", fmt.Errorf("%w: %q", ErrInvalidRef, ref)
	}
	if _, ok := BucketEnd(bucket); !ok {
		return "", fmt.Errorf("%w: %q", ErrInvalidRef, ref)
	}
	entryID, ok := strings.CutSuffix(file, ".bin")
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrInvalidRef, ref)
	}
	if err := validEntryID(entryID); err != nil {
		return "", err
	}
	return filepath.Join(d.root, bucket, file), nil
}

// validEntryID rejects anything that could escape the blob root. Server-issued
// identifiers are URL-safe base64, so this only ever fires on a bug or an
// attack.
func validEntryID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty entry id", ErrInvalidRef)
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return fmt.Errorf("%w: entry id %q", ErrInvalidRef, id)
		}
	}
	return nil
}
