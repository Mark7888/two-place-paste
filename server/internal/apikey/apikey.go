// Package apikey mints and checks the API keys of the admin REST API
// (docs/internal/plans/admin-rest-api.md D3-D8). The admin page and the
// `tpp apikey` command both create keys through it, and the relay checks
// every API request with it.
//
// A key reads tpp_ak_<id>_<secret>. The fixed prefix lets secret scanners
// recognise a leaked key; the id is public and names the key in lists, logs
// and the audit log; the secret is 32 bytes from crypto/rand. Both are
// lowercase base32, so '_' never occurs inside them. The relay stores the
// SHA-256 of the whole key, never the key: with 256 bits of entropy a slow
// hash adds nothing.
package apikey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// Prefix starts every key.
const Prefix = "tpp_ak_"

const (
	idBytes     = 5  // 8 base32 characters
	secretBytes = 32 // 52 base32 characters
	idLen       = 8
	secretLen   = 52
)

// The scopes a key can hold (D6).
const (
	// Read allows every GET except creation-token links and QR codes.
	Read = "read"
	// Tokens allows listing, creating and revoking creation tokens, and
	// reading their links and QR codes.
	Tokens = "tokens"
	// Admin allows everything, and implies Read and Tokens.
	Admin = "admin"
)

// Scopes lists every scope, in the order they are shown.
var Scopes = []string{Read, Tokens, Admin}

// ExpiryDays are the lifetimes a key can be given, in days; 0 is never
// (D8). DefaultExpiryDays is what the admin page preselects.
var ExpiryDays = []int{30, 90, 365, 0}

// DefaultExpiryDays is the lifetime a new key gets unless another is chosen.
const DefaultExpiryDays = 90

// MaxNameLen bounds a key's name.
const MaxNameLen = 80

// Errors describing a key that cannot be created.
var (
	ErrName   = fmt.Errorf("give the key a name of at most %d characters", MaxNameLen)
	ErrScope  = errors.New("choose one or more scopes: read, tokens, admin")
	ErrExpiry = errors.New("choose a lifetime of 30, 90 or 365 days, or never")
)

var encoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// Allows reports whether a key holding have may do what need requires.
func Allows(have []string, need string) bool {
	return slices.Contains(have, need) || slices.Contains(have, Admin)
}

// CheckName trims a key's name and checks it: not empty, no control
// characters, at most MaxNameLen characters.
func CheckName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > MaxNameLen || !utf8.ValidString(name) {
		return "", ErrName
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", ErrName
		}
	}
	return name, nil
}

// CheckScopes checks a set of scopes and returns it without duplicates, in
// the order of Scopes.
func CheckScopes(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, ErrScope
	}
	for _, s := range in {
		if !slices.Contains(Scopes, s) {
			return nil, ErrScope
		}
	}
	out := make([]string, 0, len(Scopes))
	for _, s := range Scopes {
		if slices.Contains(in, s) {
			out = append(out, s)
		}
	}
	return out, nil
}

// ExpiresAt is when a key created at now with a lifetime of days expires;
// the zero time for 0, which is never.
func ExpiresAt(now time.Time, days int) (time.Time, error) {
	if !slices.Contains(ExpiryDays, days) {
		return time.Time{}, ErrExpiry
	}
	if days == 0 {
		return time.Time{}, nil
	}
	return now.UTC().Add(time.Duration(days) * 24 * time.Hour), nil
}

// New describes a key to create.
type New struct {
	Name   string
	Scopes []string

	// ExpiryDays is one of ExpiryDays.
	ExpiryDays int

	// CreatedBy is "admin" or "cli".
	CreatedBy string
}

// Create checks n, mints a key and stores it. It returns the key, which is
// shown once and stored nowhere, and its record. A store error such as
// store.ErrAPIKeyLimit is returned wrapped.
func Create(ctx context.Context, keys store.APIKeys, n New, now time.Time) (string, store.APIKey, error) {
	name, err := CheckName(n.Name)
	if err != nil {
		return "", store.APIKey{}, err
	}
	scopes, err := CheckScopes(n.Scopes)
	if err != nil {
		return "", store.APIKey{}, err
	}
	expires, err := ExpiresAt(now, n.ExpiryDays)
	if err != nil {
		return "", store.APIKey{}, err
	}
	// A random 40-bit id collides with one of at most 50 others about once
	// in twenty billion keys; retrying covers it rather than hoping.
	for range 3 {
		key, id := mint()
		rec := store.APIKey{
			ID:        id,
			Name:      name,
			Hash:      Hash(key),
			Scopes:    scopes,
			CreatedAt: now.UTC(),
			ExpiresAt: expires,
			CreatedBy: n.CreatedBy,
		}
		err := keys.CreateAPIKey(ctx, rec)
		if errors.Is(err, store.ErrAPIKeyExists) {
			continue
		}
		if err != nil {
			return "", store.APIKey{}, fmt.Errorf("store API key: %w", err)
		}
		return key, rec, nil
	}
	return "", store.APIKey{}, fmt.Errorf("store API key: %w", store.ErrAPIKeyExists)
}

// mint returns a fresh key and its id.
func mint() (key, id string) {
	var b [idBytes + secretBytes]byte
	// crypto/rand.Read never fails on a supported platform; a failure is a
	// broken machine (docs/internal/conventions.md §4), as in store.newID.
	if _, err := rand.Read(b[:]); err != nil {
		panic("apikey: crypto/rand: " + err.Error())
	}
	id = encoding.EncodeToString(b[:idBytes])
	return Prefix + id + "_" + encoding.EncodeToString(b[idBytes:]), id
}

// Parse returns the id of a well-formed key. It looks only at the form; the
// caller looks the id up and checks the key with Matches.
func Parse(key string) (id string, ok bool) {
	rest, ok := strings.CutPrefix(key, Prefix)
	if !ok {
		return "", false
	}
	id, secret, ok := strings.Cut(rest, "_")
	if !ok || len(id) != idLen || len(secret) != secretLen || !isBase32(id) || !isBase32(secret) {
		return "", false
	}
	return id, true
}

func isBase32(s string) bool {
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

// Hash is what the relay stores for a key.
func Hash(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

// Matches reports whether key is the key rec was created for, in constant
// time (docs/internal/conventions.md §10).
func Matches(rec store.APIKey, key string) bool {
	return subtle.ConstantTimeCompare(Hash(key), rec.Hash) == 1
}
