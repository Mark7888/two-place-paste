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
