// Command gen produces the cross-language crypto test vectors in
// spec/vectors from the reference implementation in crypto.go.
//
// Usage:
//
//	GOWORK=off go run ./spec/vectors/gen -out spec/vectors        # regenerate
//	GOWORK=off go run ./spec/vectors/gen -out spec/vectors -check # verify
//
// -check recomputes every suite and exits non-zero if a committed file
// differs, which is the guard against a hand-edited vector.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	out := flag.String("out", "spec/vectors", "directory to write the vector files into")
	check := flag.Bool("check", false, "verify the committed files instead of writing them")
	flag.Parse()

	if err := run(*out, *check); err != nil {
		fmt.Fprintf(os.Stderr, "gen: %v\n", err)
		os.Exit(1)
	}
}

func run(dir string, check bool) error {
	files, err := build()
	if err != nil {
		return err
	}
	var problems []error
	for name, body := range files {
		path := filepath.Join(dir, name)
		if check {
			have, err := os.ReadFile(path)
			if err != nil {
				problems = append(problems, fmt.Errorf("read %s: %w", path, err))
				continue
			}
			if !bytes.Equal(have, body) {
				problems = append(problems, fmt.Errorf("%s is stale: regenerate with 'go run ./spec/vectors/gen'", path))
			}
			continue
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			problems = append(problems, fmt.Errorf("write %s: %w", path, err))
		}
	}
	if err := errors.Join(problems...); err != nil {
		return err
	}
	if !check {
		fmt.Printf("wrote %d vector files to %s\n", len(files), dir)
	}
	return nil
}

// ---------------------------------------------------------------------------
// JSON shapes
// ---------------------------------------------------------------------------

// schemaVersion is bumped whenever the JSON shape changes in a way an existing
// consumer would not survive.
const schemaVersion = 1

type suite[T any] struct {
	SchemaVersion int    `json:"schema_version"`
	Profile       string `json:"profile"`
	Suite         string `json:"suite"`
	Spec          string `json:"spec"`
	Description   string `json:"description"`
	Vectors       []T    `json:"vectors"`
}

type x25519Vector struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	PrivateKey    string `json:"private_key"`
	PublicKey     string `json:"public_key"`
	PeerPublicKey string `json:"peer_public_key,omitempty"`
	SharedSecret  string `json:"shared_secret,omitempty"`
}

type wrapVector struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	Epoch            uint64 `json:"epoch"`
	EpochHex         string `json:"epoch_hex"`
	GroupKey         string `json:"group_key"`
	RecipientPrivate string `json:"recipient_private_key"`
	RecipientPublic  string `json:"recipient_public_key"`
	EphemeralPrivate string `json:"ephemeral_private_key"`
	EphemeralPublic  string `json:"ephemeral_public_key"`
	SharedSecret     string `json:"shared_secret"`
	HKDFInfo         string `json:"hkdf_info"`
	WrapKey          string `json:"wrap_key"`
	AAD              string `json:"aad"`
	WrappedKey       string `json:"wrapped_key"`
}

type deriveVector struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	GroupKey    string `json:"group_key"`
	Epoch       uint64 `json:"epoch"`
	EpochHex    string `json:"epoch_hex"`
	Nonce       string `json:"nonce"`
	HKDFSalt    string `json:"hkdf_salt"`
	HKDFInfo    string `json:"hkdf_info"`
	ContentKey  string `json:"content_key"`
}

type frameFields struct {
	ContentType     string `json:"content_type"`
	Filename        string `json:"filename"`
	CreatedAtUnixMs uint64 `json:"created_at_unix_ms"`
	PadLen          uint32 `json:"pad_len"`
	Body            string `json:"body"`
}

type frameVector struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Frame       frameFields `json:"frame"`
	Encoded     string      `json:"encoded"`
}

type entryVector struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	GroupKey    string      `json:"group_key"`
	Epoch       uint64      `json:"epoch"`
	EpochHex    string      `json:"epoch_hex"`
	EntryID     string      `json:"entry_id"`
	Nonce       string      `json:"nonce"`
	ContentKey  string      `json:"content_key"`
	Frame       frameFields `json:"frame"`
	Encoded     string      `json:"encoded_frame"`
	AAD         string      `json:"aad"`
	Container   string      `json:"container"`
}

type failureVector struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	Operation        string `json:"operation"`
	Expect           string `json:"expect"`
	Reason           string `json:"reason"`
	GroupKey         string `json:"group_key,omitempty"`
	Epoch            uint64 `json:"epoch,omitempty"`
	EpochHex         string `json:"epoch_hex,omitempty"`
	EntryID          string `json:"entry_id,omitempty"`
	Container        string `json:"container,omitempty"`
	RecipientPrivate string `json:"recipient_private_key,omitempty"`
	WrappedKey       string `json:"wrapped_key,omitempty"`
	Encoded          string `json:"encoded_frame,omitempty"`
}

type manifestSuite struct {
	File        string `json:"file"`
	Suite       string `json:"suite"`
	Vectors     int    `json:"vectors"`
	Description string `json:"description"`
}

type manifest struct {
	SchemaVersion int             `json:"schema_version"`
	Profile       string          `json:"profile"`
	Spec          string          `json:"spec"`
	Description   string          `json:"description"`
	Total         int             `json:"total_vectors"`
	Suites        []manifestSuite `json:"suites"`
}

const profile = "tpp-crypto-v1"

func hexs(b []byte) string { return hex.EncodeToString(b) }

// epochHex renders an epoch as its 8-byte big-endian encoding. It is the
// authoritative form: `epoch` is also emitted as a JSON number for readability,
// but a u64 above 2^53 does not survive JavaScript's JSON.parse, so consumers
// read this field.
func epochHex(epoch uint64) string {
	return hexs(binary.BigEndian.AppendUint64(nil, epoch))
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("marshal vectors: %w", err)
	}
	return buf.Bytes(), nil
}

func hexDecode(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode hex: %w", err)
	}
	return b, nil
}
