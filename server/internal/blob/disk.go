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

	// minFree is the free-space floor below which Put refuses to write.
	// Zero disables the check. freeBytes reports the space available on the
	// volume holding root, and whether it could be determined; it is a field
	// so that a test can fill the disk without filling the disk.
	minFree   int64
	freeBytes func(path string) (int64, bool)

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
		freeBytes: freeBytes,
	}
}

// WithMinFreeBytes makes Put refuse with ErrStorageFull while the volume has
// less than n bytes free, plus the room the new blob could take. It returns d
// so it can follow NewDisk.
//
// This guards the host, not the quota: the storage limits are bookkeeping,
// and they cannot see a disk that something else is filling. On a platform
// where free space cannot be read the check is skipped.
func (d *Disk) WithMinFreeBytes(n int64) *Disk {
	d.minFree = n
	return d
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
	if d.minFree > 0 {
		// The blob's own size is not known until it is read, so the room it
		// could take — the per-entry cap — is what is set aside.
		if free, ok := d.freeBytes(dir); ok && free < d.minFree+d.maxSize {
			return "", fmt.Errorf("put blob for entry %s: %w: %d bytes free, floor is %d", entryID, ErrStorageFull, free, d.minFree)
		}
	}

	// O_EXCL rather than O_TRUNC: the filename is the entry id, and the id
	// comes from the writing client (/spec/crypto.md §5.3), so replacing an
	// existing file would let one client destroy another's ciphertext. The
	// entry record is refused for a duplicate id too; this is the half that
	// protects the bytes.
	path, err := d.blobPath(bucket, entryID)
	if err != nil {
		return "", err
	}
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

	return refFor(bucket, entryID), nil
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
		if e.IsDir() && e.Name() == uploadsDir {
			if err := d.sweepUploads(now); err != nil {
				errs = append(errs, err)
			}
			continue
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

// uploadsDir holds chunked uploads in progress. Its name is not an hour
// bucket, so the bucket sweep and the listing pass over it.
const uploadsDir = ".uploads"

// sweepUploads removes staging files that no session can still be writing:
// those abandoned by a crash. It is one more listing, and only when an upload
// has ever been made.
func (d *Disk) sweepUploads(now time.Time) error {
	dir := filepath.Join(d.root, uploadsDir)
	files, err := d.readDir(dir)
	if err != nil {
		return fmt.Errorf("list unfinished uploads: %w", err)
	}
	var errs []error
	for _, f := range files {
		info, err := f.Info()
		if err != nil || now.Sub(info.ModTime()) < StaleUploadAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, f.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove stale upload %s: %w", f.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// BeginUpload implements Backend. The chunks are appended to a staging file
// under <root>/.uploads, and Complete links it into its hour bucket.
func (d *Disk) BeginUpload(_ context.Context, entryID string, expiresAt time.Time, size int64) (Upload, error) {
	if err := validEntryID(entryID); err != nil {
		return nil, err
	}
	if size > d.maxSize {
		return nil, fmt.Errorf("begin upload for entry %s: %w", entryID, ErrTooLarge)
	}
	dir := filepath.Join(d.root, uploadsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create upload directory: %w", err)
	}
	if d.minFree > 0 {
		if free, ok := d.freeBytes(dir); ok && free < d.minFree+size {
			return nil, fmt.Errorf("begin upload for entry %s: %w: %d bytes free, floor is %d", entryID, ErrStorageFull, free, d.minFree)
		}
	}
	f, err := os.CreateTemp(dir, entryID+"-*.part")
	if err != nil {
		return nil, fmt.Errorf("begin upload for entry %s: %w", entryID, err)
	}
	return &diskUpload{d: d, entryID: entryID, bucket: Bucket(expiresAt), f: f, size: size}, nil
}

type diskUpload struct {
	d       *Disk
	entryID string
	bucket  string
	f       *os.File
	size    int64
	written int64
}

func (u *diskUpload) Write(ctx context.Context, chunk []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("write upload chunk for entry %s: %w", u.entryID, err)
	}
	if u.written+int64(len(chunk)) > u.size {
		return fmt.Errorf("write upload chunk for entry %s: %w", u.entryID, ErrTooLarge)
	}
	n, err := u.f.Write(chunk)
	u.written += int64(n)
	if err != nil {
		return fmt.Errorf("write upload chunk for entry %s: %w", u.entryID, err)
	}
	return nil
}

// Complete implements Upload. os.Link is the create-only step: it fails
// rather than replace an existing file, which is the guarantee Put gets from
// O_EXCL.
func (u *diskUpload) Complete(context.Context) (ref string, err error) {
	if u.written != u.size {
		return "", fmt.Errorf("complete upload for entry %s: %w: %d of %d bytes", u.entryID, ErrIncomplete, u.written, u.size)
	}
	defer func() {
		// Linked or not, the staging file has served its purpose.
		_ = os.Remove(u.f.Name())
	}()
	if err := u.f.Close(); err != nil {
		return "", fmt.Errorf("complete upload for entry %s: %w", u.entryID, err)
	}
	dst, err := u.d.blobPath(u.bucket, u.entryID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", fmt.Errorf("create blob bucket %s: %w", u.bucket, err)
	}
	err = os.Link(u.f.Name(), dst)
	if errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("complete upload for entry %s: %w", u.entryID, ErrExists)
	}
	if err != nil {
		return "", fmt.Errorf("complete upload for entry %s: %w", u.entryID, err)
	}
	return refFor(u.bucket, u.entryID), nil
}

func (u *diskUpload) Abort(context.Context) error {
	_ = u.f.Close()
	if err := os.Remove(u.f.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("abort upload for entry %s: %w", u.entryID, err)
	}
	return nil
}

// GetRange implements Backend.
func (d *Disk) GetRange(ctx context.Context, ref string, offset, length int64) (io.ReadCloser, error) {
	rc, err := d.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	f, ok := rc.(*os.File)
	if !ok {
		_ = rc.Close()
		return nil, fmt.Errorf("open blob %s: unexpected reader %T", ref, rc)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("seek blob %s: %w", ref, err)
	}
	return readCloser{Reader: io.LimitReader(f, length), Closer: f}, nil
}

// readCloser pairs a limited reader with the file it reads.
type readCloser struct {
	io.Reader
	io.Closer
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
	bucket, entryID, err := parseRef(ref)
	if err != nil {
		return "", err
	}
	return d.blobPath(bucket, entryID)
}

// blobPath is the one place a blob's filesystem path is built. Its parts are
// already validated by the callers (validEntryID, BucketEnd); this repeats the
// check on the joined name so that no path outside the root is ever opened,
// whatever a future caller forgets.
func (d *Disk) blobPath(bucket, entryID string) (string, error) {
	name := filepath.Join(bucket, entryID+".bin")
	if strings.Contains(name, "..") || !filepath.IsLocal(name) {
		return "", fmt.Errorf("%w: %q", ErrInvalidRef, name)
	}
	return filepath.Join(d.root, name), nil
}

// parseRef splits a reference into its hour bucket and entry id, rejecting
// anything that is not the exact <bucket>/<entry_id>.bin layout every backend
// writes. A reference comes back from Redis, so this is the guard that keeps a
// corrupted or forged one from naming a path or key outside the layout.
func parseRef(ref string) (bucket, entryID string, err error) {
	bucket, file, ok := strings.Cut(ref, "/")
	if !ok || strings.Contains(file, "/") {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidRef, ref)
	}
	if _, ok := BucketEnd(bucket); !ok {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidRef, ref)
	}
	entryID, ok = strings.CutSuffix(file, ".bin")
	if !ok {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidRef, ref)
	}
	if err := validEntryID(entryID); err != nil {
		return "", "", err
	}
	return bucket, entryID, nil
}

// refFor is the reference of a blob: the inverse of parseRef.
func refFor(bucket, entryID string) string { return bucket + "/" + entryID + ".bin" }

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
