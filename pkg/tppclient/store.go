package tppclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"time"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/tppcrypto"
)

// The store (SPEC §6b, /spec/crypto.md §7a): items a group keeps on purpose,
// beside its 24-hour history, until a device deletes them. The relay holds
// ciphertext and sizes; name, description, tags, type, filename and hash are
// in a sealed metadata record only the group can open.
//
// An item outlives the epoch it was saved in. Its keys are wrapped under the
// store key of that epoch, and every rekey leaves a link from the new store
// key back to the old one, so this client walks the links from its current
// key to read an item of any earlier epoch.

// Store conditions a caller branches on.
var (
	// ErrStoreDisabled means the server admin has not turned the store on for
	// this group, or it is being emptied. A UI says so instead of showing the
	// Store tab's contents (SPEC §6b).
	ErrStoreDisabled error = codeError(tppv1.ErrorCode_ERROR_CODE_STORE_DISABLED)

	// ErrRevisionConflict means another device edited the item first. The
	// caller lists it again, shows the new version and lets the user redo the
	// edit; nothing was overwritten.
	ErrRevisionConflict error = codeError(tppv1.ErrorCode_ERROR_CODE_REVISION_CONFLICT)

	// ErrClientOutdated means the relay needs something this build does not
	// send. The user updates the app.
	ErrClientOutdated error = codeError(tppv1.ErrorCode_ERROR_CODE_CLIENT_OUTDATED)

	// ErrStoreUnreadable means an item's keys, metadata or blob did not open:
	// a missing link, tampering, or corruption. The item is reported, never
	// silently dropped, and the user can delete it.
	ErrStoreUnreadable = errors.New("tppclient: the store item cannot be read")
)

// Store limits on what a user types, in bytes of UTF-8 (/spec/crypto.md
// §7a.5). A UI shows them as counters: a record over any of them is refused,
// never cut.
const (
	StoreMaxName        = tppcrypto.StoreMaxName
	StoreMaxDescription = tppcrypto.StoreMaxDescription
	StoreMaxTags        = tppcrypto.StoreMaxTags
	StoreMaxTag         = tppcrypto.StoreMaxTag
)

// StoreLimits is the group's store as the relay reports it, in bytes the relay
// counts: ciphertext plus each item's fixed-size records.
type StoreLimits struct {
	// Enabled is false while the admin has the store off or it is being
	// emptied; every other field is then zero.
	Enabled bool

	// MaxBytes is the cap the group runs into; zero means none.
	MaxBytes  int64
	UsedBytes int64

	// AvailableBytes is the largest blob a save can still have. A UI shows it
	// as the space left and refuses a larger save before encrypting.
	AvailableBytes int64

	MaxItems  int64
	UsedItems int64

	// MaxItemBytes is the largest blob the relay takes at all.
	MaxItemBytes int64
}

// StoreDetails is what a user types about an item.
type StoreDetails struct {
	Name        string
	Description string
	// Tags is a set: no tag is empty, and none appears twice.
	Tags []string
}

// StoreItem is one item of the group's store, with its metadata opened.
type StoreItem struct {
	ID      string
	SavedAt time.Time

	StoreDetails

	ContentType string
	Filename    string

	// BodySize is the plaintext length; SHA256 is the plaintext's hash, for
	// spotting duplicates without fetching anything.
	BodySize int64
	SHA256   [sha256.Size]byte

	// BlobSize is the ciphertext length, what the item costs on the relay.
	BlobSize int64

	// MetaRev is the revision an edit starts from (/spec/crypto.md §7a.5).
	MetaRev uint64

	// WrapEpoch is the epoch the item's keys are wrapped at.
	WrapEpoch uint64

	contentID    string
	contentEpoch uint64
	contentKey   []byte
	metaKey      []byte
}

// Unreadable is an item this client listed but could not open, and why. A
// UI shows it as such, with a Delete action.
type Unreadable struct {
	ID      string
	SavedAt time.Time
	Err     error
}

// StoreListing is the whole store, as far as this client can read it.
type StoreListing struct {
	Items      []StoreItem
	Unreadable []Unreadable
}

// storeKeys caches the store keys this client has derived or opened, for one
// group key. A new group key — a rekey, or another group — starts it over.
// It is never persisted (/spec/crypto.md §7a.2).
type storeKeys struct {
	groupKey []byte
	keys     map[uint64][]byte
	links    map[uint64][]byte
	fetched  bool
}

// StoreLimits asks the relay for the group's store limits and usage.
func (c *Client) StoreLimits(ctx context.Context) (StoreLimits, error) {
	if !c.InGroup() {
		return StoreLimits{}, ErrNoGroup
	}
	var resp tppv1.StoreLimitsResponse
	if err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_STORE_LIMITS_REQUEST, &tppv1.StoreLimitsRequest{}, &resp); err != nil {
		if errors.Is(err, errUnsupported) {
			// A relay that predates the store has none.
			return StoreLimits{}, nil
		}
		return StoreLimits{}, err
	}
	return StoreLimits{
		Enabled:        resp.GetEnabled(),
		MaxBytes:       clampInt64(resp.GetMaxBytes()),
		UsedBytes:      clampInt64(resp.GetUsedBytes()),
		AvailableBytes: clampInt64(resp.GetAvailableBytes()),
		MaxItems:       clampInt64(resp.GetMaxItems()),
		UsedItems:      clampInt64(resp.GetUsedItems()),
		MaxItemBytes:   clampInt64(resp.GetMaxItemBytes()),
	}, nil
}

func clampInt64(v uint64) int64 {
	return int64(min(v, math.MaxInt64)) //nolint:gosec // clamped
}

// SaveToStore encrypts an item and saves it in the group's store with the
// details the user typed (/spec/crypto.md §7a.3-§7a.5). Saving from history
// is GetEntry followed by this: the content is copied, and the entry keeps its
// own lifetime.
//
// It does not look for duplicates: a UI checks the item's hash against its
// StoreIndex first, and asks.
func (c *Client) SaveToStore(ctx context.Context, item Item, details StoreDetails) (StoreItem, error) {
	state := c.State()
	if !state.InGroup() {
		return StoreItem{}, ErrNoGroup
	}
	if item.ContentType == "" {
		return StoreItem{}, fmt.Errorf("tppclient: a store item needs a content type")
	}
	sum := sha256.Sum256(item.Body)
	meta, err := tppcrypto.EncodeStoreMeta(tppcrypto.StoreMeta{
		Name:        details.Name,
		Description: details.Description,
		Tags:        details.Tags,
		ContentType: item.ContentType,
		Filename:    item.Filename,
		BodyLen:     uint64(len(item.Body)),
		SHA256:      sum,
	})
	if err != nil {
		return StoreItem{}, fmt.Errorf("tppclient: the item's details: %w", err)
	}

	createdAt := item.CreatedAt
	if createdAt.IsZero() {
		createdAt = c.now()
	}
	frame, err := tppcrypto.Frame{
		ContentType:     item.ContentType,
		Filename:        item.Filename,
		CreatedAtUnixMs: uint64(createdAt.UTC().UnixMilli()), //nolint:gosec // a timestamp
		Body:            item.Body,
	}.Encode()
	if err != nil {
		return StoreItem{}, fmt.Errorf("tppclient: encode the plaintext frame: %w", err)
	}
	itemID, err := newEntryID()
	if err != nil {
		return StoreItem{}, err
	}
	epoch := state.Epoch

	// The blob is an entry container at this epoch, bound to the item's id;
	// its content key is the one wrapped below (§7a.4).
	nonce, err := tppcrypto.GenerateNonce()
	if err != nil {
		return StoreItem{}, fmt.Errorf("tppclient: generate the item nonce: %w", err)
	}
	contentKey, err := tppcrypto.ContentKey(state.GroupKey, nonce, epoch)
	if err != nil {
		return StoreItem{}, fmt.Errorf("tppclient: derive the item's content key: %w", err)
	}
	container, err := tppcrypto.SealEntry(state.GroupKey, nonce, epoch, itemID, frame)
	if err != nil {
		return StoreItem{}, fmt.Errorf("tppclient: seal the item: %w", err)
	}

	metaKey := make([]byte, tppcrypto.MetaKeySize)
	if _, err := rand.Read(metaKey); err != nil {
		return StoreItem{}, fmt.Errorf("tppclient: generate the item's metadata key: %w", err)
	}
	storeKey, err := tppcrypto.StoreKey(state.GroupKey, epoch)
	if err != nil {
		return StoreItem{}, fmt.Errorf("tppclient: derive the store key: %w", err)
	}
	wrapNonce, err := tppcrypto.GenerateNonce()
	if err != nil {
		return StoreItem{}, fmt.Errorf("tppclient: generate the wrap nonce: %w", err)
	}
	wrapped, err := tppcrypto.WrapItemKeys(storeKey, wrapNonce, epoch, itemID, contentKey, metaKey)
	if err != nil {
		return StoreItem{}, fmt.Errorf("tppclient: wrap the item's keys: %w", err)
	}
	metaNonce, err := tppcrypto.GenerateNonce()
	if err != nil {
		return StoreItem{}, fmt.Errorf("tppclient: generate the metadata nonce: %w", err)
	}
	sealedMeta, err := tppcrypto.SealStoreMeta(metaKey, metaNonce, itemID, 0, meta)
	if err != nil {
		return StoreItem{}, fmt.Errorf("tppclient: seal the item's metadata: %w", err)
	}

	// The limits are asked first so a store that is off, full or too small
	// refuses before anything is uploaded.
	limits, err := c.StoreLimits(ctx)
	if err != nil {
		return StoreItem{}, err
	}
	size := int64(len(container))
	switch {
	case !limits.Enabled:
		return StoreItem{}, fmt.Errorf("tppclient: save to the store: %w", ErrStoreDisabled)
	case limits.MaxItemBytes > 0 && size > limits.MaxItemBytes:
		return StoreItem{}, fmt.Errorf("tppclient: the item is %d bytes sealed, the store takes %d: %w",
			size, limits.MaxItemBytes, ErrTooLarge)
	case size > limits.AvailableBytes:
		return StoreItem{}, fmt.Errorf("tppclient: the item is %d bytes sealed, the store has room for %d: %w",
			size, limits.AvailableBytes, ErrQuotaExceeded)
	}

	req := &tppv1.StorePutRequest{
		ItemId:          itemID,
		Epoch:           epoch,
		Size:            uint64(size), //nolint:gosec // a length
		WrappedItemKeys: wrapped,
		SealedMeta:      sealedMeta,
	}
	var resp tppv1.StorePutResponse
	if size > SingleFrameMaxBytes {
		err = c.storeUploadChunked(ctx, req, container, &resp)
	} else {
		req.Body = &tppv1.StorePutRequest_Ciphertext{Ciphertext: container}
		err = c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_STORE_PUT_REQUEST, req, &resp)
	}
	if err != nil {
		// ErrEpochConflict means a rekey landed meanwhile: the caller saves
		// again once the new key arrives, as for an entry.
		return StoreItem{}, err
	}
	m := resp.GetItem()
	if m.GetItemId() != itemID || m.GetContentId() != itemID || m.GetWrapEpoch() != epoch || m.GetContentEpoch() != epoch {
		return StoreItem{}, fmt.Errorf("tppclient: the relay stored the item under other ids or epochs than the ones bound into it")
	}
	return c.openStoreItem(m, storeKey)
}

// storeUploadChunked uploads a blob larger than one frame through the
// entries' chunk messages and commits it. Any failure aborts the upload so
// the relay gives its room back at once.
func (c *Client) storeUploadChunked(ctx context.Context, req *tppv1.StorePutRequest, container []byte, out *tppv1.StorePutResponse) error {
	size := int64(len(container))
	var begin tppv1.StoreUploadBeginResponse
	err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_STORE_UPLOAD_BEGIN_REQUEST, &tppv1.StoreUploadBeginRequest{
		ItemId: req.GetItemId(),
		Epoch:  req.GetEpoch(),
		Size:   req.GetSize(),
	}, &begin)
	if err != nil {
		return err
	}
	handle := begin.GetUploadHandle()
	chunk := int64(begin.GetChunkBytes()) //nolint:gosec // the relay's own chunk size
	if chunk <= 0 || chunk > SingleFrameMaxBytes {
		chunk = SingleFrameMaxBytes
	}
	abort := func(cause error) error {
		_ = c.call(context.WithoutCancel(ctx), tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_ABORT_REQUEST,
			&tppv1.EntryUploadAbortRequest{UploadHandle: handle}, &tppv1.EntryUploadAbortResponse{})
		return cause
	}

	id := req.GetItemId()
	c.progress(Progress{EntryID: id, Direction: Upload, Total: size})
	for i, off := uint32(0), int64(0); off < size; i, off = i+1, off+chunk {
		end := min(off+chunk, size)
		var ack tppv1.EntryUploadChunkResponse
		err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_CHUNK_REQUEST, &tppv1.EntryUploadChunkRequest{
			UploadHandle: handle,
			Index:        i,
			Data:         container[off:end],
		}, &ack)
		if err != nil {
			return abort(err)
		}
		c.progress(Progress{EntryID: id, Direction: Upload, Done: end, Total: size})
	}
	req.Body = &tppv1.StorePutRequest_UploadHandle{UploadHandle: handle}
	if err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_STORE_PUT_REQUEST, req, out); err != nil {
		return abort(err)
	}
	return nil
}

// ListStore lists and opens every item of the group's store, newest first.
// An item that does not open is reported in Unreadable rather than dropped.
// A UI loads it into a StoreIndex for search, tag filters and sorting, and
// keeps it current from the store handlers.
func (c *Client) ListStore(ctx context.Context) (StoreListing, error) {
	if !c.InGroup() {
		return StoreListing{}, ErrNoGroup
	}
	var (
		out    StoreListing
		cursor string
	)
	for {
		var resp tppv1.StoreListResponse
		err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_STORE_LIST_REQUEST,
			&tppv1.StoreListRequest{Cursor: cursor}, &resp)
		if err != nil {
			return StoreListing{}, err
		}
		for _, m := range resp.GetItems() {
			it, err := c.readStoreItem(ctx, m)
			if err != nil {
				if !errors.Is(err, ErrStoreUnreadable) && !errors.Is(err, ErrEpochAhead) {
					return StoreListing{}, err
				}
				out.Unreadable = append(out.Unreadable, Unreadable{
					ID: m.GetItemId(), SavedAt: msToTime(m.GetSavedAtUnixMs()), Err: err,
				})
				continue
			}
			out.Items = append(out.Items, it)
		}
		cursor = resp.GetNextCursor()
		if cursor == "" {
			return out, nil
		}
	}
}

// readStoreItem opens one listed item, fetching the link chain when its keys
// are wrapped at an earlier epoch (/spec/crypto.md §7a.6).
func (c *Client) readStoreItem(ctx context.Context, m *tppv1.StoreItemMeta) (StoreItem, error) {
	key, err := c.storeKeyAt(ctx, m.GetWrapEpoch())
	if err != nil {
		return StoreItem{}, err
	}
	return c.openStoreItem(m, key)
}

// openStoreItem unwraps an item's keys with the store key of its wrap epoch
// and opens its metadata, at the revision the relay reports.
func (c *Client) openStoreItem(m *tppv1.StoreItemMeta, storeKey []byte) (StoreItem, error) {
	id := m.GetItemId()
	unreadable := func(what string, err error) (StoreItem, error) {
		return StoreItem{}, fmt.Errorf("tppclient: store item %s: %s: %w: %w", id, what, ErrStoreUnreadable, err)
	}
	contentKey, metaKey, err := tppcrypto.UnwrapItemKeys(storeKey, m.GetWrappedItemKeys(), m.GetWrapEpoch(), id)
	if err != nil {
		return unreadable("its keys", err)
	}
	encoded, err := tppcrypto.OpenStoreMeta(metaKey, m.GetSealedMeta(), id, m.GetMetaRev())
	if err != nil {
		return unreadable("its metadata", err)
	}
	meta, err := tppcrypto.DecodeStoreMeta(encoded)
	if err != nil {
		return unreadable("its metadata", err)
	}
	if meta.BodyLen > math.MaxInt64 {
		return unreadable("its metadata", fmt.Errorf("size out of range"))
	}
	return StoreItem{
		ID:      id,
		SavedAt: msToTime(m.GetSavedAtUnixMs()),
		StoreDetails: StoreDetails{
			Name:        meta.Name,
			Description: meta.Description,
			Tags:        meta.Tags,
		},
		ContentType:  meta.ContentType,
		Filename:     meta.Filename,
		BodySize:     int64(meta.BodyLen),
		SHA256:       meta.SHA256,
		BlobSize:     clampInt64(m.GetSize()),
		MetaRev:      m.GetMetaRev(),
		WrapEpoch:    m.GetWrapEpoch(),
		contentID:    m.GetContentId(),
		contentEpoch: m.GetContentEpoch(),
		contentKey:   contentKey,
		metaKey:      metaKey,
	}, nil
}

// FetchStoreItem downloads an item's blob and decrypts it: what Copy and
// Save as file do. The body is checked against the hash in the item's
// metadata.
func (c *Client) FetchStoreItem(ctx context.Context, it StoreItem) (Item, error) {
	if !c.InGroup() {
		return Item{}, ErrNoGroup
	}
	if it.contentKey == nil {
		return Item{}, fmt.Errorf("tppclient: fetch a store item: it was not listed by this client")
	}
	var (
		buf  []byte
		size int64
	)
	for {
		var resp tppv1.StoreFetchChunkResponse
		err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_STORE_FETCH_CHUNK_REQUEST, &tppv1.StoreFetchChunkRequest{
			ItemId: it.ID,
			Offset: uint64(len(buf)),
			Length: SingleFrameMaxBytes,
		}, &resp)
		if err != nil {
			return Item{}, err
		}
		m := resp.GetItem()
		if buf == nil {
			size = clampInt64(m.GetSize())
			if size <= 0 || size != it.BlobSize {
				return Item{}, fmt.Errorf("tppclient: store item %s reports %d bytes, listed as %d", it.ID, size, it.BlobSize)
			}
			buf = make([]byte, 0, size)
		}
		data := resp.GetData()
		if m.GetItemId() != it.ID || resp.GetOffset() != uint64(len(buf)) || len(data) == 0 || int64(len(buf)+len(data)) > size {
			return Item{}, fmt.Errorf("tppclient: store item %s: the relay returned a range that does not continue the item", it.ID)
		}
		buf = append(buf, data...)
		if size > SingleFrameMaxBytes {
			c.progress(Progress{EntryID: it.ID, Direction: Download, Done: int64(len(buf)), Total: size})
		}
		if int64(len(buf)) == size {
			break
		}
	}

	encoded, err := tppcrypto.OpenEntryWithKey(it.contentKey, buf, it.contentEpoch, it.contentID)
	if err != nil {
		return Item{}, fmt.Errorf("tppclient: store item %s did not decrypt: %w: %w", it.ID, ErrStoreUnreadable, err)
	}
	frame, err := tppcrypto.DecodeFrame(encoded)
	if err != nil {
		return Item{}, fmt.Errorf("tppclient: store item %s carries a frame this client cannot read: %w", it.ID, err)
	}
	if sum := sha256.Sum256(frame.Body); int64(len(frame.Body)) != it.BodySize || !bytes.Equal(sum[:], it.SHA256[:]) {
		return Item{}, fmt.Errorf("tppclient: store item %s does not match its metadata: %w", it.ID, ErrStoreUnreadable)
	}
	return Item{
		ContentType: frame.ContentType,
		Filename:    frame.Filename,
		Body:        frame.Body,
		CreatedAt:   frame.CreatedAt(),
	}, nil
}

// EditStoreItem replaces an item's name, description and tags. It starts from
// the revision the item was listed at: when another device edited it first
// the relay refuses with ErrRevisionConflict and nothing changes.
func (c *Client) EditStoreItem(ctx context.Context, it StoreItem, details StoreDetails) (StoreItem, error) {
	if !c.InGroup() {
		return StoreItem{}, ErrNoGroup
	}
	if it.metaKey == nil {
		return StoreItem{}, fmt.Errorf("tppclient: edit a store item: it was not listed by this client")
	}
	encoded, err := tppcrypto.EncodeStoreMeta(tppcrypto.StoreMeta{
		Name:        details.Name,
		Description: details.Description,
		Tags:        details.Tags,
		ContentType: it.ContentType,
		Filename:    it.Filename,
		BodyLen:     uint64(it.BodySize), //nolint:gosec // a length
		SHA256:      it.SHA256,
	})
	if err != nil {
		return StoreItem{}, fmt.Errorf("tppclient: the item's details: %w", err)
	}
	nonce, err := tppcrypto.GenerateNonce()
	if err != nil {
		return StoreItem{}, fmt.Errorf("tppclient: generate the metadata nonce: %w", err)
	}
	sealed, err := tppcrypto.SealStoreMeta(it.metaKey, nonce, it.ID, it.MetaRev+1, encoded)
	if err != nil {
		return StoreItem{}, fmt.Errorf("tppclient: seal the item's metadata: %w", err)
	}
	var resp tppv1.StoreUpdateMetaResponse
	err = c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_STORE_UPDATE_META_REQUEST, &tppv1.StoreUpdateMetaRequest{
		ItemId:          it.ID,
		ExpectedMetaRev: it.MetaRev,
		SealedMeta:      sealed,
	}, &resp)
	if err != nil {
		return StoreItem{}, err
	}
	m := resp.GetItem()
	if m.GetItemId() != it.ID || m.GetMetaRev() != it.MetaRev+1 {
		return StoreItem{}, fmt.Errorf("tppclient: the relay answered the edit of %s with another item or revision", it.ID)
	}
	edited := it
	edited.StoreDetails = StoreDetails{Name: details.Name, Description: details.Description, Tags: details.Tags}
	edited.MetaRev = m.GetMetaRev()
	return edited, nil
}

// DeleteStoreItem deletes an item for every device of the group. A UI asks
// first: it cannot be undone.
func (c *Client) DeleteStoreItem(ctx context.Context, itemID string) error {
	if !c.InGroup() {
		return ErrNoGroup
	}
	return c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_STORE_DELETE_REQUEST,
		&tppv1.StoreDeleteRequest{ItemId: itemID}, &tppv1.StoreDeleteResponse{})
}

// storeKeyAt returns the store key of epoch w: derived from the group key at
// the current epoch, and opened link by link below it. The links are fetched
// once per group key.
func (c *Client) storeKeyAt(ctx context.Context, w uint64) ([]byte, error) {
	state := c.State()
	if !state.InGroup() {
		return nil, ErrNoGroup
	}
	if w > state.Epoch {
		return nil, fmt.Errorf("tppclient: a store item is wrapped at epoch %d, this device is at %d: %w",
			w, state.Epoch, ErrEpochAhead)
	}
	if w == 0 {
		return nil, fmt.Errorf("tppclient: a store item is wrapped at epoch 0: %w", ErrStoreUnreadable)
	}
	if key, ok := c.cachedStoreKey(state.GroupKey, w); ok {
		return key, nil
	}
	current, err := tppcrypto.StoreKey(state.GroupKey, state.Epoch)
	if err != nil {
		return nil, fmt.Errorf("tppclient: derive the store key: %w", err)
	}
	c.cacheStoreKey(state.GroupKey, state.Epoch, current)
	if w == state.Epoch {
		return current, nil
	}

	links, err := c.storeLinks(ctx, state.GroupKey)
	if err != nil {
		return nil, err
	}
	key := current
	for e := state.Epoch; e > w; e-- {
		if k, ok := c.cachedStoreKey(state.GroupKey, e-1); ok {
			key = k
			continue
		}
		link, ok := links[e]
		if !ok {
			return nil, fmt.Errorf("tppclient: the relay has no store link into epoch %d: %w", e, ErrStoreUnreadable)
		}
		key, err = tppcrypto.OpenStoreLink(key, link, e)
		if err != nil {
			return nil, fmt.Errorf("tppclient: the store link into epoch %d: %w: %w", e, ErrStoreUnreadable, err)
		}
		c.cacheStoreKey(state.GroupKey, e-1, key)
	}
	return key, nil
}

// storeLinks fetches the group's links once per group key.
func (c *Client) storeLinks(ctx context.Context, groupKey []byte) (map[uint64][]byte, error) {
	c.mu.Lock()
	if sk := c.storeKeys; sk != nil && sk.fetched && bytes.Equal(sk.groupKey, groupKey) {
		links := sk.links
		c.mu.Unlock()
		return links, nil
	}
	c.mu.Unlock()

	var resp tppv1.StoreLinksResponse
	if err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_STORE_LINKS_REQUEST, &tppv1.StoreLinksRequest{}, &resp); err != nil {
		return nil, err
	}
	links := make(map[uint64][]byte, len(resp.GetLinks()))
	for _, l := range resp.GetLinks() {
		links[l.GetEpoch()] = l.GetLink()
	}
	c.mu.Lock()
	sk := c.storeKeysForLocked(groupKey)
	sk.links, sk.fetched = links, true
	c.mu.Unlock()
	return links, nil
}

func (c *Client) cachedStoreKey(groupKey []byte, epoch uint64) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sk := c.storeKeys
	if sk == nil || !bytes.Equal(sk.groupKey, groupKey) {
		return nil, false
	}
	k, ok := sk.keys[epoch]
	return k, ok
}

func (c *Client) cacheStoreKey(groupKey []byte, epoch uint64, key []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.storeKeysForLocked(groupKey).keys[epoch] = key
}

// storeKeysForLocked returns the cache for groupKey, starting it over when it
// was for another one. The caller holds c.mu.
func (c *Client) storeKeysForLocked(groupKey []byte) *storeKeys {
	if c.storeKeys == nil || !bytes.Equal(c.storeKeys.groupKey, groupKey) {
		c.storeKeys = &storeKeys{
			groupKey: bytes.Clone(groupKey),
			keys:     make(map[uint64][]byte),
		}
	}
	return c.storeKeys
}

// onStoreItem delivers a pushed item. Its keys are usually wrapped at the
// current epoch, which opens here; an item of an earlier epoch needs the link
// chain, which is a request, so it is opened on a goroutine of its own rather
// than on the read loop that would answer it.
func (c *Client) onStoreItem(m *tppv1.StoreItemMeta, handler func(StoreItem)) {
	if handler == nil || m == nil {
		return
	}
	state := c.State()
	if !state.InGroup() {
		return
	}
	if key, ok := c.cachedStoreKey(state.GroupKey, m.GetWrapEpoch()); ok || m.GetWrapEpoch() == state.Epoch {
		if !ok {
			var err error
			if key, err = tppcrypto.StoreKey(state.GroupKey, state.Epoch); err != nil {
				return
			}
		}
		if it, err := c.openStoreItem(m, key); err == nil {
			handler(it)
		} else {
			c.logger.Warn("a pushed store item did not open", "item_id", m.GetItemId())
		}
		return
	}
	ctx := c.runContext()
	go func() {
		ctx, cancel := context.WithTimeout(ctx, CallTimeout)
		defer cancel()
		it, err := c.readStoreItem(ctx, m)
		if err != nil {
			c.logger.Warn("a pushed store item did not open", "item_id", m.GetItemId())
			return
		}
		handler(it)
	}()
}
