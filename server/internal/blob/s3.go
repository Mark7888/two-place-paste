package blob

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotImplemented is returned by every S3 operation. The S3 backend is
// deferred work (SPEC §4.3, ROADMAP deferred backlog); this stub exists so the
// interface has a second implementation from day one and so the deferred
// deliverable is a new file, not a refactor.
var ErrNotImplemented = errors.New("blob: the s3 backend is not implemented")

// S3 is the placeholder object-storage backend.
type S3 struct {
	// Bucket is the object-store bucket name. Credentials and endpoint
	// configuration land with the implementation.
	Bucket string
}

var _ Backend = (*S3)(nil)

// Put implements Backend.
func (s *S3) Put(context.Context, string, time.Time, io.Reader) (string, error) {
	return "", ErrNotImplemented
}

// Get implements Backend.
func (s *S3) Get(context.Context, string) (io.ReadCloser, error) { return nil, ErrNotImplemented }

// Delete implements Backend.
func (s *S3) Delete(context.Context, string) error { return ErrNotImplemented }

// Sweep implements Backend as a deliberate no-op, and will keep doing so once
// the rest of this backend exists.
//
// Object stores expire objects natively through a lifecycle rule configured on
// the bucket, so there is nothing for a sweeper to do (SPEC §4.5). Lifecycle
// granularity is one day rather than one hour, so objects may outlive their
// TTL by up to a day; that is harmless, because Redis — not the object store —
// is the sole authority on whether an entry is visible.
func (s *S3) Sweep(context.Context, time.Time) error { return nil }
