package tppcrypto

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// vectorsDir is /spec/vectors relative to this package. The corpus is a shared
// contract (ROADMAP §3): this phase reads it and never edits it.
const vectorsDir = "../../../spec/vectors"

// hexString unmarshals the lowercase hex the corpus is written in.
type hexString []byte

func (h *hexString) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return err
	}
	*h = raw
	return nil
}

// epochField carries the u64be encoding of an epoch. epoch_hex is
// authoritative: one vector uses 2^64-1, which does not survive a JSON number
// in every language (spec §9.1).
type epochField struct {
	Hex hexString `json:"epoch_hex"`
}

func (e epochField) value(t *testing.T) uint64 {
	t.Helper()
	if len(e.Hex) != 8 {
		t.Fatalf("epoch_hex is %d bytes, want 8", len(e.Hex))
	}
	var v uint64
	for _, b := range e.Hex {
		v = v<<8 | uint64(b)
	}
	return v
}

func loadSuite(t *testing.T, file string, out any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(vectorsDir, file))
	if err != nil {
		t.Fatalf("read vector suite %s: %v", file, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode vector suite %s: %v", file, err)
	}
}

// TestVectorCorpusIsFullyCovered fails when the corpus grows a suite or a
// vector this package does not run. Without it, a contract-change PR could add
// a case and leave the Go client silently unvalidated (spec §9.1).
func TestVectorCorpusIsFullyCovered(t *testing.T) {
	var index struct {
		TotalVectors int `json:"total_vectors"`
		Suites       []struct {
			File    string `json:"file"`
			Vectors int    `json:"vectors"`
		} `json:"suites"`
	}
	loadSuite(t, "index.json", &index)

	// Every suite below is exercised by one test in this file.
	covered := map[string]bool{
		"x25519.json":  true,
		"wrap.json":    true,
		"derive.json":  true,
		"frame.json":   true,
		"entry.json":   true,
		"failure.json": true,
	}
	total := 0
	for _, s := range index.Suites {
		if !covered[s.File] {
			t.Errorf("vector suite %s is not run by any test in this package", s.File)
		}
		total += s.Vectors
	}
	if total != index.TotalVectors {
		t.Errorf("suite counts sum to %d, index says %d", total, index.TotalVectors)
	}
}

func TestX25519Vectors(t *testing.T) {
	var suite struct {
		Vectors []struct {
			Name          string    `json:"name"`
			PrivateKey    hexString `json:"private_key"`
			PublicKey     hexString `json:"public_key"`
			PeerPublicKey hexString `json:"peer_public_key"`
			SharedSecret  hexString `json:"shared_secret"`
		} `json:"vectors"`
	}
	loadSuite(t, "x25519.json", &suite)
	if len(suite.Vectors) == 0 {
		t.Fatal("x25519 suite is empty")
	}

	for _, v := range suite.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			pub, err := PublicKey(v.PrivateKey)
			if err != nil {
				t.Fatalf("PublicKey: %v", err)
			}
			if !bytes.Equal(pub, v.PublicKey) {
				t.Errorf("public key = %x, want %x", pub, v.PublicKey)
			}
			if len(v.PeerPublicKey) == 0 {
				return
			}
			shared, err := SharedSecret(v.PrivateKey, v.PeerPublicKey)
			if err != nil {
				t.Fatalf("SharedSecret: %v", err)
			}
			if !bytes.Equal(shared, v.SharedSecret) {
				t.Errorf("shared secret = %x, want %x", shared, v.SharedSecret)
			}
		})
	}
}

func TestWrapVectors(t *testing.T) {
	var suite struct {
		Vectors []struct {
			epochField
			Name                string    `json:"name"`
			GroupKey            hexString `json:"group_key"`
			RecipientPrivateKey hexString `json:"recipient_private_key"`
			RecipientPublicKey  hexString `json:"recipient_public_key"`
			EphemeralPrivateKey hexString `json:"ephemeral_private_key"`
			EphemeralPublicKey  hexString `json:"ephemeral_public_key"`
			SharedSecret        hexString `json:"shared_secret"`
			HKDFInfo            hexString `json:"hkdf_info"`
			WrapKey             hexString `json:"wrap_key"`
			AAD                 hexString `json:"aad"`
			WrappedKey          hexString `json:"wrapped_key"`
		} `json:"vectors"`
	}
	loadSuite(t, "wrap.json", &suite)
	if len(suite.Vectors) == 0 {
		t.Fatal("wrap suite is empty")
	}

	for _, v := range suite.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			epoch := v.value(t)

			if got := WrapInfo(v.EphemeralPublicKey, v.RecipientPublicKey); !bytes.Equal(got, v.HKDFInfo) {
				t.Errorf("wrap info = %x, want %x", got, v.HKDFInfo)
			}
			if got := WrapAAD(epoch); !bytes.Equal(got, v.AAD) {
				t.Errorf("wrap aad = %x, want %x", got, v.AAD)
			}
			shared, err := SharedSecret(v.EphemeralPrivateKey, v.RecipientPublicKey)
			if err != nil {
				t.Fatalf("SharedSecret: %v", err)
			}
			if !bytes.Equal(shared, v.SharedSecret) {
				t.Errorf("shared secret = %x, want %x", shared, v.SharedSecret)
			}
			wrapKey, err := WrapKey(shared, v.EphemeralPublicKey, v.RecipientPublicKey)
			if err != nil {
				t.Fatalf("WrapKey: %v", err)
			}
			if !bytes.Equal(wrapKey, v.WrapKey) {
				t.Errorf("wrap key = %x, want %x", wrapKey, v.WrapKey)
			}

			// Replaying the recorded ephemeral key is the only way to
			// reproduce the container byte for byte; Wrap itself always draws
			// a fresh one.
			wrapped, err := wrapWith(v.GroupKey, v.RecipientPublicKey, v.EphemeralPrivateKey, epoch)
			if err != nil {
				t.Fatalf("wrap: %v", err)
			}
			if !bytes.Equal(wrapped, v.WrappedKey) {
				t.Errorf("wrapped key = %x, want %x", wrapped, v.WrappedKey)
			}
			if len(wrapped) != WrappedKeySize {
				t.Errorf("wrapped key is %d bytes, want %d", len(wrapped), WrappedKeySize)
			}

			groupKey, err := Unwrap(v.WrappedKey, v.RecipientPrivateKey, epoch)
			if err != nil {
				t.Fatalf("Unwrap: %v", err)
			}
			if !bytes.Equal(groupKey, v.GroupKey) {
				t.Errorf("unwrapped group key = %x, want %x", groupKey, v.GroupKey)
			}
		})
	}
}

func TestDeriveVectors(t *testing.T) {
	var suite struct {
		Vectors []struct {
			epochField
			Name       string    `json:"name"`
			GroupKey   hexString `json:"group_key"`
			Nonce      hexString `json:"nonce"`
			HKDFSalt   hexString `json:"hkdf_salt"`
			HKDFInfo   hexString `json:"hkdf_info"`
			ContentKey hexString `json:"content_key"`
		} `json:"vectors"`
	}
	loadSuite(t, "derive.json", &suite)
	if len(suite.Vectors) == 0 {
		t.Fatal("derive suite is empty")
	}

	for _, v := range suite.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			epoch := v.value(t)
			if got := EntryInfo(epoch); !bytes.Equal(got, v.HKDFInfo) {
				t.Errorf("entry info = %x, want %x", got, v.HKDFInfo)
			}
			if !bytes.Equal(v.Nonce, v.HKDFSalt) {
				t.Errorf("vector's salt %x is not its nonce %x", v.HKDFSalt, v.Nonce)
			}
			key, err := ContentKey(v.GroupKey, v.Nonce, epoch)
			if err != nil {
				t.Fatalf("ContentKey: %v", err)
			}
			if !bytes.Equal(key, v.ContentKey) {
				t.Errorf("content key = %x, want %x", key, v.ContentKey)
			}
		})
	}
}

// frameVector is the JSON shape of a frame in both frame.json and entry.json.
type frameVector struct {
	ContentType     string    `json:"content_type"`
	Filename        string    `json:"filename"`
	CreatedAtUnixMs uint64    `json:"created_at_unix_ms"`
	PadLen          uint32    `json:"pad_len"`
	Body            hexString `json:"body"`
}

func (f frameVector) frame() Frame {
	return Frame{
		ContentType:     f.ContentType,
		Filename:        f.Filename,
		CreatedAtUnixMs: f.CreatedAtUnixMs,
		PadLen:          f.PadLen,
		Body:            f.Body,
	}
}

func TestFrameVectors(t *testing.T) {
	var suite struct {
		Vectors []struct {
			Name    string      `json:"name"`
			Frame   frameVector `json:"frame"`
			Encoded hexString   `json:"encoded"`
		} `json:"vectors"`
	}
	loadSuite(t, "frame.json", &suite)
	if len(suite.Vectors) == 0 {
		t.Fatal("frame suite is empty")
	}

	for _, v := range suite.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			encoded, err := v.Frame.frame().Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !bytes.Equal(encoded, v.Encoded) {
				t.Errorf("encoded frame = %x, want %x", encoded, v.Encoded)
			}
			decoded, err := DecodeFrame(v.Encoded)
			if err != nil {
				t.Fatalf("DecodeFrame: %v", err)
			}
			want := v.Frame.frame()
			if decoded.ContentType != want.ContentType || decoded.Filename != want.Filename ||
				decoded.CreatedAtUnixMs != want.CreatedAtUnixMs || decoded.PadLen != want.PadLen ||
				!bytes.Equal(decoded.Body, want.Body) {
				t.Errorf("decoded frame = %+v, want %+v", decoded, want)
			}
		})
	}
}

func TestEntryVectors(t *testing.T) {
	var suite struct {
		Vectors []struct {
			epochField
			Name         string      `json:"name"`
			GroupKey     hexString   `json:"group_key"`
			EntryID      string      `json:"entry_id"`
			Nonce        hexString   `json:"nonce"`
			ContentKey   hexString   `json:"content_key"`
			Frame        frameVector `json:"frame"`
			EncodedFrame hexString   `json:"encoded_frame"`
			AAD          hexString   `json:"aad"`
			Container    hexString   `json:"container"`
		} `json:"vectors"`
	}
	loadSuite(t, "entry.json", &suite)
	if len(suite.Vectors) == 0 {
		t.Fatal("entry suite is empty")
	}

	for _, v := range suite.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			epoch := v.value(t)
			if got := EntryAAD(epoch, v.EntryID); !bytes.Equal(got, v.AAD) {
				t.Errorf("entry aad = %x, want %x", got, v.AAD)
			}

			frame, err := v.Frame.frame().Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !bytes.Equal(frame, v.EncodedFrame) {
				t.Errorf("encoded frame = %x, want %x", frame, v.EncodedFrame)
			}

			container, err := SealEntry(v.GroupKey, v.Nonce, epoch, v.EntryID, frame)
			if err != nil {
				t.Fatalf("SealEntry: %v", err)
			}
			if !bytes.Equal(container, v.Container) {
				t.Errorf("container = %x, want %x", container, v.Container)
			}

			opened, err := OpenEntry(v.GroupKey, v.Container, epoch, v.EntryID)
			if err != nil {
				t.Fatalf("OpenEntry: %v", err)
			}
			if !bytes.Equal(opened, v.EncodedFrame) {
				t.Errorf("opened frame = %x, want %x", opened, v.EncodedFrame)
			}
		})
	}
}

// TestFailureVectors is the important half of the corpus: for every input
// here, returning plaintext is a bug, not leniency (spec §9.1).
func TestFailureVectors(t *testing.T) {
	var suite struct {
		Vectors []struct {
			epochField
			Name                string    `json:"name"`
			Operation           string    `json:"operation"`
			Expect              string    `json:"expect"`
			Reason              string    `json:"reason"`
			RecipientPrivateKey hexString `json:"recipient_private_key"`
			WrappedKey          hexString `json:"wrapped_key"`
			GroupKey            hexString `json:"group_key"`
			EntryID             string    `json:"entry_id"`
			Container           hexString `json:"container"`
			EncodedFrame        hexString `json:"encoded_frame"`
		} `json:"vectors"`
	}
	loadSuite(t, "failure.json", &suite)
	if len(suite.Vectors) == 0 {
		t.Fatal("failure suite is empty")
	}

	for _, v := range suite.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			if v.Expect != "error" {
				t.Fatalf("failure vector expects %q, want \"error\"", v.Expect)
			}
			var err error
			switch v.Operation {
			case "unwrap":
				_, err = Unwrap(v.WrappedKey, v.RecipientPrivateKey, v.value(t))
			case "open_entry":
				_, err = OpenEntry(v.GroupKey, v.Container, v.value(t), v.EntryID)
			case "decode_frame":
				_, err = DecodeFrame(v.EncodedFrame)
			default:
				t.Fatalf("unknown failure operation %q; this package must be taught to run it", v.Operation)
			}
			if err == nil {
				t.Fatalf("%s succeeded but must fail: %s", v.Operation, v.Reason)
			}
			// Every rejection maps onto one of the profile's error kinds, so a
			// caller can tell a corrupt container from a wrong key.
			switch {
			case errors.Is(err, ErrVersion), errors.Is(err, ErrMalformed),
				errors.Is(err, ErrAuth), errors.Is(err, ErrPadNonZero):
			default:
				t.Errorf("error %v is none of the profile's error kinds", err)
			}
		})
	}
}
