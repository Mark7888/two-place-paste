package main

import (
	"encoding/binary"
	"fmt"
)

// flip returns a copy of b with the bit 0x01 toggled at index i. It is how
// every "tampered" negative vector is produced: the smallest possible change.
func flip(b []byte, i int) []byte {
	out := append([]byte(nil), b...)
	out[i] ^= 0x01
	return out
}

func buildFailure() ([]byte, int, error) {
	desktop, phone, _, err := devices()
	if err != nil {
		return nil, 0, err
	}

	// A valid wrap to the desktop device at epoch 1, mutated below.
	groupKey := det("group-key/epoch-1", GroupKeySize)
	ephPriv := det("device/ephemeral/creation", X25519KeySize)
	wrapped, err := Wrap(groupKey, desktop.pub, ephPriv, 1)
	if err != nil {
		return nil, 0, err
	}

	// A valid entry, mutated below.
	c := frameCases()[0]
	nonce := det(c.nonceLbl, NonceSize)
	encoded, err := c.frame.Encode()
	if err != nil {
		return nil, 0, err
	}
	container, err := SealEntry(groupKey, nonce, c.epoch, c.entryID, encoded)
	if err != nil {
		return nil, 0, err
	}

	vs := []failureVector{
		{
			Name:             "unwrap-wrong-recipient",
			Description:      "a wrapped key addressed to another device must not open",
			Operation:        "unwrap",
			Reason:           "the AEAD key is derived from the recipient's own public key",
			Epoch:            1,
			WrappedKey:       hexs(wrapped),
			RecipientPrivate: hexs(phone.priv),
		},
		{
			Name:             "unwrap-wrong-epoch",
			Description:      "SPEC §3.3: a wrapped key replayed at a different epoch must not open",
			Operation:        "unwrap",
			Reason:           "the epoch is bound into the AAD",
			Epoch:            2,
			WrappedKey:       hexs(wrapped),
			RecipientPrivate: hexs(desktop.priv),
		},
		{
			Name:             "unwrap-tampered-ciphertext",
			Description:      "one flipped bit in the wrapped group key",
			Operation:        "unwrap",
			Reason:           "Poly1305 authentication fails",
			Epoch:            1,
			WrappedKey:       hexs(flip(wrapped, 1+X25519KeySize)),
			RecipientPrivate: hexs(desktop.priv),
		},
		{
			Name:             "unwrap-tampered-ephemeral-key",
			Description:      "one flipped bit in the ephemeral public key",
			Operation:        "unwrap",
			Reason:           "a different shared secret derives a different AEAD key",
			Epoch:            1,
			WrappedKey:       hexs(flip(wrapped, 1)),
			RecipientPrivate: hexs(desktop.priv),
		},
		{
			Name:             "unwrap-unknown-version",
			Description:      "version byte 0x02",
			Operation:        "unwrap",
			Reason:           "an unknown container version is rejected before any key material is touched",
			Epoch:            1,
			WrappedKey:       hexs(append([]byte{0x02}, wrapped[1:]...)),
			RecipientPrivate: hexs(desktop.priv),
		},
		{
			Name:             "unwrap-truncated",
			Description:      "the last byte of the tag removed",
			Operation:        "unwrap",
			Reason:           "a wrapped key has exactly one valid length",
			Epoch:            1,
			WrappedKey:       hexs(wrapped[:len(wrapped)-1]),
			RecipientPrivate: hexs(desktop.priv),
		},
		{
			Name:        "entry-wrong-epoch",
			Description: "SPEC §3.3: an entry from another epoch must not open, it must be skipped",
			Operation:   "open_entry",
			Reason:      "the epoch feeds both the HKDF info and the AAD",
			GroupKey:    hexs(groupKey),
			Epoch:       2,
			EntryID:     c.entryID,
			Container:   hexs(container),
		},
		{
			Name:        "entry-wrong-entry-id",
			Description: "the same ciphertext presented under another entry id",
			Operation:   "open_entry",
			Reason:      "entry_id is bound into the AAD, so an entry cannot be relabelled",
			GroupKey:    hexs(groupKey),
			Epoch:       c.epoch,
			EntryID:     "01J9ZQK8N4X0000000000000ZZ",
			Container:   hexs(container),
		},
		{
			Name:        "entry-tampered-ciphertext",
			Description: "one flipped bit in the ciphertext",
			Operation:   "open_entry",
			Reason:      "Poly1305 authentication fails",
			GroupKey:    hexs(groupKey),
			Epoch:       c.epoch,
			EntryID:     c.entryID,
			Container:   hexs(flip(container, 1+NonceSize)),
		},
		{
			Name:        "entry-tampered-nonce",
			Description: "one flipped bit in the nonce",
			Operation:   "open_entry",
			Reason:      "the nonce is the HKDF salt, so a different nonce derives a different content key",
			GroupKey:    hexs(groupKey),
			Epoch:       c.epoch,
			EntryID:     c.entryID,
			Container:   hexs(flip(container, 1)),
		},
		{
			Name:        "entry-truncated-tag",
			Description: "the last byte of the tag removed",
			Operation:   "open_entry",
			Reason:      "a truncated container never authenticates",
			GroupKey:    hexs(groupKey),
			Epoch:       c.epoch,
			EntryID:     c.entryID,
			Container:   hexs(container[:len(container)-1]),
		},
		{
			Name:        "entry-unknown-version",
			Description: "version byte 0x02",
			Operation:   "open_entry",
			Reason:      "an unknown container version is rejected before any key material is touched",
			GroupKey:    hexs(groupKey),
			Epoch:       c.epoch,
			EntryID:     c.entryID,
			Container:   hexs(append([]byte{0x02}, container[1:]...)),
		},
		{
			Name:        "entry-wrong-group-key",
			Description: "an entry from another group, or from before a rekey the client already applied",
			Operation:   "open_entry",
			Reason:      "the content key is derived from the group key",
			GroupKey:    hexs(det("group-key/epoch-7", GroupKeySize)),
			Epoch:       c.epoch,
			EntryID:     c.entryID,
			Container:   hexs(container),
		},
		{
			Name:        "frame-trailing-bytes",
			Description: "a well-formed frame with one extra byte appended",
			Operation:   "decode_frame",
			Reason:      "the frame encoding is canonical; trailing bytes are rejected",
			Encoded:     hexs(append(append([]byte(nil), encoded...), 0x00)),
		},
		{
			Name:        "frame-truncated-body",
			Description: "body_len promises more bytes than the frame carries",
			Operation:   "decode_frame",
			Reason:      "length prefixes are validated against the remaining input",
			Encoded:     hexs(encoded[:len(encoded)-1]),
		},
		{
			Name:        "frame-unknown-version",
			Description: "frame version byte 0x02",
			Operation:   "decode_frame",
			Reason:      "an unknown frame version is rejected",
			Encoded:     hexs(append([]byte{0x02}, encoded[1:]...)),
		},
		{
			Name:        "frame-pad-len-nonzero",
			Description: "pad_len is reserved and MUST be zero in v1",
			Operation:   "decode_frame",
			Reason:      "padding is specified but not implemented (SPEC §9); a non-zero pad_len is a future format",
			Encoded:     hexs(withPadLen(encoded, c.frame, 4)),
		},
		{
			Name:        "frame-empty-content-type",
			Description: "a zero-length content type",
			Operation:   "decode_frame",
			Reason:      "content_type is mandatory",
			Encoded:     hexs(emptyContentType(c.frame)),
		},
	}

	for i := range vs {
		vs[i].Expect = "error"
		if vs[i].Epoch != 0 {
			vs[i].EpochHex = epochHex(vs[i].Epoch)
		}
		if err := assertFails(vs[i]); err != nil {
			return nil, 0, err
		}
	}

	body, err := marshal(suite[failureVector]{
		SchemaVersion: schemaVersion,
		Profile:       profile,
		Suite:         "failure",
		Spec:          "spec/crypto.md §7",
		Description: "Inputs every implementation must reject. `operation` names the call under " +
			"test; the only correct outcome is an error. An implementation that returns " +
			"plaintext for any of these is broken, not lenient.",
		Vectors: vs,
	})
	return body, len(vs), err
}

// withPadLen re-encodes a frame with a non-zero pad_len and the matching zero
// padding appended, which Encode itself refuses to produce.
func withPadLen(encoded []byte, f Frame, pad uint32) []byte {
	// pad_len sits 12 bytes before the end of the header: u32 pad_len, u32
	// body_len, then the body. Rebuild rather than patch, to stay readable.
	out := make([]byte, 0, len(encoded)+int(pad))
	out = append(out, frameV1)
	out = binary.BigEndian.AppendUint16(out, uint16(len(f.ContentType)))
	out = append(out, f.ContentType...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(f.Filename)))
	out = append(out, f.Filename...)
	out = binary.BigEndian.AppendUint64(out, f.CreatedAtUnix)
	out = binary.BigEndian.AppendUint32(out, pad)
	out = binary.BigEndian.AppendUint32(out, uint32(len(f.Body)))
	out = append(out, f.Body...)
	return append(out, make([]byte, pad)...)
}

// emptyContentType encodes a frame whose content type is zero-length.
func emptyContentType(f Frame) []byte {
	out := []byte{frameV1}
	out = binary.BigEndian.AppendUint16(out, 0)
	out = binary.BigEndian.AppendUint16(out, uint16(len(f.Filename)))
	out = append(out, f.Filename...)
	out = binary.BigEndian.AppendUint64(out, f.CreatedAtUnix)
	out = binary.BigEndian.AppendUint32(out, 0)
	out = binary.BigEndian.AppendUint32(out, uint32(len(f.Body)))
	return append(out, f.Body...)
}

// assertFails runs a negative vector through the reference implementation and
// reports if it unexpectedly succeeds. A negative vector that passes is worse
// than no vector at all.
func assertFails(v failureVector) error {
	unhex := func(s string) []byte {
		b, err := hexDecode(s)
		if err != nil {
			panic(fmt.Sprintf("vector %s: %v", v.Name, err))
		}
		return b
	}
	var err error
	switch v.Operation {
	case "unwrap":
		_, err = Unwrap(unhex(v.WrappedKey), unhex(v.RecipientPrivate), v.Epoch)
	case "open_entry":
		_, err = OpenEntry(unhex(v.GroupKey), unhex(v.Container), v.Epoch, v.EntryID)
	case "decode_frame":
		_, err = DecodeFrame(unhex(v.Encoded))
	default:
		return fmt.Errorf("vector %s: unknown operation %q", v.Name, v.Operation)
	}
	if err == nil {
		return fmt.Errorf("vector %s: expected an error, got success", v.Name)
	}
	return nil
}
