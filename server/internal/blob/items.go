package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
)

// The store's blobs (SPEC §4.6) live beside the hour buckets, under their own
// name:
//
//	<root>/store/<group_id>/<item_id>.bin      disk
//	<prefix>/store/<group_id>/<item_id>.bin    S3
//
// "store" is not an hour bucket, so the bucket sweep, its listing and
// `tpp gc --verify` pass over it: store items do not expire, and nothing of
// SPEC §4.5 applies to them. One directory or prefix per group is what makes
// emptying a group's store one prefix delete rather than one per item.

// ItemsDir is the name of the store's directory under the root or prefix.
const ItemsDir = "store"

// Items stores the store's blobs. Like Backend it creates and never
// replaces, and enforces sizes at the reader.
type Items interface {
	// PutItem stores exactly size bytes from r as an item of a group. A body
	// of any other length fails with ErrTooLarge or ErrIncomplete and leaves
	// nothing behind; an item that already has a blob fails with ErrExists.
	PutItem(ctx context.Context, groupID, itemID string, size int64, r io.Reader) error

	// BeginItemUpload starts an item's blob that arrives in chunks. Nothing
	// is visible under the item until Complete.
	BeginItemUpload(ctx context.Context, groupID, itemID string, size int64) (Upload, error)

	// GetItemRange opens length bytes of an item's blob from offset. A
	// missing blob is ErrNotFound.
	GetItemRange(ctx context.Context, groupID, itemID string, offset, length int64) (io.ReadCloser, error)

	// DeleteItem removes an item's blob. A missing one is not an error.
	DeleteItem(ctx context.Context, groupID, itemID string) error
}

var (
	_ Items = (*Disk)(nil)
	_ Items = (*S3)(nil)
)

// itemName is an item's path relative to the root or prefix, after checking
// that neither id can name anything outside the store's directory.
func itemName(groupID, itemID string) (string, error) {
	if err := validEntryID(groupID); err != nil {
		return "", fmt.Errorf("%w: group id", ErrInvalidRef)
	}
	if err := validEntryID(itemID); err != nil {
		return "", fmt.Errorf("%w: item id", ErrInvalidRef)
	}
	return ItemsDir + "/" + groupID + "/" + itemID + ".bin", nil
}

// itemPath is an item's absolute path on disk.
func (d *Disk) itemPath(groupID, itemID string) (string, error) {
	name, err := itemName(groupID, itemID)
	if err != nil {
		return "", err
	}
	local := filepath.FromSlash(name)
	if !filepath.IsLocal(local) {
		return "", fmt.Errorf("%w: %q", ErrInvalidRef, name)
	}
	return filepath.Join(d.root, local), nil
}

// checkFree refuses with ErrStorageFull while the volume holding dir has less
// than the floor plus need free.
func (d *Disk) checkFree(dir, what string, need int64) error {
	if d.minFree <= 0 {
		return nil
	}
	if free, ok := d.freeBytes(dir); ok && free < d.minFree+need {
		return fmt.Errorf("%s: %w: %d bytes free, floor is %d", what, ErrStorageFull, free, d.minFree)
	}
	return nil
}

// PutItem implements Items. Like Put it creates with O_EXCL: the item id
// comes from the writing client, and replacing a file would let one client
// destroy another's.
func (d *Disk) PutItem(ctx context.Context, groupID, itemID string, size int64, r io.Reader) error {
	path, err := d.itemPath(groupID, itemID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("put store item %s: %w", itemID, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create store directory of group %s: %w", groupID, err)
	}
	if err := d.checkFree(dir, "put store item "+itemID, size); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create store item %s: %w", itemID, ErrExists)
	}
	if err != nil {
		return fmt.Errorf("create store item %s: %w", itemID, err)
	}
	n, copyErr := io.Copy(f, io.LimitReader(r, size+1))
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		_ = os.Remove(path)
		return fmt.Errorf("write store item %s: %w", itemID, copyErr)
	case closeErr != nil:
		_ = os.Remove(path)
		return fmt.Errorf("write store item %s: %w", itemID, closeErr)
	case n > size:
		_ = os.Remove(path)
		return fmt.Errorf("write store item %s: %w", itemID, ErrTooLarge)
	case n < size:
		_ = os.Remove(path)
		return fmt.Errorf("write store item %s: %w: %d of %d bytes", itemID, ErrIncomplete, n, size)
	}
	return nil
}

// BeginItemUpload implements Items. The chunks go to the same staging
// directory as an entry's, and Complete links the file into the group's
// store directory.
func (d *Disk) BeginItemUpload(_ context.Context, groupID, itemID string, size int64) (Upload, error) {
	path, err := d.itemPath(groupID, itemID)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(d.root, uploadsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create upload directory: %w", err)
	}
	if err := d.checkFree(dir, "begin upload of store item "+itemID, size); err != nil {
		return nil, err
	}
	name, _ := itemName(groupID, itemID)
	return d.beginStaged(itemID, path, name, size)
}

// GetItemRange implements Items.
func (d *Disk) GetItemRange(ctx context.Context, groupID, itemID string, offset, length int64) (io.ReadCloser, error) {
	path, err := d.itemPath(groupID, itemID)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open store item %s: %w", itemID, err)
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open store item %s: %w", itemID, err)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("seek store item %s: %w", itemID, err)
	}
	return readCloser{Reader: io.LimitReader(f, length), Closer: f}, nil
}

// DeleteItem implements Items.
func (d *Disk) DeleteItem(_ context.Context, groupID, itemID string) error {
	path, err := d.itemPath(groupID, itemID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete store item %s: %w", itemID, err)
	}
	return nil
}

// itemKey is an item's object key.
func (s *S3) itemKey(groupID, itemID string) (string, error) {
	name, err := itemName(groupID, itemID)
	if err != nil {
		return "", err
	}
	return s.prefix + name, nil
}

// PutItem implements Items with one create-only PUT, as Put does: the body
// is read into memory up to its declared size first, which a single-frame
// save bounds.
func (s *S3) PutItem(ctx context.Context, groupID, itemID string, size int64, r io.Reader) error {
	key, err := s.itemKey(groupID, itemID)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(r, size+1))
	if err != nil {
		return fmt.Errorf("read store item %s: %w", itemID, err)
	}
	switch n := int64(len(body)); {
	case n > size:
		return fmt.Errorf("write store item %s: %w", itemID, ErrTooLarge)
	case n < size:
		return fmt.Errorf("write store item %s: %w: %d of %d bytes", itemID, ErrIncomplete, n, size)
	}
	opts := minio.PutObjectOptions{
		ContentType: "application/octet-stream",
		PartSize:    uint64(max(len(body), 5<<20)), //nolint:gosec // non-negative
	}
	opts.SetMatchETagExcept("*")
	if _, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(body), int64(len(body)), opts); err != nil {
		resp := minio.ToErrorResponse(err)
		if resp.StatusCode == http.StatusPreconditionFailed || resp.StatusCode == http.StatusConflict {
			return fmt.Errorf("create store item %s: %w", itemID, ErrExists)
		}
		return fmt.Errorf("write store item %s: %w", itemID, err)
	}
	return nil
}

// BeginItemUpload implements Items with a multipart upload, completed
// create-only like an entry's.
func (s *S3) BeginItemUpload(ctx context.Context, groupID, itemID string, size int64) (Upload, error) {
	key, err := s.itemKey(groupID, itemID)
	if err != nil {
		return nil, err
	}
	id, err := s.core.NewMultipartUpload(ctx, s.bucket, key, minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		return nil, fmt.Errorf("begin upload of store item %s: %w", itemID, err)
	}
	name := strings.TrimPrefix(key, s.prefix)
	return &s3Upload{s: s, entryID: itemID, key: key, ref: name, uploadID: id, size: size}, nil
}

// GetItemRange implements Items.
func (s *S3) GetItemRange(ctx context.Context, groupID, itemID string, offset, length int64) (io.ReadCloser, error) {
	key, err := s.itemKey(groupID, itemID)
	if err != nil {
		return nil, err
	}
	if length <= 0 {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	opts := minio.GetObjectOptions{}
	if err := opts.SetRange(offset, offset+length-1); err != nil {
		return nil, fmt.Errorf("open store item %s: %w", itemID, err)
	}
	rc, _, _, err := s.core.GetObject(ctx, s.bucket, key, opts)
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("open store item %s: %w", itemID, err)
	}
	return rc, nil
}

// DeleteItem implements Items.
func (s *S3) DeleteItem(ctx context.Context, groupID, itemID string) error {
	key, err := s.itemKey(groupID, itemID)
	if err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete store item %s: %w", itemID, err)
	}
	return nil
}
