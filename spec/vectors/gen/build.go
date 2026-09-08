package main

import (
	"bytes"
	"fmt"
)

// build produces every vector file keyed by filename. Every positive vector is
// round-tripped and every negative vector is asserted to fail before it is
// written, so a corpus that generates at all is a corpus that is self-consistent.
func build() (map[string][]byte, error) {
	suites := []struct {
		file        string
		description string
		count       int
		name        string
		body        []byte
	}{}

	x, xn, err := buildX25519()
	if err != nil {
		return nil, err
	}
	suites = append(suites, struct {
		file        string
		description string
		count       int
		name        string
		body        []byte
	}{"x25519.json", "device and ephemeral X25519 keys, and the raw shared secret", xn, "x25519", x})

	w, wn, err := buildWrap()
	if err != nil {
		return nil, err
	}
	suites = append(suites, struct {
		file        string
		description string
		count       int
		name        string
		body        []byte
	}{"wrap.json", "group key wrapping to a device public key", wn, "wrap", w})

	d, dn, err := buildDerive()
	if err != nil {
		return nil, err
	}
	suites = append(suites, struct {
		file        string
		description string
		count       int
		name        string
		body        []byte
	}{"derive.json", "HKDF-SHA256 content key derivation", dn, "derive", d})

	f, fn, err := buildFrame()
	if err != nil {
		return nil, err
	}
	suites = append(suites, struct {
		file        string
		description string
		count       int
		name        string
		body        []byte
	}{"frame.json", "plaintext frame serialization", fn, "frame", f})

	e, en, err := buildEntry()
	if err != nil {
		return nil, err
	}
	suites = append(suites, struct {
		file        string
		description string
		count       int
		name        string
		body        []byte
	}{"entry.json", "end-to-end entry encryption", en, "entry", e})

	x2, xn2, err := buildFailure()
	if err != nil {
		return nil, err
	}
	suites = append(suites, struct {
		file        string
		description string
		count       int
		name        string
		body        []byte
	}{"failure.json", "inputs that every implementation must reject", xn2, "failure", x2})

	files := make(map[string][]byte, len(suites)+1)
	m := manifest{
		SchemaVersion: schemaVersion,
		Profile:       profile,
		Spec:          "spec/crypto.md",
		Description: "Cross-language test vectors for the TwoPlacePaste crypto profile. " +
			"All byte strings are lowercase hex. A client that cannot reproduce every " +
			"vector in this corpus does not ship (docs/conventions.md §11).",
	}
	for _, s := range suites {
		files[s.file] = s.body
		m.Total += s.count
		m.Suites = append(m.Suites, manifestSuite{File: s.file, Suite: s.name, Vectors: s.count, Description: s.description})
	}
	index, err := marshal(m)
	if err != nil {
		return nil, err
	}
	files["index.json"] = index
	return files, nil
}

// ---------------------------------------------------------------------------

// device holds the deterministic key material for one participant.
type device struct {
	name string
	priv []byte
	pub  []byte
}

func newDevice(name string) (device, error) {
	priv := det("device/"+name, X25519KeySize)
	pub, err := PublicKey(priv)
	if err != nil {
		return device{}, fmt.Errorf("device %s: %w", name, err)
	}
	return device{name: name, priv: priv, pub: pub}, nil
}

func devices() (desktop, phone, laptop device, err error) {
	if desktop, err = newDevice("desktop"); err != nil {
		return
	}
	if phone, err = newDevice("phone"); err != nil {
		return
	}
	laptop, err = newDevice("laptop")
	return
}

func buildX25519() ([]byte, int, error) {
	desktop, phone, laptop, err := devices()
	if err != nil {
		return nil, 0, err
	}
	eph, err := newDevice("ephemeral/pairing")
	if err != nil {
		return nil, 0, err
	}

	vs := []x25519Vector{
		{
			Name:        "device-desktop",
			Description: "public key derived from a device private key",
			PrivateKey:  hexs(desktop.priv),
			PublicKey:   hexs(desktop.pub),
		},
		{
			Name:        "device-phone",
			Description: "public key derived from a device private key",
			PrivateKey:  hexs(phone.priv),
			PublicKey:   hexs(phone.pub),
		},
		{
			Name:        "device-laptop",
			Description: "public key derived from a device private key",
			PrivateKey:  hexs(laptop.priv),
			PublicKey:   hexs(laptop.pub),
		},
		{
			Name:        "ephemeral-pairing",
			Description: "ephemeral key used by the wrap suite",
			PrivateKey:  hexs(eph.priv),
			PublicKey:   hexs(eph.pub),
		},
	}

	// Agreement, computed from both sides: the shared secret must match.
	ab, err := SharedSecret(desktop.priv, phone.pub)
	if err != nil {
		return nil, 0, err
	}
	ba, err := SharedSecret(phone.priv, desktop.pub)
	if err != nil {
		return nil, 0, err
	}
	if !bytes.Equal(ab, ba) {
		return nil, 0, fmt.Errorf("x25519 agreement is not symmetric")
	}
	vs = append(vs,
		x25519Vector{
			Name:          "agreement-desktop-to-phone",
			Description:   "raw X25519 shared secret, computed from the desktop side",
			PrivateKey:    hexs(desktop.priv),
			PublicKey:     hexs(desktop.pub),
			PeerPublicKey: hexs(phone.pub),
			SharedSecret:  hexs(ab),
		},
		x25519Vector{
			Name:          "agreement-phone-to-desktop",
			Description:   "the same shared secret, computed from the phone side",
			PrivateKey:    hexs(phone.priv),
			PublicKey:     hexs(phone.pub),
			PeerPublicKey: hexs(desktop.pub),
			SharedSecret:  hexs(ba),
		},
	)

	body, err := marshal(suite[x25519Vector]{
		SchemaVersion: schemaVersion,
		Profile:       profile,
		Suite:         "x25519",
		Spec:          "spec/crypto.md §3",
		Description:   "Device keypairs and raw X25519 agreement (RFC 7748).",
		Vectors:       vs,
	})
	return body, len(vs), err
}

// wrapCase describes one group key wrapping scenario.
type wrapCase struct {
	name        string
	description string
	epoch       uint64
	groupKeyLbl string
	ephLbl      string
	recipient   device
}

func wrapCases() ([]wrapCase, error) {
	desktop, phone, laptop, err := devices()
	if err != nil {
		return nil, err
	}
	return []wrapCase{
		{
			name:        "creation-self-wrap",
			description: "SPEC §3.1 step 4: the creating device wraps a fresh group key to itself at epoch 1",
			epoch:       1,
			groupKeyLbl: "group-key/epoch-1",
			ephLbl:      "ephemeral/creation",
			recipient:   desktop,
		},
		{
			name:        "pairing-wrap-to-joiner",
			description: "SPEC §3.2 step 4: the inviter wraps the current group key to the joiner",
			epoch:       1,
			groupKeyLbl: "group-key/epoch-1",
			ephLbl:      "ephemeral/pairing",
			recipient:   phone,
		},
		{
			name:        "rekey-wrap-after-revocation",
			description: "SPEC §3.3 step 3: a new group key wrapped to a remaining device at the incremented epoch",
			epoch:       7,
			groupKeyLbl: "group-key/epoch-7",
			ephLbl:      "ephemeral/rekey",
			recipient:   laptop,
		},
	}, nil
}

func buildWrap() ([]byte, int, error) {
	cases, err := wrapCases()
	if err != nil {
		return nil, 0, err
	}
	vs := make([]wrapVector, 0, len(cases))
	for _, c := range cases {
		groupKey := det(c.groupKeyLbl, GroupKeySize)
		ephPriv := det("device/"+c.ephLbl, X25519KeySize)
		ephPub, err := PublicKey(ephPriv)
		if err != nil {
			return nil, 0, err
		}
		shared, err := SharedSecret(ephPriv, c.recipient.pub)
		if err != nil {
			return nil, 0, err
		}
		wrapKey, err := WrapKey(shared, ephPub, c.recipient.pub)
		if err != nil {
			return nil, 0, err
		}
		wrapped, err := Wrap(groupKey, c.recipient.pub, ephPriv, c.epoch)
		if err != nil {
			return nil, 0, err
		}
		got, err := Unwrap(wrapped, c.recipient.priv, c.epoch)
		if err != nil {
			return nil, 0, fmt.Errorf("round-trip %s: %w", c.name, err)
		}
		if !bytes.Equal(got, groupKey) {
			return nil, 0, fmt.Errorf("round-trip %s: unwrapped key differs", c.name)
		}
		vs = append(vs, wrapVector{
			Name:             c.name,
			Description:      c.description,
			Epoch:            c.epoch,
			EpochHex:         epochHex(c.epoch),
			GroupKey:         hexs(groupKey),
			RecipientPrivate: hexs(c.recipient.priv),
			RecipientPublic:  hexs(c.recipient.pub),
			EphemeralPrivate: hexs(ephPriv),
			EphemeralPublic:  hexs(ephPub),
			SharedSecret:     hexs(shared),
			HKDFInfo:         hexs(WrapInfo(ephPub, c.recipient.pub)),
			WrapKey:          hexs(wrapKey),
			AAD:              hexs(WrapAAD(c.epoch)),
			WrappedKey:       hexs(wrapped),
		})
	}
	body, err := marshal(suite[wrapVector]{
		SchemaVersion: schemaVersion,
		Profile:       profile,
		Suite:         "wrap",
		Spec:          "spec/crypto.md §4",
		Description: "Group key wrapping. Given the ephemeral private key an implementation " +
			"must reproduce wrapped_key byte for byte; with a fresh ephemeral key it must " +
			"still produce a blob the recipient can unwrap.",
		Vectors: vs,
	})
	return body, len(vs), err
}

func buildDerive() ([]byte, int, error) {
	type deriveCase struct {
		name        string
		description string
		groupKeyLbl string
		nonceLbl    string
		epoch       uint64
	}
	cases := []deriveCase{
		{"epoch-1-text", "content key for the first entry at epoch 1", "group-key/epoch-1", "nonce/text", 1},
		{"epoch-1-same-key-other-nonce", "same group key and epoch, different nonce: an unrelated content key", "group-key/epoch-1", "nonce/image", 1},
		{"epoch-2-same-nonce", "same group key and nonce, different epoch: the info string separates them", "group-key/epoch-1", "nonce/text", 2},
		{"epoch-7-after-rekey", "content key under the group key installed by a rekey", "group-key/epoch-7", "nonce/file", 7},
		{"epoch-max", "epoch at the u64 ceiling, to pin the big-endian encoding", "group-key/epoch-1", "nonce/text", 18446744073709551615},
	}
	vs := make([]deriveVector, 0, len(cases))
	for _, c := range cases {
		groupKey := det(c.groupKeyLbl, GroupKeySize)
		nonce := det(c.nonceLbl, NonceSize)
		key, err := ContentKey(groupKey, nonce, c.epoch)
		if err != nil {
			return nil, 0, err
		}
		vs = append(vs, deriveVector{
			Name:        c.name,
			Description: c.description,
			GroupKey:    hexs(groupKey),
			Epoch:       c.epoch,
			EpochHex:    epochHex(c.epoch),
			Nonce:       hexs(nonce),
			HKDFSalt:    hexs(nonce),
			HKDFInfo:    hexs(EntryInfo(c.epoch)),
			ContentKey:  hexs(key),
		})
	}
	body, err := marshal(suite[deriveVector]{
		SchemaVersion: schemaVersion,
		Profile:       profile,
		Suite:         "derive",
		Spec:          "spec/crypto.md §5.1",
		Description:   "HKDF-SHA256(ikm=group_key, salt=nonce, info=\"tpp/v1/entry\"||u64be(epoch), L=32).",
		Vectors:       vs,
	})
	return body, len(vs), err
}

// frameCase is one plaintext frame, shared by the frame and entry suites.
type frameCase struct {
	name        string
	description string
	frame       Frame
	nonceLbl    string
	entryID     string
	epoch       uint64
	groupKeyLbl string
}

func frameCases() []frameCase {
	return []frameCase{
		{
			name:        "text-ascii",
			description: "the ordinary case: a short ASCII clipboard string",
			frame:       Frame{ContentType: "text/plain; charset=utf-8", CreatedAtUnix: 1757340000000, Body: []byte("asd123")},
			nonceLbl:    "nonce/text",
			entryID:     "01J9ZQK8N4X0000000000000AB",
			epoch:       1,
			groupKeyLbl: "group-key/epoch-1",
		},
		{
			name:        "text-unicode",
			description: "non-ASCII body and a multi-byte content type boundary",
			frame:       Frame{ContentType: "text/plain; charset=utf-8", CreatedAtUnix: 1757340061234, Body: []byte("héllo — 世界 🌍")},
			nonceLbl:    "nonce/unicode",
			entryID:     "01J9ZQK8N4X0000000000000CD",
			epoch:       1,
			groupKeyLbl: "group-key/epoch-1",
		},
		{
			name:        "image-binary",
			description: "binary body with no filename",
			frame:       Frame{ContentType: "image/png", CreatedAtUnix: 1757340122000, Body: det("body/image", 64)},
			nonceLbl:    "nonce/image",
			entryID:     "01J9ZQK8N4X0000000000000EF",
			epoch:       1,
			groupKeyLbl: "group-key/epoch-1",
		},
		{
			name:        "file-with-filename",
			description: "a file entry: the filename is inside the frame, so the server never sees it",
			frame:       Frame{ContentType: "application/pdf", Filename: "quarterly report.pdf", CreatedAtUnix: 1757340183000, Body: det("body/file", 128)},
			nonceLbl:    "nonce/file",
			entryID:     "01J9ZQK8N4X0000000000000GH",
			epoch:       7,
			groupKeyLbl: "group-key/epoch-7",
		},
		{
			name:        "empty-body",
			description: "zero-length body: legal, and the length prefixes must still be present",
			frame:       Frame{ContentType: "text/plain; charset=utf-8", CreatedAtUnix: 1757340244000, Body: []byte{}},
			nonceLbl:    "nonce/empty",
			entryID:     "01J9ZQK8N4X0000000000000JK",
			epoch:       1,
			groupKeyLbl: "group-key/epoch-1",
		},
	}
}

func fields(f Frame) frameFields {
	return frameFields{
		ContentType:     f.ContentType,
		Filename:        f.Filename,
		CreatedAtUnixMs: f.CreatedAtUnix,
		PadLen:          f.PadLen,
		Body:            hexs(f.Body),
	}
}

func buildFrame() ([]byte, int, error) {
	cases := frameCases()
	vs := make([]frameVector, 0, len(cases))
	for _, c := range cases {
		encoded, err := c.frame.Encode()
		if err != nil {
			return nil, 0, err
		}
		back, err := DecodeFrame(encoded)
		if err != nil {
			return nil, 0, fmt.Errorf("round-trip frame %s: %w", c.name, err)
		}
		if back.ContentType != c.frame.ContentType || back.Filename != c.frame.Filename ||
			back.CreatedAtUnix != c.frame.CreatedAtUnix || back.PadLen != c.frame.PadLen ||
			!bytes.Equal(back.Body, c.frame.Body) {
			return nil, 0, fmt.Errorf("round-trip frame %s: decoded frame differs", c.name)
		}
		vs = append(vs, frameVector{
			Name:        c.name,
			Description: c.description,
			Frame:       fields(c.frame),
			Encoded:     hexs(encoded),
		})
	}
	body, err := marshal(suite[frameVector]{
		SchemaVersion: schemaVersion,
		Profile:       profile,
		Suite:         "frame",
		Spec:          "spec/crypto.md §6",
		Description: "Plaintext frame serialization. The encoding is canonical: encoding the " +
			"fields must produce `encoded` byte for byte, and decoding `encoded` must " +
			"produce the fields.",
		Vectors: vs,
	})
	return body, len(vs), err
}

func buildEntry() ([]byte, int, error) {
	cases := frameCases()
	vs := make([]entryVector, 0, len(cases))
	for _, c := range cases {
		groupKey := det(c.groupKeyLbl, GroupKeySize)
		nonce := det(c.nonceLbl, NonceSize)
		encoded, err := c.frame.Encode()
		if err != nil {
			return nil, 0, err
		}
		contentKey, err := ContentKey(groupKey, nonce, c.epoch)
		if err != nil {
			return nil, 0, err
		}
		container, err := SealEntry(groupKey, nonce, c.epoch, c.entryID, encoded)
		if err != nil {
			return nil, 0, err
		}
		back, err := OpenEntry(groupKey, container, c.epoch, c.entryID)
		if err != nil {
			return nil, 0, fmt.Errorf("round-trip entry %s: %w", c.name, err)
		}
		if !bytes.Equal(back, encoded) {
			return nil, 0, fmt.Errorf("round-trip entry %s: frame differs", c.name)
		}
		vs = append(vs, entryVector{
			Name:        c.name,
			Description: c.description,
			GroupKey:    hexs(groupKey),
			Epoch:       c.epoch,
			EpochHex:    epochHex(c.epoch),
			EntryID:     c.entryID,
			Nonce:       hexs(nonce),
			ContentKey:  hexs(contentKey),
			Frame:       fields(c.frame),
			Encoded:     hexs(encoded),
			AAD:         hexs(EntryAAD(c.epoch, c.entryID)),
			Container:   hexs(container),
		})
	}
	body, err := marshal(suite[entryVector]{
		SchemaVersion: schemaVersion,
		Profile:       profile,
		Suite:         "entry",
		Spec:          "spec/crypto.md §5",
		Description: "End-to-end entry encryption. `container` is the byte string the server " +
			"stores and never understands.",
		Vectors: vs,
	})
	return body, len(vs), err
}
