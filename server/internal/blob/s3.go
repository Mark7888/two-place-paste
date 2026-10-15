package blob

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// PathStyle chooses how bucket names are put into request URLs.
type PathStyle string

const (
	// PathStyleAuto uses path-style URLs for a custom endpoint (MinIO and
	// most self-hosted stores) and virtual-host style for AWS.
	PathStyleAuto PathStyle = "auto"

	// PathStylePath always puts the bucket in the path: host/bucket/key.
	PathStylePath PathStyle = "path"

	// PathStyleVirtual always puts the bucket in the host: bucket.host/key.
	PathStyleVirtual PathStyle = "virtual"
)

// awsEndpoint is used when no endpoint is configured.
const awsEndpoint = "s3.amazonaws.com"

// S3Options configures the object-storage backend.
type S3Options struct {
	// Endpoint is host[:port] of the S3 API. Empty means AWS.
	Endpoint string

	// UseSSL selects https. Only an endpoint on a private network, such as
	// the MinIO service of the compose file, should run without it.
	UseSSL bool

	Region string
	Bucket string

	// Prefix is prepended to every key, so that one bucket can hold more
	// than this deployment. Leading and trailing slashes are ignored.
	Prefix string

	// AccessKeyID and SecretAccessKey are static credentials. When both are
	// empty the standard AWS chain is used instead: the AWS_* environment
	// variables, then an instance or task role.
	AccessKeyID     string
	SecretAccessKey string

	PathStyle PathStyle

	// MaxSize is the per-entry cap, enforced at the reader like the disk
	// backend's.
	MaxSize int64

	// Transport overrides the HTTP transport; tests use it. Nil is the
	// default transport, which honours the proxy environment.
	Transport http.RoundTripper
}

// S3 stores blobs in an S3-compatible object store — MinIO, AWS S3 or any
// other — under <prefix>/<YYYYMMDDHH>/<entry_id>.bin, the same layout as the
// disk backend (SPEC §4.3, §4.5).
//
// Every request goes through the relay: clients never talk to the object
// store, so it can stay on a private network.
type S3 struct {
	client  *minio.Client
	bucket  string
	prefix  string // "" or "<prefix>/"
	maxSize int64
	desc    string
}

var (
	_ Backend = (*S3)(nil)
	_ Lister  = (*S3)(nil)
)

// NewS3 returns an S3 backend. It does not contact the store; Check does.
func NewS3(opts S3Options) (*S3, error) {
	if opts.Bucket == "" {
		return nil, errors.New("blob: s3 bucket is required")
	}
	endpoint := opts.Endpoint
	custom := endpoint != ""
	if !custom {
		endpoint = awsEndpoint
	}

	var creds *credentials.Credentials
	switch {
	case opts.AccessKeyID != "" || opts.SecretAccessKey != "":
		creds = credentials.NewStaticV4(opts.AccessKeyID, opts.SecretAccessKey, "")
	default:
		creds = credentials.NewChainCredentials([]credentials.Provider{
			&credentials.EnvAWS{},
			&credentials.IAM{},
		})
	}

	lookup := minio.BucketLookupAuto
	switch opts.PathStyle {
	case PathStylePath:
		lookup = minio.BucketLookupPath
	case PathStyleVirtual:
		lookup = minio.BucketLookupDNS
	case PathStyleAuto, "":
		if custom {
			lookup = minio.BucketLookupPath
		}
	default:
		return nil, fmt.Errorf("blob: unknown s3 path style %q", opts.PathStyle)
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:        creds,
		Secure:       opts.UseSSL,
		Region:       opts.Region,
		BucketLookup: lookup,
		Transport:    opts.Transport,
	})
	if err != nil {
		return nil, fmt.Errorf("blob: configure s3 client for %s: %w", endpoint, err)
	}

	prefix := strings.Trim(opts.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	scheme := "https"
	if !opts.UseSSL {
		scheme = "http"
	}
	return &S3{
		client:  client,
		bucket:  opts.Bucket,
		prefix:  prefix,
		maxSize: opts.MaxSize,
		desc:    fmt.Sprintf("%s://%s/%s/%s", scheme, endpoint, opts.Bucket, prefix),
	}, nil
}

// String names the store for logs and error messages. It holds no secret.
func (s *S3) String() string { return s.desc }

// Check verifies, before the server takes traffic, that the bucket is there
// and that the credentials can write, read and delete in it — so a typo in
// the configuration fails at startup with a reason, not at the first large
// paste. With create, a missing bucket is created instead of reported.
func (s *S3) Check(ctx context.Context, create bool) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("reach s3 bucket %s: %w", s, err)
	}
	if !exists {
		if !create {
			return fmt.Errorf("s3 bucket %q does not exist at %s; create it, or set TPP_S3_CREATE_BUCKET=true", s.bucket, s)
		}
		err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{})
		switch code := minio.ToErrorResponse(err).Code; {
		case err == nil, code == "BucketAlreadyOwnedByYou":
			// Created, or created by a concurrent start of this deployment.
		case code == "BucketAlreadyExists":
			return fmt.Errorf("s3 bucket name %q is taken by another account; choose another TPP_S3_BUCKET", s.bucket)
		default:
			return fmt.Errorf("create s3 bucket %q: %w", s.bucket, err)
		}
	}

	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Errorf("s3 probe: %w", err)
	}
	key := s.prefix + ".tpp-probe/" + hex.EncodeToString(b[:])
	payload := []byte("tpp storage probe")
	if _, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(payload), int64(len(payload)),
		minio.PutObjectOptions{ContentType: "application/octet-stream"}); err != nil {
		return fmt.Errorf("s3 probe: write to %s: %w", s, err)
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err == nil {
		var got []byte
		got, err = io.ReadAll(obj)
		_ = obj.Close()
		if err == nil && !bytes.Equal(got, payload) {
			err = errors.New("read back different bytes than were written")
		}
	}
	if err != nil {
		_ = s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
		return fmt.Errorf("s3 probe: read from %s: %w", s, err)
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("s3 probe: delete from %s: %w", s, err)
	}
	return nil
}

func (s *S3) key(bucket, entryID string) string { return s.prefix + refFor(bucket, entryID) }

// Put implements Backend.
//
// The body is read into memory, up to the cap, before anything is sent: the
// object store needs a length up front to make a single create-only PUT, and
// an entry is at most the per-entry cap. The write carries If-None-Match: *,
// so an id that already has a blob is refused by the store itself — the same
// create-never-replace rule the disk backend gets from O_EXCL.
func (s *S3) Put(ctx context.Context, entryID string, expiresAt time.Time, r io.Reader) (string, error) {
	if err := validEntryID(entryID); err != nil {
		return "", err
	}
	body, err := io.ReadAll(io.LimitReader(r, s.maxSize+1))
	if err != nil {
		return "", fmt.Errorf("read blob for entry %s: %w", entryID, err)
	}
	if int64(len(body)) > s.maxSize {
		return "", fmt.Errorf("write blob for entry %s: %w", entryID, ErrTooLarge)
	}

	bucket := Bucket(expiresAt)
	opts := minio.PutObjectOptions{
		ContentType: "application/octet-stream",
		// One part for the whole body: a conditional create is a single PUT.
		// 5 MiB is S3's floor for a part size.
		PartSize: uint64(max(len(body), 5<<20)), //nolint:gosec // non-negative
	}
	opts.SetMatchETagExcept("*")

	_, err = s.client.PutObject(ctx, s.bucket, s.key(bucket, entryID), bytes.NewReader(body), int64(len(body)), opts)
	if err != nil {
		resp := minio.ToErrorResponse(err)
		// 412: the object exists. 409: another create of the same key is in
		// flight. Either way this id's blob is someone else's.
		if resp.StatusCode == http.StatusPreconditionFailed || resp.StatusCode == http.StatusConflict {
			return "", fmt.Errorf("create blob for entry %s: %w", entryID, ErrExists)
		}
		return "", fmt.Errorf("write blob for entry %s: %w", entryID, err)
	}
	return refFor(bucket, entryID), nil
}

// Get implements Backend.
func (s *S3) Get(ctx context.Context, ref string) (io.ReadCloser, error) {
	bucket, entryID, err := parseRef(ref)
	if err != nil {
		return nil, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, s.key(bucket, entryID), minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("open blob %s: %w", ref, err)
	}
	// GetObject is lazy; Stat is what learns whether the object is there.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("open blob %s: %w", ref, err)
	}
	return obj, nil
}

// Delete implements Backend. Deleting a missing object is not an error, in
// S3 or here.
func (s *S3) Delete(ctx context.Context, ref string) error {
	bucket, entryID, err := parseRef(ref)
	if err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, s.key(bucket, entryID), minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete blob %s: %w", ref, err)
	}
	return nil
}

// Sweep implements Backend: list the hour prefixes and delete every object
// under the ones whose hour has fully passed — the disk backend's algorithm,
// with a prefix for a directory (SPEC §4.5).
//
// It deletes actively rather than leaving it to a bucket lifecycle rule:
// lifecycle rules work in whole days, and the storage limits would count dead
// objects for up to a day. A lifecycle rule is still a good backstop for a
// relay that stays down.
func (s *S3) Sweep(ctx context.Context, now time.Time) error {
	buckets, err := s.hourPrefixes(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range buckets {
		if !Expired(b, now) {
			continue
		}
		if err := s.removePrefix(ctx, s.prefix+b+"/"); err != nil {
			errs = append(errs, fmt.Errorf("remove expired bucket %s: %w", b, err))
		}
	}
	return errors.Join(errs...)
}

// ListRefs implements Lister. It backs `tpp gc --verify` only.
func (s *S3) ListRefs(ctx context.Context) ([]string, error) {
	buckets, err := s.hourPrefixes(ctx)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, b := range buckets {
		for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.prefix + b + "/", Recursive: true}) {
			if obj.Err != nil {
				return nil, fmt.Errorf("list blob bucket %s: %w", b, obj.Err)
			}
			ref := strings.TrimPrefix(obj.Key, s.prefix)
			if _, _, err := parseRef(ref); err == nil {
				refs = append(refs, ref)
			}
		}
	}
	return refs, nil
}

// hourPrefixes lists the hour buckets directly under the prefix. Anything else
// there — the startup probe's keys, another deployment's files — is ignored.
func (s *S3) hourPrefixes(ctx context.Context) ([]string, error) {
	var buckets []string
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.prefix}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("list blob buckets: %w", obj.Err)
		}
		name, ok := strings.CutSuffix(strings.TrimPrefix(obj.Key, s.prefix), "/")
		if !ok {
			continue // an object, not a prefix
		}
		if _, ok := BucketEnd(name); ok {
			buckets = append(buckets, name)
		}
	}
	return buckets, nil
}

// removePrefix deletes every object under prefix, in the store's batches.
func (s *S3) removePrefix(ctx context.Context, prefix string) error {
	objects := make(chan minio.ObjectInfo)
	listErr := make(chan error, 1)
	go func() {
		defer close(objects)
		for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
			if obj.Err != nil {
				listErr <- obj.Err
				return
			}
			select {
			case objects <- obj:
			case <-ctx.Done():
				return
			}
		}
	}()

	var errs []error
	for e := range s.client.RemoveObjects(ctx, s.bucket, objects, minio.RemoveObjectsOptions{}) {
		errs = append(errs, fmt.Errorf("delete %s: %w", e.ObjectName, e.Err))
	}
	select {
	case err := <-listErr:
		errs = append(errs, fmt.Errorf("list %s: %w", prefix, err))
	default:
	}
	return errors.Join(errs...)
}
