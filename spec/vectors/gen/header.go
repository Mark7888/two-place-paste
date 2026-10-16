package main

import (
	"bytes"
	"fmt"
	"strings"
)

// headerFields is a header as plain JSON.
type headerFields struct {
	ContentType string `json:"content_type"`
	Filename    string `json:"filename"`
	BodyLen     uint64 `json:"body_len"`
}

type headerVector struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	GroupKey    string       `json:"group_key"`
	Epoch       uint64       `json:"epoch"`
	EpochHex    string       `json:"epoch_hex"`
	EntryID     string       `json:"entry_id"`
	Nonce       string       `json:"header_nonce"`
	HeaderKey   string       `json:"header_key"`
	Input       headerFields `json:"input"`
	Encoded     string       `json:"encoded_header"`
	Decoded     headerFields `json:"decoded"`
	AAD         string       `json:"aad"`
	Container   string       `json:"container"`
}

type headerCase struct {
	name, description string
	groupKeyLbl       string
	nonceLbl          string
	epoch             uint64
	entryID           string
	in                Header
}

func headerCases() []headerCase {
	return []headerCase{
		{
			name:        "header-text",
			description: "a text entry: no filename",
			groupKeyLbl: "group-key/epoch-1",
			nonceLbl:    "header-nonce/text",
			epoch:       1,
			entryID:     "8c1f0e2a-5d4b-4e7a-9f3c-2b6d1a0e9c47",
			in:          Header{ContentType: "text/plain; charset=utf-8", BodyLen: 11},
		},
		{
			name:        "header-file",
			description: "a file entry with a non-ASCII filename, at a later epoch",
			groupKeyLbl: "group-key/epoch-7",
			nonceLbl:    "header-nonce/file",
			epoch:       7,
			entryID:     "Qm3vT_x9Lr2-KpZ8aW4yNg",
			in:          Header{ContentType: "application/pdf", Filename: "Quarterly report – Q3 ✓.pdf", BodyLen: 1 << 20},
		},
		{
			name:        "header-long-content-type",
			description: "a content type over 128 bytes is written as application/octet-stream",
			groupKeyLbl: "group-key/epoch-1",
			nonceLbl:    "header-nonce/long-type",
			epoch:       1,
			entryID:     "long-content-type",
			in:          Header{ContentType: "application/vnd." + strings.Repeat("x", 130), Filename: "data.bin", BodyLen: 42},
		},
		{
			name:        "header-filename-cut",
			description: "a filename that does not fit is cut to what does, never inside a UTF-8 sequence",
			groupKeyLbl: "group-key/epoch-1",
			nonceLbl:    "header-nonce/filename-cut",
			epoch:       1,
			entryID:     "filename-cut",
			in:          Header{ContentType: "image/png", Filename: strings.Repeat("é", 200) + ".png", BodyLen: 123456789},
		},
	}
}

func buildHeader() ([]byte, int, error) {
	cases := headerCases()
	vs := make([]headerVector, 0, len(cases))
	for _, c := range cases {
		groupKey := det(c.groupKeyLbl, GroupKeySize)
		nonce := det(c.nonceLbl, NonceSize)
		encoded, err := EncodeHeader(c.in)
		if err != nil {
			return nil, 0, err
		}
		decoded, err := DecodeHeader(encoded)
		if err != nil {
			return nil, 0, fmt.Errorf("round-trip header %s: %w", c.name, err)
		}
		key, err := HeaderKey(groupKey, nonce, c.epoch)
		if err != nil {
			return nil, 0, err
		}
		container, err := SealHeader(groupKey, nonce, c.epoch, c.entryID, encoded)
		if err != nil {
			return nil, 0, err
		}
		if len(container) != HeaderContainerSize {
			return nil, 0, fmt.Errorf("header %s: container is %d bytes", c.name, len(container))
		}
		back, err := OpenHeader(groupKey, container, c.epoch, c.entryID)
		if err != nil || !bytes.Equal(back, encoded) {
			return nil, 0, fmt.Errorf("round-trip header %s: %v", c.name, err)
		}
		vs = append(vs, headerVector{
			Name:        c.name,
			Description: c.description,
			GroupKey:    hexs(groupKey),
			Epoch:       c.epoch,
			EpochHex:    epochHex(c.epoch),
			EntryID:     c.entryID,
			Nonce:       hexs(nonce),
			HeaderKey:   hexs(key),
			Input:       headerFields{c.in.ContentType, c.in.Filename, c.in.BodyLen},
			Encoded:     hexs(encoded),
			Decoded:     headerFields{decoded.ContentType, decoded.Filename, decoded.BodyLen},
			AAD:         hexs(HeaderAAD(c.epoch, c.entryID)),
			Container:   hexs(container),
		})
	}
	body, err := marshal(suite[headerVector]{
		SchemaVersion: schemaVersion,
		Profile:       profile,
		Suite:         "header",
		Spec:          "spec/crypto.md §5.5",
		Description: "Entry headers: the content type, filename and size a client may read " +
			"before fetching an entry. `input` is what the writer had; `decoded` is what a " +
			"reader gets back, after the content-type fallback and the filename cut. Every " +
			"container is exactly 297 bytes.",
		Vectors: vs,
	})
	return body, len(vs), err
}

// headerFailures are the header cases of failure.json.
func headerFailures() ([]failureVector, error) {
	c := headerCases()[0]
	groupKey := det(c.groupKeyLbl, GroupKeySize)
	nonce := det(c.nonceLbl, NonceSize)
	encoded, err := EncodeHeader(c.in)
	if err != nil {
		return nil, err
	}
	container, err := SealHeader(groupKey, nonce, c.epoch, c.entryID, encoded)
	if err != nil {
		return nil, err
	}
	open := func(name, desc, reason string, container []byte, epoch uint64, entryID string) failureVector {
		return failureVector{
			Name: name, Description: desc, Operation: "open_header", Reason: reason,
			GroupKey: hexs(groupKey), Epoch: epoch, EpochHex: epochHex(epoch), EntryID: entryID,
			Container: hexs(container),
		}
	}
	decode := func(name, desc, reason string, b []byte) failureVector {
		return failureVector{Name: name, Description: desc, Operation: "decode_header", Reason: reason, EncodedHeader: hexs(b)}
	}
	badVersion := append([]byte(nil), encoded...)
	badVersion[0] = 0x02
	padded := append([]byte(nil), encoded...)
	padded[HeaderSize-1] = 0x01
	emptyType := append([]byte(nil), encoded...)
	emptyType[1], emptyType[2] = 0, 0
	overflow := append([]byte(nil), encoded...)
	overflow[1], overflow[2] = 0x01, 0x00 // a 256-byte content type cannot fit

	vs := []failureVector{
		open("header-wrong-entry-id", "a header moved onto another entry must not open",
			"the entry id is bound into the AAD", container, c.epoch, c.entryID+"x"),
		open("header-wrong-epoch", "a header presented at another epoch must not open",
			"the epoch is in the key derivation and the AAD", container, c.epoch+1, c.entryID),
		open("header-tampered-ciphertext", "one flipped bit in the header ciphertext",
			"Poly1305 authentication fails", flip(container, 1+NonceSize+3), c.epoch, c.entryID),
		open("header-unknown-version", "a container version this profile does not implement",
			"rejected before any key is derived", flip(container, 0), c.epoch, c.entryID),
		open("header-wrong-length", "a container that is not exactly 297 bytes",
			"every header is the same size", container[:len(container)-1], c.epoch, c.entryID),
		decode("header-decode-unknown-version", "an encoded header of an unknown version", "the version byte is checked first", badVersion),
		decode("header-decode-pad-nonzero", "a non-zero byte in the padding", "the encoding is canonical", padded),
		decode("header-decode-empty-content-type", "an empty content type", "a header always names a type", emptyType),
		decode("header-decode-length-overflow", "a length prefix past the end of the header", "lengths must fit", overflow),
		decode("header-decode-wrong-size", "an encoded header that is not 256 bytes", "every header is the same size", encoded[:HeaderSize-1]),
	}
	return vs, nil
}
