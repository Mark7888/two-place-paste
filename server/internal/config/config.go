// Package config loads TwoPlacePaste server configuration from a .env file
// and the process environment (SPEC §4.1, §4.4).
//
// Precedence: process environment wins over the .env file, so a container
// orchestrator can override a baked-in file without editing it.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Spec-mandated constants. These are deliberately NOT configurable.
const (
	// EntryTTL is the lifetime of every clipboard entry (SPEC §4.2).
	//
	// Do not make this configurable and do not add a pin / favourite /
	// extend-TTL feature. The blob GC scheme in SPEC §4.5 derives a blob's
	// storage path from created_at+EntryTTL and reclaims whole hour buckets
	// without ever consulting Redis. A mutable lifetime invalidates that
	// scheme outright. See ROADMAP §5, "Standing prohibition".
	EntryTTL = 24 * time.Hour

	// PairingTokenTTL is the lifetime of a single-use pairing token (SPEC §3.2).
	PairingTokenTTL = 5 * time.Minute
)

// Defaults for values that are configurable.
const (
	DefaultHTTPAddr      = ":8080"
	DefaultRedisAddr     = "127.0.0.1:6379"
	DefaultBlobRoot      = "/var/lib/tpp/blobs"
	DefaultSweepInterval = 10 * time.Minute
	DefaultSessionTTL    = 12 * time.Hour
	DefaultInlineMax     = 256 << 10 // 256 KiB, SPEC §4.3
	DefaultEntryMax      = 10 << 20  // 10 MiB ciphertext, SPEC §4.3
)

// Config is the fully resolved server configuration.
type Config struct {
	// HTTPAddr is the listen address. TLS is terminated by a reverse proxy in
	// front of the server (SPEC §4.1), so this is plain HTTP.
	HTTPAddr string

	// PublicBaseURL is the externally reachable origin, e.g.
	// "https://tpp.example.com". Creation-token URLs and their QR codes are
	// built from it (SPEC §3.1, §4.4), so it must not include a path.
	PublicBaseURL string

	LogLevel slog.Level

	Redis RedisConfig
	Admin AdminConfig
	Blob  BlobConfig
	Entry EntryConfig
}

// RedisConfig addresses the single datastore (SPEC §4.2).
//
// The deployment must set maxmemory-policy to volatile-lru. allkeys-lru would
// silently evict persistent-zone records (groups, devices, wrapped keys) under
// memory pressure and destroy pairings.
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

// AdminConfig holds credentials for the admin UI (SPEC §4.4).
//
// Exactly one of Password or PasswordHash is set. MVP ships the plaintext
// path; moving to hashed storage is a config change, not a code change
// (SPEC §9, deferred backlog).
type AdminConfig struct {
	// Password is a plaintext password read from ADMIN_PASSWORD.
	Password string

	// PasswordHash is a pre-hashed password read from ADMIN_PASSWORD_HASH.
	// The hash format is the verifier's concern, not this package's.
	PasswordHash string

	// SessionTTL bounds the admin session cookie lifetime.
	SessionTTL time.Duration
}

// UsesHash reports whether the hashed-credential path is configured. Callers
// verify against PasswordHash when true and Password when false.
func (a AdminConfig) UsesHash() bool { return a.PasswordHash != "" }

// BlobBackend selects the storage implementation for oversized ciphertext.
type BlobBackend string

const (
	// BlobBackendDisk stores blobs on the local filesystem (MVP, SPEC §4.3).
	BlobBackendDisk BlobBackend = "disk"

	// BlobBackendS3 is the deferred object-storage backend. Its Sweep() is a
	// documented no-op because object stores expire objects via lifecycle
	// rules (SPEC §4.5).
	BlobBackendS3 BlobBackend = "s3"
)

// BlobConfig configures the blob backend and its sweeper (SPEC §4.3, §4.5).
type BlobConfig struct {
	Backend BlobBackend

	// Root is the disk backend's base directory. Layout is
	// <Root>/<YYYYMMDDHH>/<entry_id>.bin where the bucket is the UTC hour in
	// which the blob expires.
	Root string

	// SweepInterval is how often the bucket sweep runs after the startup pass.
	SweepInterval time.Duration
}

// EntryConfig bounds entry sizes. Both limits are measured on the ciphertext:
// the server has no knowledge of plaintext size (SPEC §4.3).
type EntryConfig struct {
	// InlineMaxBytes is the largest ciphertext stored inline in Redis.
	InlineMaxBytes int64

	// MaxBytes is the hard per-entry cap, enforced at the reader rather than
	// after buffering.
	MaxBytes int64
}

// Options controls Load.
type Options struct {
	// DotenvPath is the .env file to read. Defaults to ".env". A missing file
	// is not an error.
	DotenvPath string

	// Lookup resolves process environment variables. Defaults to os.LookupEnv;
	// tests inject their own.
	Lookup func(key string) (string, bool)
}

// Load resolves configuration from the .env file and the process environment,
// then validates it. The returned error names every problem found, so an
// operator fixes one file once rather than one variable per restart.
func Load(opts Options) (*Config, error) {
	path := opts.DotenvPath
	if path == "" {
		path = ".env"
	}
	lookup := opts.Lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}

	file, err := loadDotenvFile(path)
	if err != nil {
		return nil, err
	}

	r := &resolver{file: file, lookup: lookup}
	cfg := &Config{
		HTTPAddr:      r.str("TPP_HTTP_ADDR", DefaultHTTPAddr),
		PublicBaseURL: strings.TrimRight(r.str("TPP_PUBLIC_BASE_URL", ""), "/"),
		LogLevel:      r.logLevel("TPP_LOG_LEVEL", slog.LevelInfo),
		Redis: RedisConfig{
			Addr:     r.str("TPP_REDIS_ADDR", DefaultRedisAddr),
			Password: r.str("TPP_REDIS_PASSWORD", ""),
			DB:       r.intVal("TPP_REDIS_DB", 0),
		},
		Admin: AdminConfig{
			Password:     r.str("ADMIN_PASSWORD", ""),
			PasswordHash: r.str("ADMIN_PASSWORD_HASH", ""),
			SessionTTL:   r.duration("TPP_ADMIN_SESSION_TTL", DefaultSessionTTL),
		},
		Blob: BlobConfig{
			Backend:       BlobBackend(r.str("TPP_BLOB_BACKEND", string(BlobBackendDisk))),
			Root:          r.str("TPP_BLOB_ROOT", DefaultBlobRoot),
			SweepInterval: r.duration("TPP_BLOB_SWEEP_INTERVAL", DefaultSweepInterval),
		},
		Entry: EntryConfig{
			InlineMaxBytes: r.int64Val("TPP_ENTRY_INLINE_MAX_BYTES", DefaultInlineMax),
			MaxBytes:       r.int64Val("TPP_ENTRY_MAX_BYTES", DefaultEntryMax),
		},
	}

	if err := errors.Join(append(r.errs, cfg.Validate())...); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// Validate reports every configuration problem at once.
func (c *Config) Validate() error {
	var errs []error

	if c.HTTPAddr == "" {
		errs = append(errs, errors.New("TPP_HTTP_ADDR must not be empty"))
	}
	if c.PublicBaseURL == "" {
		errs = append(errs, errors.New("TPP_PUBLIC_BASE_URL is required (creation-token URLs and QR codes are built from it)"))
	} else if !strings.HasPrefix(c.PublicBaseURL, "http://") && !strings.HasPrefix(c.PublicBaseURL, "https://") {
		errs = append(errs, fmt.Errorf("TPP_PUBLIC_BASE_URL must start with http:// or https://, got %q", c.PublicBaseURL))
	}
	if c.Redis.Addr == "" {
		errs = append(errs, errors.New("TPP_REDIS_ADDR must not be empty"))
	}
	if c.Redis.DB < 0 {
		errs = append(errs, fmt.Errorf("TPP_REDIS_DB must not be negative, got %d", c.Redis.DB))
	}

	switch {
	case c.Admin.Password == "" && c.Admin.PasswordHash == "":
		errs = append(errs, errors.New("set exactly one of ADMIN_PASSWORD or ADMIN_PASSWORD_HASH; neither is set"))
	case c.Admin.Password != "" && c.Admin.PasswordHash != "":
		errs = append(errs, errors.New("set exactly one of ADMIN_PASSWORD or ADMIN_PASSWORD_HASH; both are set"))
	}
	if c.Admin.SessionTTL <= 0 {
		errs = append(errs, fmt.Errorf("TPP_ADMIN_SESSION_TTL must be positive, got %s", c.Admin.SessionTTL))
	}

	switch c.Blob.Backend {
	case BlobBackendDisk:
		if c.Blob.Root == "" {
			errs = append(errs, errors.New("TPP_BLOB_ROOT must not be empty for the disk backend"))
		}
	case BlobBackendS3:
		// Credentials and bucket configuration land with the deferred S3
		// implementation (SPEC §4.3); nothing to validate yet.
	default:
		errs = append(errs, fmt.Errorf("TPP_BLOB_BACKEND must be %q or %q, got %q", BlobBackendDisk, BlobBackendS3, c.Blob.Backend))
	}
	if c.Blob.SweepInterval <= 0 {
		errs = append(errs, fmt.Errorf("TPP_BLOB_SWEEP_INTERVAL must be positive, got %s", c.Blob.SweepInterval))
	}

	if c.Entry.MaxBytes <= 0 {
		errs = append(errs, fmt.Errorf("TPP_ENTRY_MAX_BYTES must be positive, got %d", c.Entry.MaxBytes))
	}
	if c.Entry.InlineMaxBytes <= 0 {
		errs = append(errs, fmt.Errorf("TPP_ENTRY_INLINE_MAX_BYTES must be positive, got %d", c.Entry.InlineMaxBytes))
	}
	if c.Entry.InlineMaxBytes > c.Entry.MaxBytes {
		errs = append(errs, fmt.Errorf("TPP_ENTRY_INLINE_MAX_BYTES (%d) must not exceed TPP_ENTRY_MAX_BYTES (%d)", c.Entry.InlineMaxBytes, c.Entry.MaxBytes))
	}

	return errors.Join(errs...)
}

// resolver reads values with process-environment-over-file precedence and
// accumulates parse errors instead of failing on the first one.
type resolver struct {
	file   map[string]string
	lookup func(string) (string, bool)
	errs   []error
}

func (r *resolver) raw(key string) (string, bool) {
	if v, ok := r.lookup(key); ok {
		return v, true
	}
	v, ok := r.file[key]
	return v, ok
}

func (r *resolver) str(key, def string) string {
	if v, ok := r.raw(key); ok && v != "" {
		return v
	}
	return def
}

func (r *resolver) intVal(key string, def int) int {
	v, ok := r.raw(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %q is not an integer", key, v))
		return def
	}
	return n
}

func (r *resolver) int64Val(key string, def int64) int64 {
	v, ok := r.raw(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %q is not an integer", key, v))
		return def
	}
	return n
}

func (r *resolver) duration(key string, def time.Duration) time.Duration {
	v, ok := r.raw(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %q is not a duration (want e.g. \"10m\", \"12h\")", key, v))
		return def
	}
	return d
}

func (r *resolver) logLevel(key string, def slog.Level) slog.Level {
	v, ok := r.raw(key)
	if !ok || v == "" {
		return def
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(v)); err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %q is not a log level (want debug, info, warn or error)", key, v))
		return def
	}
	return lvl
}
