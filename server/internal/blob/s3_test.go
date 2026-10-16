package blob_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
)

// TestS3Check covers the startup probe: it is what turns a typo in the
// configuration into a refusal to start with a reason.
func TestS3Check(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	opts := s3Options(t, 1024)
	s3Reachable(t, opts)

	var b [6]byte
	_, _ = rand.Read(b[:])
	missing := opts
	missing.Bucket = "tpp-missing-" + hex.EncodeToString(b[:])
	backend, err := blob.NewS3(missing)
	if err != nil {
		t.Fatalf("NewS3() error = %v", err)
	}
	if err := backend.Check(ctx, false); err == nil || !strings.Contains(err.Error(), "TPP_S3_CREATE_BUCKET") {
		t.Errorf("Check(missing bucket, no create) error = %v, want one naming TPP_S3_CREATE_BUCKET", err)
	}
	if err := backend.Check(ctx, true); err != nil {
		t.Fatalf("Check(missing bucket, create) error = %v", err)
	}
	t.Cleanup(func() { removeBucket(t, missing) })
	if err := backend.Check(ctx, false); err != nil {
		t.Errorf("Check(existing bucket) error = %v", err)
	}

	// The probe cleans up after itself.
	if refs, err := backend.ListRefs(ctx); err != nil || len(refs) != 0 {
		t.Errorf("ListRefs() after Check = %v, %v; want nothing", refs, err)
	}

	wrong := opts
	wrong.SecretAccessKey = "not-the-secret"
	backend, err = blob.NewS3(wrong)
	if err != nil {
		t.Fatalf("NewS3() error = %v", err)
	}
	if err := backend.Check(ctx, true); err == nil {
		t.Errorf("Check(wrong secret) error = nil, want a refusal")
	}
}

// TestS3SweepLeavesOtherKeysAlone: a shared bucket can hold anything under the
// prefix's parent, and things under the prefix that are not hour buckets; the
// sweep removes expired hour buckets and nothing else.
func TestS3SweepLeavesOtherKeysAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := newTestS3(t, 1024)
	opts := s3Options(t, 1024) // only for the endpoint and credentials

	client, err := minio.New(opts.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(opts.AccessKeyID, opts.SecretAccessKey, ""),
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		t.Fatalf("minio.New() error = %v", err)
	}
	prefix := strings.TrimSuffix(strings.TrimPrefix(backend.String(), "http://"+opts.Endpoint+"/"+s3TestBucket+"/"), "/")
	foreign := []string{
		prefix + "/notes.txt",
		prefix + "/1999010100-not-a-bucket/x.bin",
	}
	for _, key := range foreign {
		if _, err := client.PutObject(ctx, s3TestBucket, key, bytes.NewReader([]byte("x")), 1, minio.PutObjectOptions{}); err != nil {
			t.Fatalf("PutObject(%s) error = %v", key, err)
		}
	}

	if err := backend.Sweep(ctx, time.Now()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	for _, key := range foreign {
		if _, err := client.StatObject(ctx, s3TestBucket, key, minio.StatObjectOptions{}); err != nil {
			t.Errorf("StatObject(%s) after sweep error = %v, want it kept", key, err)
		}
	}
}

func removeBucket(t *testing.T, opts blob.S3Options) {
	t.Helper()
	client, err := minio.New(opts.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(opts.AccessKeyID, opts.SecretAccessKey, ""),
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return
	}
	ctx := context.Background()
	for obj := range client.ListObjects(ctx, opts.Bucket, minio.ListObjectsOptions{Recursive: true}) {
		_ = client.RemoveObject(ctx, opts.Bucket, obj.Key, minio.RemoveObjectOptions{})
	}
	_ = client.RemoveBucket(ctx, opts.Bucket)
}
