package tppcrypto

import (
	"encoding/binary"
	"fmt"
	"time"
)

// FrameVersion is the plaintext frame version (spec §6).
const FrameVersion = 0x01

// maxFrameString is the ceiling of the u16 length prefixes carrying
// content_type and filename.
const maxFrameString = 0xFFFF

// Frame is the plaintext structure the AEAD protects (spec §6). It carries the
// content type and filename, so neither ever reaches the server (SPEC §2.3).
//
// The encoding is a fixed binary layout rather than protobuf: /proto is a
// shared contract owned by another phase, and the server never parses this
// structure — it only ever holds the ciphertext.
type Frame struct {
	// ContentType is an IANA media type, optionally with parameters. It must
	// not be empty; an unknown type is "application/octet-stream".
	ContentType string

	// Filename is a bare filename for file entries, empty when absent. It must
	// not contain a path separator, and a consumer must treat it as untrusted
	// display text, never as a path to write to.
	Filename string

	// CreatedAtUnixMs is the *client's* UTC clock and is advisory: the
	// server's own timestamp governs TTL and ordering (SPEC §4.5, §6).
	CreatedAtUnixMs uint64

	// PadLen is reserved for the plaintext padding SPEC §9 leaves open. An
	// encoder must write 0 and a decoder must reject anything else, so that
	// padding can arrive as tpp-crypto-v2 without ambiguity.
	PadLen uint32

	// Body is the clipboard payload.
	Body []byte
}

// CreatedAt returns CreatedAtUnixMs as a UTC time.
func (f Frame) CreatedAt() time.Time {
	return time.UnixMilli(int64(f.CreatedAtUnixMs)).UTC()
}

// Encode serializes a frame (spec §6). The encoding is canonical: one frame
// has exactly one valid serialization.
func (f Frame) Encode() ([]byte, error) {
	if f.ContentType == "" || len(f.ContentType) > maxFrameString {
		return nil, fmt.Errorf("tppcrypto: encode frame: %w: content_type is %d bytes", ErrMalformed, len(f.ContentType))
	}
	if len(f.Filename) > maxFrameString {
		return nil, fmt.Errorf("tppcrypto: encode frame: %w: filename is %d bytes", ErrMalformed, len(f.Filename))
	}
	if f.PadLen != 0 {
		return nil, fmt.Errorf("tppcrypto: encode frame: %w", ErrPadNonZero)
	}
	out := make([]byte, 0, 1+2+len(f.ContentType)+2+len(f.Filename)+8+4+4+len(f.Body))
	out = append(out, FrameVersion)
	out = binary.BigEndian.AppendUint16(out, uint16(len(f.ContentType)))
	out = append(out, f.ContentType...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(f.Filename)))
	out = append(out, f.Filename...)
	out = binary.BigEndian.AppendUint64(out, f.CreatedAtUnixMs)
	out = binary.BigEndian.AppendUint32(out, f.PadLen)
	out = binary.BigEndian.AppendUint32(out, uint32(len(f.Body)))
	return append(out, f.Body...), nil
}

// DecodeFrame parses a serialized frame. It rejects a length prefix that runs
// past the end and any trailing byte after the padding, which is what makes
// the encoding canonical.
func DecodeFrame(b []byte) (Frame, error) {
	var f Frame
	r := frameReader{b: b}

	ver, err := r.u8()
	if err != nil {
		return f, fmt.Errorf("tppcrypto: decode frame version: %w", err)
	}
	if ver != FrameVersion {
		return f, fmt.Errorf("tppcrypto: decode frame: %w", ErrVersion)
	}
	if f.ContentType, err = r.str16(); err != nil {
		return f, fmt.Errorf("tppcrypto: decode frame content_type: %w", err)
	}
	if f.ContentType == "" {
		return f, fmt.Errorf("tppcrypto: decode frame content_type: %w: empty", ErrMalformed)
	}
	if f.Filename, err = r.str16(); err != nil {
		return f, fmt.Errorf("tppcrypto: decode frame filename: %w", err)
	}
	if f.CreatedAtUnixMs, err = r.u64(); err != nil {
		return f, fmt.Errorf("tppcrypto: decode frame created_at: %w", err)
	}
	if f.PadLen, err = r.u32(); err != nil {
		return f, fmt.Errorf("tppcrypto: decode frame pad_len: %w", err)
	}
	if f.PadLen != 0 {
		return f, fmt.Errorf("tppcrypto: decode frame: %w", ErrPadNonZero)
	}
	bodyLen, err := r.u32()
	if err != nil {
		return f, fmt.Errorf("tppcrypto: decode frame body_len: %w", err)
	}
	body, err := r.bytes(int(bodyLen))
	if err != nil {
		return f, fmt.Errorf("tppcrypto: decode frame body: %w", err)
	}
	// The frame is handed to callers that keep it; it must not alias the
	// caller's buffer.
	f.Body = append([]byte(nil), body...)
	if r.i != len(r.b) {
		return f, fmt.Errorf("tppcrypto: decode frame: %w: %d trailing bytes", ErrMalformed, len(r.b)-r.i)
	}
	return f, nil
}

// frameReader is a bounds-checked cursor over an encoded frame.
type frameReader struct {
	b []byte
	i int
}

func (r *frameReader) bytes(n int) ([]byte, error) {
	if n < 0 || len(r.b)-r.i < n {
		return nil, fmt.Errorf("%w: want %d bytes, %d remain", ErrMalformed, n, len(r.b)-r.i)
	}
	out := r.b[r.i : r.i+n]
	r.i += n
	return out, nil
}

func (r *frameReader) u8() (byte, error) {
	b, err := r.bytes(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (r *frameReader) u32() (uint32, error) {
	b, err := r.bytes(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func (r *frameReader) u64() (uint64, error) {
	b, err := r.bytes(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b), nil
}

func (r *frameReader) str16() (string, error) {
	n, err := r.bytes(2)
	if err != nil {
		return "", err
	}
	s, err := r.bytes(int(binary.BigEndian.Uint16(n)))
	if err != nil {
		return "", err
	}
	return string(s), nil
}
