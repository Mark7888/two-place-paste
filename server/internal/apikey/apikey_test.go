package apikey_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/apikey"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

func TestCreate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	keys := store.NewMemoryStore()

	key, rec, err := apikey.Create(ctx, keys, apikey.New{
		Name: "  backup script ", Scopes: []string{"admin", "read", "read"}, ExpiryDays: 30, CreatedBy: "cli",
	}, now)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	id, ok := apikey.Parse(key)
	if !ok || id != rec.ID {
		t.Fatalf("Parse(new key) = %q, %v; want %q, true", id, ok, rec.ID)
	}
	if !strings.HasPrefix(key, "tpp_ak_") || len(key) != len("tpp_ak_")+8+1+52 {
		t.Errorf("key = %q, want tpp_ak_ + 8 + _ + 52 characters", key)
	}
	if rec.Name != "backup script" {
		t.Errorf("name = %q, want %q", rec.Name, "backup script")
	}
	if want := []string{"read", "admin"}; !slices.Equal(rec.Scopes, want) {
		t.Errorf("scopes = %v, want %v", rec.Scopes, want)
	}
	if want := now.Add(30 * 24 * time.Hour); !rec.ExpiresAt.Equal(want) {
		t.Errorf("expires = %v, want %v", rec.ExpiresAt, want)
	}
	if strings.Contains(string(rec.Hash), key) || len(rec.Hash) != 32 {
		t.Errorf("hash = %x, want a 32-byte digest", rec.Hash)
	}

	stored, err := keys.GetAPIKey(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetAPIKey() error = %v", err)
	}
	if !apikey.Matches(stored, key) {
		t.Error("Matches(stored, key) = false, want true")
	}
	other, _, err := apikey.Create(ctx, keys, apikey.New{Name: "other", Scopes: []string{"read"}}, now)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if apikey.Matches(stored, other) {
		t.Error("Matches(stored, another key) = true, want false")
	}
}

func TestCreateRefuses(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		in   apikey.New
		want error
	}{
		{"empty name", apikey.New{Name: " ", Scopes: []string{"read"}}, apikey.ErrName},
		{"long name", apikey.New{Name: strings.Repeat("x", 81), Scopes: []string{"read"}}, apikey.ErrName},
		{"control character", apikey.New{Name: "a\nb", Scopes: []string{"read"}}, apikey.ErrName},
		{"no scope", apikey.New{Name: "a"}, apikey.ErrScope},
		{"unknown scope", apikey.New{Name: "a", Scopes: []string{"write"}}, apikey.ErrScope},
		{"odd lifetime", apikey.New{Name: "a", Scopes: []string{"read"}, ExpiryDays: 7}, apikey.ErrExpiry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := apikey.Create(context.Background(), store.NewMemoryStore(), tt.in, now)
			if !errors.Is(err, tt.want) {
				t.Errorf("Create() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCreateLimit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	keys := store.NewMemoryStore()
	for range store.MaxAPIKeys {
		if _, _, err := apikey.Create(ctx, keys, apikey.New{Name: "k", Scopes: []string{"read"}}, time.Now()); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}
	if _, _, err := apikey.Create(ctx, keys, apikey.New{Name: "k", Scopes: []string{"read"}}, time.Now()); !errors.Is(err, store.ErrAPIKeyLimit) {
		t.Errorf("Create() over the limit error = %v, want %v", err, store.ErrAPIKeyLimit)
	}
}

func TestParse(t *testing.T) {
	t.Parallel()

	secret := strings.Repeat("a", 52)
	tests := []struct {
		name   string
		key    string
		wantID string
		wantOK bool
	}{
		{"valid", "tpp_ak_abcdefgh_" + secret, "abcdefgh", true},
		{"no prefix", "abcdefgh_" + secret, "", false},
		{"wrong prefix", "tpp_xx_abcdefgh_" + secret, "", false},
		{"short id", "tpp_ak_abcdefg_" + secret, "", false},
		{"short secret", "tpp_ak_abcdefgh_" + secret[1:], "", false},
		{"upper case", "tpp_ak_ABCDEFGH_" + secret, "", false},
		{"not base32", "tpp_ak_abcdefg1_" + secret, "", false},
		{"no separator", "tpp_ak_abcdefgh" + secret, "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id, ok := apikey.Parse(tt.key)
			if id != tt.wantID || ok != tt.wantOK {
				t.Errorf("Parse() = %q, %v; want %q, %v", id, ok, tt.wantID, tt.wantOK)
			}
		})
	}
}

func TestAllows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		have []string
		need string
		want bool
	}{
		{[]string{"read"}, "read", true},
		{[]string{"read"}, "tokens", false},
		{[]string{"read"}, "admin", false},
		{[]string{"tokens"}, "read", false},
		{[]string{"read", "tokens"}, "tokens", true},
		{[]string{"admin"}, "read", true},
		{[]string{"admin"}, "tokens", true},
		{[]string{"admin"}, "admin", true},
		{nil, "read", false},
	}
	for _, tt := range tests {
		if got := apikey.Allows(tt.have, tt.need); got != tt.want {
			t.Errorf("Allows(%v, %q) = %v, want %v", tt.have, tt.need, got, tt.want)
		}
	}
}
