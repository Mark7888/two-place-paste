package sensitive

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// An exact-match filter protects one specific secret, typically a password,
// without the settings file holding it: only a salted argon2id hash is kept.
//
// argon2id rather than a plain SHA-256 because the settings file is an
// ordinary readable file, and a fast hash of a short password is a fast
// brute force for whoever reads it. The parameters are OWASP's argon2id
// baseline (19 MiB, two passes, one lane): a few tens of milliseconds per
// clipboard change.
//
// One salt for the whole install, not one per filter, is what keeps that cost
// flat: a clipboard change is hashed once and compared against every exact
// filter, however many there are.
const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonKeyLen    = 32

	// SaltBytes is the length of the install-wide salt.
	SaltBytes = 16

	// MaxExactBytes is the longest text an exact filter is compared with.
	// Secrets are short; a longer text cannot be one, and is not hashed.
	MaxExactBytes = 1 << 10
)

// hashPrefix names the algorithm and parameters, so that changing them later
// can tell an old hash from a new one rather than silently never matching.
var hashPrefix = fmt.Sprintf("argon2id$m=%d,t=%d,p=%d$", argonMemoryKiB, argonTime, argonThreads)

// NewSalt returns a fresh install-wide salt, base64-encoded for the settings
// file.
func NewSalt() (string, error) {
	b := make([]byte, SaltBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate a filter salt: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// DecodeSalt parses a salt from the settings file.
func DecodeSalt(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != SaltBytes {
		return nil, fmt.Errorf("%w: the filter salt is not %d base64-encoded bytes", ErrInvalid, SaltBytes)
	}
	return b, nil
}

// HashExact hashes a secret for an exact filter. The secret is trimmed of
// surrounding whitespace, as the content it is compared with is. It returns
// "" for a secret that could never match: empty, or longer than
// MaxExactBytes.
func HashExact(salt []byte, secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" || len(secret) > MaxExactBytes {
		return ""
	}
	key := argon2.IDKey([]byte(secret), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return hashPrefix + base64.RawStdEncoding.EncodeToString(key)
}

func validHash(h string) bool {
	rest, ok := strings.CutPrefix(h, hashPrefix)
	if !ok {
		return false
	}
	b, err := base64.RawStdEncoding.DecodeString(rest)
	return err == nil && len(b) == argonKeyLen
}
