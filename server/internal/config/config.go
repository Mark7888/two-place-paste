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
	"math"
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

	// DefaultGroupMax is the per-group blob storage limit. The total limit
	// defaults to unlimited: only the operator knows how big the volume or
	// bucket is.
	DefaultGroupMax = 1 << 30 // 1 GiB
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

	// BlobBackendS3 stores blobs in an S3-compatible object store: MinIO,
	// AWS S3 or any other (SPEC §4.3).
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

	// MaxTotalBytes caps the ciphertext held in the blob backend across every
	// group. When a new entry would not fit, the oldest entries are evicted,
	// even inside their 24 hours. Zero is unlimited.
	MaxTotalBytes int64

	// MaxGroupBytes caps one group's blob storage, evicting that group's
	// oldest entries first, so one busy group cannot push out everyone
	// else's. Zero is unlimited.
	MaxGroupBytes int64

	// S3 configures the s3 backend; it is ignored by the disk backend.
	S3 S3Config

	// MinFreeBytes is the disk backend's floor: a write is refused while the
	// volume has less than this free, whatever the limits above allow. Zero
	// disables the check.
	MinFreeBytes int64
}

// S3Config addresses an S3-compatible object store.
type S3Config struct {
	// Endpoint is host[:port] of the S3 API, e.g. "minio:9000". Empty means
	// AWS. A scheme may be given ("http://minio:9000"), and then decides
	// UseSSL on its own.
	Endpoint string

	// UseSSL selects https. It defaults to true, and to false when the
	// endpoint is written with http://.
	UseSSL bool

	Region string
	Bucket string

	// Prefix is prepended to every key, so one bucket can be shared.
	Prefix string

	// AccessKeyID and SecretAccessKey are static credentials. Both empty
	// means the standard AWS credential chain (environment, then an
	// instance or task role).
	AccessKeyID     string
	SecretAccessKey string

	// PathStyle is "auto", "path" or "virtual". Auto means path-style for a
	// custom endpoint and virtual-host style for AWS.
	PathStyle string

	// CreateBucket creates a missing bucket at startup instead of refusing
	// to start.
	CreateBucket bool
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
			MaxTotalBytes: r.byteSize("TPP_BLOB_MAX_TOTAL_BYTES", 0),
			MaxGroupBytes: r.byteSize("TPP_BLOB_MAX_GROUP_BYTES", DefaultGroupMax),
			MinFreeBytes:  r.byteSize("TPP_BLOB_MIN_FREE_BYTES", 0),
			S3:            r.s3(),
		},
		Entry: EntryConfig{
			InlineMaxBytes: r.byteSize("TPP_ENTRY_INLINE_MAX_BYTES", DefaultInlineMax),
			MaxBytes:       r.byteSize("TPP_ENTRY_MAX_BYTES", DefaultEntryMax),
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
		s3 := c.Blob.S3
		if s3.Bucket == "" {
			errs = append(errs, errors.New("TPP_S3_BUCKET is required for the s3 backend"))
		}
		if (s3.AccessKeyID == "") != (s3.SecretAccessKey == "") {
			errs = append(errs, errors.New("set both TPP_S3_ACCESS_KEY_ID and TPP_S3_SECRET_ACCESS_KEY, or neither (to use the AWS credential chain)"))
		}
		if strings.Contains(s3.Endpoint, "/") {
			errs = append(errs, fmt.Errorf("TPP_S3_ENDPOINT must be host[:port] with an optional http:// or https://, and no path; got %q", s3.Endpoint))
		}
		switch s3.PathStyle {
		case "auto", "path", "virtual":
		default:
			errs = append(errs, fmt.Errorf("TPP_S3_PATH_STYLE must be auto, path or virtual, got %q", s3.PathStyle))
		}
	default:
		errs = append(errs, fmt.Errorf("TPP_BLOB_BACKEND must be %q or %q, got %q", BlobBackendDisk, BlobBackendS3, c.Blob.Backend))
	}
	if c.Blob.SweepInterval <= 0 {
		errs = append(errs, fmt.Errorf("TPP_BLOB_SWEEP_INTERVAL must be positive, got %s", c.Blob.SweepInterval))
	}
	for _, lim := range []struct {
		key string
		v   int64
	}{
		{"TPP_BLOB_MAX_TOTAL_BYTES", c.Blob.MaxTotalBytes},
		{"TPP_BLOB_MAX_GROUP_BYTES", c.Blob.MaxGroupBytes},
		{"TPP_BLOB_MIN_FREE_BYTES", c.Blob.MinFreeBytes},
	} {
		if lim.v < 0 {
			errs = append(errs, fmt.Errorf("%s must not be negative, got %d", lim.key, lim.v))
		}
	}
	// A limit smaller than one entry would refuse every large entry outright,
	// which is a misconfiguration rather than a policy.
	if c.Blob.MaxTotalBytes > 0 && c.Blob.MaxTotalBytes < c.Entry.MaxBytes {
		errs = append(errs, fmt.Errorf("TPP_BLOB_MAX_TOTAL_BYTES (%d) must be 0 (unlimited) or at least TPP_ENTRY_MAX_BYTES (%d)", c.Blob.MaxTotalBytes, c.Entry.MaxBytes))
	}
	if c.Blob.MaxGroupBytes > 0 && c.Blob.MaxGroupBytes < c.Entry.MaxBytes {
		errs = append(errs, fmt.Errorf("TPP_BLOB_MAX_GROUP_BYTES (%d) must be 0 (unlimited) or at least TPP_ENTRY_MAX_BYTES (%d)", c.Blob.MaxGroupBytes, c.Entry.MaxBytes))
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

// s3 reads the S3 settings. The endpoint's scheme, when there is one, decides
// TLS unless TPP_S3_USE_SSL says otherwise, so that "http://minio:9000" works
// on its own.
func (r *resolver) s3() S3Config {
	endpoint := r.str("TPP_S3_ENDPOINT", "")
	useSSL := true
	switch {
	case strings.HasPrefix(endpoint, "http://"):
		endpoint, useSSL = strings.TrimPrefix(endpoint, "http://"), false
	case strings.HasPrefix(endpoint, "https://"):
		endpoint = strings.TrimPrefix(endpoint, "https://")
	}
	endpoint = strings.TrimRight(endpoint, "/")
	return S3Config{
		Endpoint:        endpoint,
		UseSSL:          r.boolean("TPP_S3_USE_SSL", useSSL),
		Region:          r.str("TPP_S3_REGION", "us-east-1"),
		Bucket:          r.str("TPP_S3_BUCKET", ""),
		Prefix:          r.str("TPP_S3_PREFIX", ""),
		AccessKeyID:     r.secret("TPP_S3_ACCESS_KEY_ID"),
		SecretAccessKey: r.secret("TPP_S3_SECRET_ACCESS_KEY"),
		PathStyle:       strings.ToLower(r.str("TPP_S3_PATH_STYLE", "auto")),
		CreateBucket:    r.boolean("TPP_S3_CREATE_BUCKET", false),
	}
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

// secret reads key, or the file named by key_FILE — the Docker secrets
// convention, so a credential need not sit in the environment. Setting both is
// an error rather than a guess.
func (r *resolver) secret(key string) string {
	v := r.str(key, "")
	path := r.str(key+"_FILE", "")
	if path == "" {
		return v
	}
	if v != "" {
		r.errs = append(r.errs, fmt.Errorf("set %s or %s_FILE, not both", key, key))
		return v
	}
	b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s_FILE: %w", key, err))
		return ""
	}
	return strings.TrimRight(string(b), "\r\n")
}

// boolean reads true/false, yes/no, on/off or 1/0.
func (r *resolver) boolean(key string, def bool) bool {
	v, ok := r.raw(key)
	if !ok || v == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	r.errs = append(r.errs, fmt.Errorf("%s: %q is not a boolean (want true or false)", key, v))
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

// byteSize reads a size in bytes: a plain integer, or a number with a unit —
// "256KiB", "10MB", "5 GiB". Units are case-insensitive; KB/MB/GB/TB are
// decimal and KiB/MiB/GiB/TiB binary, as everywhere else.
func (r *resolver) byteSize(key string, def int64) int64 {
	v, ok := r.raw(key)
	if !ok || v == "" {
		return def
	}
	n, err := ParseByteSize(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %w", key, err))
		return def
	}
	return n
}

// byteUnits maps a lower-cased unit suffix to its multiplier.
var byteUnits = map[string]int64{
	"": 1, "b": 1,
	"kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
	"k": 1 << 10, "m": 1 << 20, "g": 1 << 30, "t": 1 << 40,
	"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
}

// ParseByteSize parses a byte count such as "5GiB", "500MB" or "1048576".
// Fractions are allowed with a unit ("1.5GiB"); the result is truncated to a
// whole byte.
func ParseByteSize(s string) (int64, error) {
	t := strings.TrimSpace(s)
	i := 0
	for i < len(t) && (t[i] >= '0' && t[i] <= '9' || t[i] == '.' || t[i] == '-') {
		i++
	}
	num, unit := t[:i], strings.ToLower(strings.TrimSpace(t[i:]))
	mult, ok := byteUnits[unit]
	if num == "" || !ok {
		return 0, fmt.Errorf("%q is not a size (want e.g. \"1048576\", \"500MB\", \"5GiB\")", s)
	}
	if n, err := strconv.ParseInt(num, 10, 64); err == nil {
		if n != 0 && (n > math.MaxInt64/mult || n < math.MinInt64/mult) {
			return 0, fmt.Errorf("%q is too large", s)
		}
		return n * mult, nil
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a size (want e.g. \"1048576\", \"500MB\", \"5GiB\")", s)
	}
	f *= float64(mult)
	if f >= math.MaxInt64 || f <= math.MinInt64 {
		return 0, fmt.Errorf("%q is too large", s)
	}
	return int64(f), nil
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
