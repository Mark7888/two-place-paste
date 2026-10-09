package update

import (
	"crypto/ed25519"
	"crypto/x509"
	_ "embed"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
)

// signingKeyPEM is the public half of the UPDATE_SIGNING_KEY secret CI signs
// manifests with. It is committed rather than injected at build time so that
// a change to it shows up in review (plan §3.6).
//
//go:embed update-signing.pub.pem
var signingKeyPEM []byte

var (
	signingKeyOnce sync.Once
	signingKey     ed25519.PublicKey
	errSigningKey  error
)

// SigningKey is the key every manifest must be signed with.
func SigningKey() (ed25519.PublicKey, error) {
	signingKeyOnce.Do(func() {
		signingKey, errSigningKey = parsePublicKey(signingKeyPEM)
	})
	return signingKey, errSigningKey
}

func parsePublicKey(b []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, errors.New("update: the signing key is not a PEM public key")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("update: parse the signing key: %w", err)
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("update: the signing key is a %T, not Ed25519", k)
	}
	return pub, nil
}
