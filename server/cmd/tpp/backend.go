package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/config"
)

// backendCheckTimeout bounds the startup probe of an object store, so that an
// unreachable endpoint fails the start with a reason instead of hanging it.
const backendCheckTimeout = 30 * time.Second

// newBlobBackend builds the configured blob backend (SPEC §4.3). It does not
// contact anything; checkBlobBackend does.
func newBlobBackend(cfg *config.Config) (blob.Backend, error) {
	switch cfg.Blob.Backend {
	case config.BlobBackendDisk:
		return blob.NewDisk(cfg.Blob.Root, cfg.Entry.MaxBytes).WithMinFreeBytes(cfg.Blob.MinFreeBytes), nil
	case config.BlobBackendS3:
		s3 := cfg.Blob.S3
		b, err := blob.NewS3(blob.S3Options{
			Endpoint:        s3.Endpoint,
			UseSSL:          s3.UseSSL,
			Region:          s3.Region,
			Bucket:          s3.Bucket,
			Prefix:          s3.Prefix,
			AccessKeyID:     s3.AccessKeyID,
			SecretAccessKey: s3.SecretAccessKey,
			PathStyle:       blob.PathStyle(s3.PathStyle),
			MaxSize:         cfg.Entry.MaxBytes,
		})
		if err != nil {
			return nil, fmt.Errorf("configure the s3 blob backend: %w", err)
		}
		return b, nil
	default:
		return nil, fmt.Errorf("unknown blob backend %q", cfg.Blob.Backend)
	}
}

// checkBlobBackend verifies an object store before the server takes traffic:
// the bucket exists (or is created, if configured) and the credentials can
// write, read and delete. A disk backend needs no check — a missing or
// unwritable root surfaces on the first write, and the volume is local.
func checkBlobBackend(ctx context.Context, cfg *config.Config, b blob.Backend) error {
	s3, ok := b.(*blob.S3)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, backendCheckTimeout)
	defer cancel()
	if err := s3.Check(ctx, cfg.Blob.S3.CreateBucket); err != nil {
		return fmt.Errorf("the s3 blob backend is not usable: %w", err)
	}
	return nil
}

// blobLocation names where blobs live, for the startup log and the dashboard.
// It holds no secret: the S3 description is endpoint, bucket and prefix.
func blobLocation(cfg *config.Config, b blob.Backend) string {
	if s3, ok := b.(*blob.S3); ok {
		return s3.String()
	}
	return cfg.Blob.Root
}
