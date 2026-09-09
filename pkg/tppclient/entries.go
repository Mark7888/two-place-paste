package tppclient

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/tppcrypto"
)

// MaxCiphertextBytes is the relay's per-entry cap (SPEC §4.3). It is measured
// on the ciphertext, so a plaintext close to it will be refused: the container
// adds 41 bytes plus the frame header.
const MaxCiphertextBytes = 10 << 20

// newEntryID returns the identifier this client binds into an entry's
// associated data and sends with it (/spec/crypto.md §5.3).
//
// The id must exist before the ciphertext does — the AAD binds it — so the
// client chooses it rather than the relay: 128 bits from the CSPRNG, in
// base64url, which is the alphabet the relay accepts because an id is also a
// key and a blob filename. At that width a collision is not a case worth
// retrying for; the relay refuses one rather than overwriting an entry, and
// the caller sees the refusal.
func newEntryID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("tppclient: generate an entry id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Item is one clipboard payload in plaintext. Nothing here reaches the relay
// in the clear: content type and filename live inside the encrypted frame
// precisely so the server never learns them (SPEC §2.3).
type Item struct {
	// ContentType is an IANA media type; an unknown one is
	// "application/octet-stream". It must not be empty.
	ContentType string

	// Filename is a bare filename for file entries, empty otherwise. A
	// consumer treats it as untrusted display text, never as a path.
	Filename string

	// Body is the payload.
	Body []byte

	// CreatedAt is the writing client's clock and is advisory. Expiry and
	// ordering come from the relay's own timestamps (SPEC §4.5, §6).
	CreatedAt time.Time

	// Meta is the relay's view of the entry this item came from. It is zero
	// for an item that has not been written yet.
	Meta EntryMeta
}

// EntryMeta is everything the relay knows about an entry. Note what is absent:
// content type, filename and plaintext size never leave the ciphertext.
type EntryMeta struct {
	ID        string
	Epoch     uint64
	Size      int64
	CreatedAt time.Time
	ExpiresAt time.Time

	// Inline is true when the relay delivered the ciphertext with the
	// metadata; false when it must be fetched separately. Callers of this
	// package do not need to care — GetLatest fetches either way.
	Inline bool
}

// History is a page of entry metadata, newest first (SPEC §6).
type History struct {
	Entries []EntryMeta

	// NextBefore pages backwards: pass it as HistoryQuery.Before. Zero when
	// the listing reached the end.
	NextBefore time.Time
}

// HistoryQuery bounds a history listing.
type HistoryQuery struct {
	// Limit is the page size. Zero means the relay's default.
	Limit uint32

	// Before returns only entries created strictly before this time. Zero
	// starts at the newest.
	Before time.Time
}

// PutEntry encrypts an item and stores it as the group's latest entry
// (SPEC §6). Last write to reach the relay wins.
func (c *Client) PutEntry(ctx context.Context, item Item) (EntryMeta, error) {
	state := c.State()
	if !state.InGroup() {
		return EntryMeta{}, ErrNoGroup
	}
	if item.ContentType == "" {
		return EntryMeta{}, fmt.Errorf("tppclient: an entry needs a content type")
	}
	createdAt := item.CreatedAt
	if createdAt.IsZero() {
		createdAt = c.now()
	}

	frame, err := tppcrypto.Frame{
		ContentType:     item.ContentType,
		Filename:        item.Filename,
		CreatedAtUnixMs: uint64(createdAt.UTC().UnixMilli()),
		Body:            item.Body,
	}.Encode()
	if err != nil {
		return EntryMeta{}, fmt.Errorf("tppclient: encode the plaintext frame: %w", err)
	}
	nonce, err := tppcrypto.GenerateNonce()
	if err != nil {
		return EntryMeta{}, fmt.Errorf("tppclient: generate the entry nonce: %w", err)
	}
	entryID, err := newEntryID()
	if err != nil {
		return EntryMeta{}, err
	}
	container, err := tppcrypto.SealEntry(state.GroupKey, nonce, state.Epoch, entryID, frame)
	if err != nil {
		return EntryMeta{}, fmt.Errorf("tppclient: seal the entry: %w", err)
	}
	if len(container) > MaxCiphertextBytes {
		// Refused here rather than by the relay: the cap is on the ciphertext,
		// and the caller deserves the answer before a 10 MB upload.
		return EntryMeta{}, fmt.Errorf("tppclient: ciphertext is %d bytes, the cap is %d: %w",
			len(container), MaxCiphertextBytes, ErrTooLarge)
	}

	var resp tppv1.EntryPutResponse
	err = c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST, &tppv1.EntryPutRequest{
		EntryId: entryID,
		Epoch:   state.Epoch,
		Size:    uint64(len(container)),
		Body:    &tppv1.EntryPutRequest_Ciphertext{Ciphertext: container},
	}, &resp)
	if err != nil {
		// ErrEpochConflict means a rekey landed between sealing and sending.
		// The caller re-encrypts under the new key once it arrives; this
		// package does not retry silently, because a clipboard write the user
		// did not ask for twice is not this library's call.
		return EntryMeta{}, err
	}
	meta := entryMeta(resp.GetMeta())
	if meta.ID != entryID {
		// The relay filed the entry under an id this client did not bind, so
		// nothing — this client included — can decrypt it. Reported rather
		// than ignored: the entry is on the relay and is unreadable.
		return EntryMeta{}, fmt.Errorf(
			"tppclient: the relay stored the entry under a different id than the one bound into its ciphertext")
	}
	return meta, nil
}

// GetLatest returns the group's most recent entry, decrypted (SPEC §6).
//
// Epoch handling follows /spec/crypto.md §7 exactly:
//
//   - same epoch: decrypt.
//   - older epoch: ErrStaleEntry. The entry belongs to a key generation this
//     client no longer holds and must not keep; it expires within 24 hours and
//     the caller shows nothing rather than an error.
//   - newer epoch: ErrEpochAhead. This client is behind a rekey; its wrapped
//     key arrives on the connection and the caller retries.
//
// An empty group is ErrNoEntry, which is what a freshly paired device sees.
func (c *Client) GetLatest(ctx context.Context) (Item, error) {
	if !c.InGroup() {
		return Item{}, ErrNoGroup
	}
	var resp tppv1.EntryLatestResponse
	if err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_ENTRY_LATEST_REQUEST, &tppv1.EntryLatestRequest{}, &resp); err != nil {
		return Item{}, err
	}
	if resp.GetMeta() == nil {
		return Item{}, ErrNoEntry
	}

	meta := entryMeta(resp.GetMeta())
	ciphertext := resp.GetCiphertext()
	if !meta.Inline {
		// A large entry lives in the blob backend and is pulled by id; whether
		// it was inline is the relay's problem, not the caller's.
		fetched, body, err := c.fetch(ctx, meta.ID)
		if err != nil {
			return Item{}, err
		}
		meta, ciphertext = fetched, body
	}
	return c.open(meta, ciphertext)
}

// GetHistory lists entry metadata, newest first (SPEC §6).
//
// History is only ever pulled. Nothing in this package fetches it on
// reconnect, and the relay never pushes it: a client that listed history
// automatically would be making the user's clipboard history travel without
// being asked.
func (c *Client) GetHistory(ctx context.Context, q HistoryQuery) (History, error) {
	if !c.InGroup() {
		return History{}, ErrNoGroup
	}
	req := &tppv1.EntryHistoryRequest{Limit: q.Limit}
	if !q.Before.IsZero() {
		req.BeforeUnixMs = q.Before.UTC().UnixMilli()
	}

	var resp tppv1.EntryHistoryResponse
	if err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_ENTRY_HISTORY_REQUEST, req, &resp); err != nil {
		return History{}, err
	}
	out := History{
		Entries:    make([]EntryMeta, 0, len(resp.GetEntries())),
		NextBefore: msToTime(resp.GetNextBeforeUnixMs()),
	}
	for _, m := range resp.GetEntries() {
		out.Entries = append(out.Entries, entryMeta(m))
	}
	return out, nil
}

// GetEntry fetches one entry by id and decrypts it — what a history browser
// does when the user taps an entry to copy it.
//
// The same epoch rules as GetLatest apply.
func (c *Client) GetEntry(ctx context.Context, entryID string) (Item, error) {
	if !c.InGroup() {
		return Item{}, ErrNoGroup
	}
	meta, ciphertext, err := c.fetch(ctx, entryID)
	if err != nil {
		return Item{}, err
	}
	return c.open(meta, ciphertext)
}

// fetch pulls one entry's ciphertext by id.
func (c *Client) fetch(ctx context.Context, entryID string) (EntryMeta, []byte, error) {
	var resp tppv1.EntryFetchResponse
	err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_ENTRY_FETCH_REQUEST,
		&tppv1.EntryFetchRequest{EntryId: entryID}, &resp)
	if err != nil {
		return EntryMeta{}, nil, err
	}
	return entryMeta(resp.GetMeta()), resp.GetCiphertext(), nil
}

// open applies the epoch rules and decrypts.
func (c *Client) open(meta EntryMeta, ciphertext []byte) (Item, error) {
	state := c.State()
	switch {
	case meta.Epoch < state.Epoch:
		return Item{}, fmt.Errorf("tppclient: entry %s is at epoch %d, this device is at %d: %w",
			meta.ID, meta.Epoch, state.Epoch, ErrStaleEntry)
	case meta.Epoch > state.Epoch:
		return Item{}, fmt.Errorf("tppclient: entry %s is at epoch %d, this device is at %d: %w",
			meta.ID, meta.Epoch, state.Epoch, ErrEpochAhead)
	}

	// The epoch and the entry id used here are the ones the relay reports. A
	// client must never try other epochs, or other ids, to make an entry
	// decrypt: at its own epoch a failure is corruption or tampering — an
	// entry the relay relabelled or replayed under another id included — and
	// it is reported (§5.3, §7).
	encoded, err := tppcrypto.OpenEntry(state.GroupKey, ciphertext, meta.Epoch, meta.ID)
	if err != nil {
		return Item{}, fmt.Errorf("tppclient: entry %s did not decrypt: %w", meta.ID, err)
	}
	frame, err := tppcrypto.DecodeFrame(encoded)
	if err != nil {
		return Item{}, fmt.Errorf("tppclient: entry %s carries a frame this client cannot read: %w", meta.ID, err)
	}
	return Item{
		ContentType: frame.ContentType,
		Filename:    frame.Filename,
		Body:        frame.Body,
		CreatedAt:   frame.CreatedAt(),
		Meta:        meta,
	}, nil
}

func entryMeta(m *tppv1.EntryMeta) EntryMeta {
	return EntryMeta{
		ID:        m.GetEntryId(),
		Epoch:     m.GetEpoch(),
		Size:      int64(m.GetSize()),
		CreatedAt: msToTime(m.GetCreatedAtUnixMs()),
		ExpiresAt: msToTime(m.GetExpiresAtUnixMs()),
		Inline:    m.GetInline(),
	}
}
