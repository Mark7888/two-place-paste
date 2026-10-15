package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// minimalEnv is the smallest configuration that validates.
func minimalEnv() map[string]string {
	return map[string]string{
		"TPP_PUBLIC_BASE_URL": "https://tpp.example.com",
		"ADMIN_PASSWORD":      "hunter2",
	}
}

func lookupFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// writeDotenv writes lines to a temp .env and returns its path.
func writeDotenv(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Load(Options{DotenvPath: filepath.Join(t.TempDir(), "absent.env"), Lookup: lookupFrom(minimalEnv())})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPAddr != DefaultHTTPAddr {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, DefaultHTTPAddr)
	}
	if cfg.Redis.Addr != DefaultRedisAddr {
		t.Errorf("Redis.Addr = %q, want %q", cfg.Redis.Addr, DefaultRedisAddr)
	}
	if cfg.Blob.Backend != BlobBackendDisk {
		t.Errorf("Blob.Backend = %q, want %q", cfg.Blob.Backend, BlobBackendDisk)
	}
	if cfg.Blob.SweepInterval != DefaultSweepInterval {
		t.Errorf("Blob.SweepInterval = %s, want %s", cfg.Blob.SweepInterval, DefaultSweepInterval)
	}
	if cfg.Entry.InlineMaxBytes != DefaultInlineMax {
		t.Errorf("Entry.InlineMaxBytes = %d, want %d", cfg.Entry.InlineMaxBytes, DefaultInlineMax)
	}
	if cfg.Entry.MaxBytes != DefaultEntryMax {
		t.Errorf("Entry.MaxBytes = %d, want %d", cfg.Entry.MaxBytes, DefaultEntryMax)
	}
	if cfg.Blob.MaxTotalBytes != 0 || cfg.Blob.MaxGroupBytes != DefaultGroupMax || cfg.Blob.MinFreeBytes != 0 {
		t.Errorf("Blob limits = total %d, group %d, min free %d; want 0, %d, 0",
			cfg.Blob.MaxTotalBytes, cfg.Blob.MaxGroupBytes, cfg.Blob.MinFreeBytes, DefaultGroupMax)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %s, want info", cfg.LogLevel)
	}
	if cfg.Admin.UsesHash() {
		t.Error("Admin.UsesHash() = true, want false for a plaintext password")
	}
}

// A missing .env is a supported deployment: docker-compose injects env vars
// directly (ROADMAP P3e).
func TestLoadMissingDotenvIsNotAnError(t *testing.T) {
	t.Parallel()

	if _, err := Load(Options{
		DotenvPath: filepath.Join(t.TempDir(), "nope.env"),
		Lookup:     lookupFrom(minimalEnv()),
	}); err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
}

func TestLoadProcessEnvOverridesDotenv(t *testing.T) {
	t.Parallel()

	path := writeDotenv(t, strings.Join([]string{
		"TPP_PUBLIC_BASE_URL=https://from-file.example.com",
		"ADMIN_PASSWORD=from-file",
		"TPP_REDIS_ADDR=file-redis:6379",
	}, "\n"))

	cfg, err := Load(Options{
		DotenvPath: path,
		Lookup:     lookupFrom(map[string]string{"ADMIN_PASSWORD": "from-env"}),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Admin.Password != "from-env" {
		t.Errorf("Admin.Password = %q, want the process-environment value", cfg.Admin.Password)
	}
	if cfg.Redis.Addr != "file-redis:6379" {
		t.Errorf("Redis.Addr = %q, want the .env value", cfg.Redis.Addr)
	}
	if cfg.PublicBaseURL != "https://from-file.example.com" {
		t.Errorf("PublicBaseURL = %q, want the .env value", cfg.PublicBaseURL)
	}
}

// SPEC §4.4 / §9: switching to a hashed admin password must be config-only.
func TestLoadAdminCredentialModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		env      map[string]string
		wantErr  string
		wantHash bool
	}{
		{
			name:     "plaintext password",
			env:      map[string]string{"TPP_PUBLIC_BASE_URL": "https://x.example", "ADMIN_PASSWORD": "pw"},
			wantHash: false,
		},
		{
			name:     "hashed password",
			env:      map[string]string{"TPP_PUBLIC_BASE_URL": "https://x.example", "ADMIN_PASSWORD_HASH": "$2y$10$abc"},
			wantHash: true,
		},
		{
			name:    "neither set",
			env:     map[string]string{"TPP_PUBLIC_BASE_URL": "https://x.example"},
			wantErr: "neither is set",
		},
		{
			name: "both set",
			env: map[string]string{
				"TPP_PUBLIC_BASE_URL": "https://x.example",
				"ADMIN_PASSWORD":      "pw",
				"ADMIN_PASSWORD_HASH": "$2y$10$abc",
			},
			wantErr: "both are set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Load(Options{DotenvPath: filepath.Join(t.TempDir(), "absent.env"), Lookup: lookupFrom(tt.env)})
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Load() error = nil, want error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got := cfg.Admin.UsesHash(); got != tt.wantHash {
				t.Errorf("Admin.UsesHash() = %v, want %v", got, tt.wantHash)
			}
		})
	}
}

func TestLoadValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "missing public base url",
			env:     map[string]string{"ADMIN_PASSWORD": "pw"},
			wantErr: "TPP_PUBLIC_BASE_URL is required",
		},
		{
			name:    "public base url without scheme",
			env:     map[string]string{"ADMIN_PASSWORD": "pw", "TPP_PUBLIC_BASE_URL": "tpp.example.com"},
			wantErr: "must start with http:// or https://",
		},
		{
			name:    "unknown blob backend",
			env:     map[string]string{"ADMIN_PASSWORD": "pw", "TPP_PUBLIC_BASE_URL": "https://x.example", "TPP_BLOB_BACKEND": "gcs"},
			wantErr: `got "gcs"`,
		},
		{
			name:    "inline max above hard cap",
			env:     map[string]string{"ADMIN_PASSWORD": "pw", "TPP_PUBLIC_BASE_URL": "https://x.example", "TPP_ENTRY_INLINE_MAX_BYTES": "99999999999"},
			wantErr: "must not exceed TPP_ENTRY_MAX_BYTES",
		},
		{
			name:    "non-numeric integer",
			env:     map[string]string{"ADMIN_PASSWORD": "pw", "TPP_PUBLIC_BASE_URL": "https://x.example", "TPP_REDIS_DB": "two"},
			wantErr: `TPP_REDIS_DB: "two" is not an integer`,
		},
		{
			name:    "bad duration",
			env:     map[string]string{"ADMIN_PASSWORD": "pw", "TPP_PUBLIC_BASE_URL": "https://x.example", "TPP_BLOB_SWEEP_INTERVAL": "ten minutes"},
			wantErr: "is not a duration",
		},
		{
			name:    "bad log level",
			env:     map[string]string{"ADMIN_PASSWORD": "pw", "TPP_PUBLIC_BASE_URL": "https://x.example", "TPP_LOG_LEVEL": "chatty"},
			wantErr: "is not a log level",
		},
		{
			name:    "bad byte size",
			env:     map[string]string{"ADMIN_PASSWORD": "pw", "TPP_PUBLIC_BASE_URL": "https://x.example", "TPP_BLOB_MAX_TOTAL_BYTES": "5 gallons"},
			wantErr: `TPP_BLOB_MAX_TOTAL_BYTES: "5 gallons" is not a size`,
		},
		{
			name:    "negative limit",
			env:     map[string]string{"ADMIN_PASSWORD": "pw", "TPP_PUBLIC_BASE_URL": "https://x.example", "TPP_BLOB_MIN_FREE_BYTES": "-1"},
			wantErr: "TPP_BLOB_MIN_FREE_BYTES must not be negative",
		},
		{
			name:    "total limit below one entry",
			env:     map[string]string{"ADMIN_PASSWORD": "pw", "TPP_PUBLIC_BASE_URL": "https://x.example", "TPP_BLOB_MAX_TOTAL_BYTES": "1MiB"},
			wantErr: "TPP_BLOB_MAX_TOTAL_BYTES (1048576) must be 0 (unlimited) or at least TPP_ENTRY_MAX_BYTES",
		},
		{
			name:    "group limit below one entry",
			env:     map[string]string{"ADMIN_PASSWORD": "pw", "TPP_PUBLIC_BASE_URL": "https://x.example", "TPP_BLOB_MAX_GROUP_BYTES": "5MB"},
			wantErr: "TPP_BLOB_MAX_GROUP_BYTES (5000000) must be 0 (unlimited) or at least TPP_ENTRY_MAX_BYTES",
		},
		{
			name:    "negative sweep interval",
			env:     map[string]string{"ADMIN_PASSWORD": "pw", "TPP_PUBLIC_BASE_URL": "https://x.example", "TPP_BLOB_SWEEP_INTERVAL": "-5m"},
			wantErr: "TPP_BLOB_SWEEP_INTERVAL must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(Options{DotenvPath: filepath.Join(t.TempDir(), "absent.env"), Lookup: lookupFrom(tt.env)})
			if err == nil {
				t.Fatalf("Load() error = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Load() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// Every problem should surface in one pass so an operator edits .env once.
func TestLoadReportsAllErrorsAtOnce(t *testing.T) {
	t.Parallel()

	_, err := Load(Options{
		DotenvPath: filepath.Join(t.TempDir(), "absent.env"),
		Lookup:     lookupFrom(map[string]string{"TPP_BLOB_BACKEND": "gcs"}),
	})
	if err == nil {
		t.Fatal("Load() error = nil, want a combined error")
	}
	for _, want := range []string{"TPP_PUBLIC_BASE_URL is required", "neither is set", "TPP_BLOB_BACKEND"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load() error = %v, want it to contain %q", err, want)
		}
	}
}

func TestLoadTrimsTrailingSlashFromBaseURL(t *testing.T) {
	t.Parallel()

	cfg, err := Load(Options{
		DotenvPath: filepath.Join(t.TempDir(), "absent.env"),
		Lookup:     lookupFrom(map[string]string{"ADMIN_PASSWORD": "pw", "TPP_PUBLIC_BASE_URL": "https://x.example/"}),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PublicBaseURL != "https://x.example" {
		t.Errorf("PublicBaseURL = %q, want the trailing slash trimmed", cfg.PublicBaseURL)
	}
}

// SPEC §4.5 depends on entry lifetimes being immutable; assert nobody has
// quietly made them configurable.
func TestSpecConstants(t *testing.T) {
	t.Parallel()

	if EntryTTL != 24*time.Hour {
		t.Errorf("EntryTTL = %s, want 24h (SPEC §4.2; the §4.5 GC scheme depends on it)", EntryTTL)
	}
	if PairingTokenTTL != 5*time.Minute {
		t.Errorf("PairingTokenTTL = %s, want 5m (SPEC §3.2)", PairingTokenTTL)
	}
}

func TestLoadByteSizes(t *testing.T) {
	t.Parallel()

	env := minimalEnv()
	env["TPP_BLOB_MAX_TOTAL_BYTES"] = "5GiB"
	env["TPP_BLOB_MAX_GROUP_BYTES"] = "0"
	env["TPP_ENTRY_MAX_BYTES"] = "10485760"
	env["TPP_ENTRY_INLINE_MAX_BYTES"] = "256 KiB"
	cfg, err := Load(Options{DotenvPath: filepath.Join(t.TempDir(), "absent.env"), Lookup: lookupFrom(env)})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Blob.MaxTotalBytes != 5<<30 || cfg.Blob.MaxGroupBytes != 0 {
		t.Errorf("Blob limits = %d / %d, want %d / 0", cfg.Blob.MaxTotalBytes, cfg.Blob.MaxGroupBytes, int64(5<<30))
	}
	if cfg.Entry.MaxBytes != 10<<20 || cfg.Entry.InlineMaxBytes != 256<<10 {
		t.Errorf("Entry limits = %d / %d, want %d / %d", cfg.Entry.MaxBytes, cfg.Entry.InlineMaxBytes, 10<<20, 256<<10)
	}
}

func TestParseByteSize(t *testing.T) {
	t.Parallel()

	good := map[string]int64{
		"0":        0,
		"1048576":  1 << 20,
		"512B":     512,
		"500MB":    500e6,
		"500mb":    500e6,
		"5GiB":     5 << 30,
		"5 GiB":    5 << 30,
		"1.5GiB":   3 << 29,
		"64k":      64 << 10,
		"2T":       2 << 40,
		" 10MiB  ": 10 << 20,
	}
	for in, want := range good {
		got, err := ParseByteSize(in)
		if err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %d, %v; want %d, nil", in, got, err, want)
		}
	}
	for _, in := range []string{"", "GiB", "5 gallons", "1..5MB", "9999999999999TiB", "1e3"} {
		if got, err := ParseByteSize(in); err == nil {
			t.Errorf("ParseByteSize(%q) = %d, nil; want an error", in, got)
		}
	}
}

func TestLoadS3(t *testing.T) {
	t.Parallel()

	secretFile := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secretFile, []byte("from-a-file\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	env := minimalEnv()
	env["TPP_BLOB_BACKEND"] = "s3"
	env["TPP_S3_ENDPOINT"] = "http://minio:9000/"
	env["TPP_S3_BUCKET"] = "tpp"
	env["TPP_S3_ACCESS_KEY_ID"] = "tpp-relay"
	env["TPP_S3_SECRET_ACCESS_KEY_FILE"] = secretFile
	env["TPP_S3_CREATE_BUCKET"] = "yes"

	cfg, err := Load(Options{DotenvPath: filepath.Join(t.TempDir(), "absent.env"), Lookup: lookupFrom(env)})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := S3Config{
		Endpoint: "minio:9000", UseSSL: false, Region: "us-east-1", Bucket: "tpp",
		AccessKeyID: "tpp-relay", SecretAccessKey: "from-a-file", PathStyle: "auto", CreateBucket: true,
	}
	if cfg.Blob.S3 != want {
		t.Errorf("Blob.S3 = %+v, want %+v", cfg.Blob.S3, want)
	}

	// An endpoint without a scheme is https unless told otherwise, and an
	// explicit TPP_S3_USE_SSL wins over the scheme.
	env["TPP_S3_ENDPOINT"] = "s3.example.com"
	cfg, err = Load(Options{DotenvPath: filepath.Join(t.TempDir(), "absent.env"), Lookup: lookupFrom(env)})
	if err != nil || !cfg.Blob.S3.UseSSL {
		t.Errorf("bare endpoint: UseSSL = %v, %v; want true", cfg.Blob.S3.UseSSL, err)
	}
	env["TPP_S3_ENDPOINT"] = "http://minio:9000"
	env["TPP_S3_USE_SSL"] = "true"
	cfg, err = Load(Options{DotenvPath: filepath.Join(t.TempDir(), "absent.env"), Lookup: lookupFrom(env)})
	if err != nil || !cfg.Blob.S3.UseSSL {
		t.Errorf("explicit TPP_S3_USE_SSL: UseSSL = %v, %v; want true", cfg.Blob.S3.UseSSL, err)
	}
}

func TestLoadS3Errors(t *testing.T) {
	t.Parallel()

	base := func() map[string]string {
		env := minimalEnv()
		env["TPP_BLOB_BACKEND"] = "s3"
		env["TPP_S3_BUCKET"] = "tpp"
		return env
	}
	tests := []struct {
		name    string
		set     map[string]string
		wantErr string
	}{
		{"no bucket", map[string]string{"TPP_S3_BUCKET": ""}, "TPP_S3_BUCKET is required"},
		{"half the credentials", map[string]string{"TPP_S3_ACCESS_KEY_ID": "id"}, "set both TPP_S3_ACCESS_KEY_ID and TPP_S3_SECRET_ACCESS_KEY"},
		{"endpoint with a path", map[string]string{"TPP_S3_ENDPOINT": "https://s3.example.com/tpp"}, "TPP_S3_ENDPOINT must be host[:port]"},
		{"bad path style", map[string]string{"TPP_S3_PATH_STYLE": "sideways"}, "TPP_S3_PATH_STYLE must be auto, path or virtual"},
		{"bad boolean", map[string]string{"TPP_S3_CREATE_BUCKET": "perhaps"}, `TPP_S3_CREATE_BUCKET: "perhaps" is not a boolean`},
		{"secret and file", map[string]string{"TPP_S3_ACCESS_KEY_ID": "id", "TPP_S3_SECRET_ACCESS_KEY": "s", "TPP_S3_SECRET_ACCESS_KEY_FILE": "/x"}, "not both"},
		{"missing secret file", map[string]string{"TPP_S3_ACCESS_KEY_ID": "id", "TPP_S3_SECRET_ACCESS_KEY_FILE": "/no/such/file"}, "TPP_S3_SECRET_ACCESS_KEY_FILE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := base()
			for k, v := range tt.set {
				env[k] = v
			}
			_, err := Load(Options{DotenvPath: filepath.Join(t.TempDir(), "absent.env"), Lookup: lookupFrom(env)})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Load() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
