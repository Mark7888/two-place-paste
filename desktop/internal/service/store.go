package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/desktop/internal/preview"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/tppcrypto"
)

// The store (SPEC §6b) on the desktop: the Store tab, Save to store from the
// clipboard, a history entry or a file, and the "saved" mark in the history.
//
// The service keeps the group's store opened in memory, in a
// tppclient.StoreIndex, and the tab searches, filters and sorts it here. It is
// listed the first time it is needed and listed again after anything that may
// have made it stale: a reconnect, which may have missed events, or the admin
// turning the store on. Between those, the client's store events keep it
// current. It is never written to disk.

// storeTransferTimeout bounds a save or a fetch: an item can be as large as
// an entry, and gets the same per-request deadlines inside the client.
const storeTransferTimeout = 30 * time.Minute

// storeState is the opened store.
type storeState struct {
	// loadMu serialises listings, so two tabs opening at once list once.
	loadMu sync.Mutex

	mu         sync.Mutex
	index      *tppclient.StoreIndex
	loaded     bool
	unreadable []tppclient.Unreadable

	// off remembers that the relay said the store is off, so a history page
	// does not ask again each time; invalidate forgets it.
	off bool
}

func newStoreState() *storeState {
	return &storeState{index: tppclient.NewStoreIndex()}
}

// invalidate makes the next use list the store again.
func (st *storeState) invalidate() {
	st.mu.Lock()
	st.loaded, st.off = false, false
	st.mu.Unlock()
}

// clear forgets the store: this device left its group.
func (st *storeState) clear() {
	st.mu.Lock()
	st.index.Reset(nil)
	st.unreadable = nil
	st.loaded, st.off = false, false
	st.mu.Unlock()
}

func (st *storeState) isLoaded() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.loaded
}

// storeHandlers are the client callbacks that keep the index current. Each
// tells every open page to look again.
func (s *Service) storeHandlers(h *tppclient.Handlers) {
	put := func(it tppclient.StoreItem) {
		if s.store.isLoaded() {
			s.store.index.Put(it)
		}
		s.hub.publish(localui.Event{Kind: localui.EventStore})
	}
	h.OnStoreItemAdded = put
	h.OnStoreItemUpdated = put
	h.OnStoreItemDeleted = func(itemID string) {
		s.store.index.Remove(itemID)
		s.hub.publish(localui.Event{Kind: localui.EventStore})
	}
	h.OnStoreState = func(bool) {
		s.store.invalidate()
		s.hub.publish(localui.Event{Kind: localui.EventStore})
	}
}

// ensureStore lists the store unless it is already in memory.
func (s *Service) ensureStore(ctx context.Context, relay Relay) error {
	st := s.store
	st.loadMu.Lock()
	defer st.loadMu.Unlock()
	st.mu.Lock()
	loaded, off := st.loaded, st.off
	st.mu.Unlock()
	switch {
	case loaded:
		return nil
	case off:
		return tppclient.ErrStoreDisabled
	}
	listing, err := relay.ListStore(ctx)
	if errors.Is(err, tppclient.ErrStoreDisabled) {
		st.mu.Lock()
		st.off = true
		st.mu.Unlock()
	}
	if err != nil {
		return fmt.Errorf("service: list the store: %w", err)
	}
	st.mu.Lock()
	st.index.Reset(listing.Items)
	st.unreadable = listing.Unreadable
	st.loaded = true
	st.mu.Unlock()
	if len(listing.Unreadable) > 0 {
		s.logger.WarnContext(ctx, "some store items could not be opened", "items", len(listing.Unreadable))
	}
	return nil
}

// Store implements localui.StoreAPI.
func (s *Service) Store(ctx context.Context, q localui.StoreQuery) (localui.StoreView, error) {
	relay, err := s.ready()
	if err != nil {
		return localui.StoreView{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	limits, err := relay.StoreLimits(ctx)
	if err != nil {
		return localui.StoreView{}, s.storeError("read the store's limits", err)
	}
	if !limits.Enabled {
		s.store.clear()
		s.store.mu.Lock()
		s.store.off = true
		s.store.mu.Unlock()
		return localui.StoreView{Items: []localui.StoreItemView{}, Tags: []localui.StoreTagView{}, Unreadable: []localui.StoreUnreadableView{}}, nil
	}
	if err := s.ensureStore(ctx, relay); err != nil {
		return localui.StoreView{}, s.storeError("list the store", err)
	}

	items := s.store.index.Query(tppclient.StoreQuery{Text: q.Text, Tags: q.Tags, Sort: storeSort(q.Sort)})
	view := localui.StoreView{
		Enabled: true,
		Limits: &localui.StoreLimitsView{
			MaxBytes:       limits.MaxBytes,
			UsedBytes:      limits.UsedBytes,
			AvailableBytes: limits.AvailableBytes,
			MaxItems:       limits.MaxItems,
			UsedItems:      limits.UsedItems,
			MaxItemBytes:   limits.MaxItemBytes,
		},
		Items:      make([]localui.StoreItemView, 0, len(items)),
		Total:      s.store.index.Len(),
		Tags:       []localui.StoreTagView{},
		Unreadable: []localui.StoreUnreadableView{},
	}
	for _, it := range items {
		view.Items = append(view.Items, s.itemView(it))
	}
	for _, t := range s.store.index.Tags() {
		view.Tags = append(view.Tags, localui.StoreTagView{Tag: t.Tag, Count: t.Count})
	}
	s.store.mu.Lock()
	for _, u := range s.store.unreadable {
		view.Unreadable = append(view.Unreadable, localui.StoreUnreadableView{
			ID: u.ID, SavedAt: u.SavedAt, Reason: unreadableReason(u.Err),
		})
	}
	s.store.mu.Unlock()
	return view, nil
}

func storeSort(v string) tppclient.StoreSort {
	switch v {
	case "oldest":
		return tppclient.SortOldest
	case "name":
		return tppclient.SortName
	case "name_desc":
		return tppclient.SortNameDesc
	default:
		return tppclient.SortNewest
	}
}

// unreadableReason says why an item did not open, without content.
func unreadableReason(err error) string {
	if errors.Is(err, tppclient.ErrEpochAhead) {
		return "saved with a group key this device has not received yet"
	}
	return "it could not be decrypted on this device"
}

// SaveToStore implements localui.StoreAPI: it saves the clipboard or a history
// entry.
func (s *Service) SaveToStore(ctx context.Context, req localui.StoreSaveRequest) (localui.StoreItemView, error) {
	relay, err := s.ready()
	if err != nil {
		return localui.StoreItemView{}, err
	}
	var item tppclient.Item
	switch req.Source {
	case "clipboard":
		c, err := s.clip.Read(ctx)
		switch {
		case errors.Is(err, clipboard.ErrEmpty):
			return localui.StoreItemView{}, localui.Errorf(http.StatusConflict, err, "the clipboard is empty")
		case errors.Is(err, clipboard.ErrUnsupported):
			return localui.StoreItemView{}, localui.Errorf(http.StatusNotImplemented, err, "this build cannot reach a clipboard")
		case err != nil:
			return localui.StoreItemView{}, localui.Errorf(http.StatusInternalServerError, err, "the clipboard could not be read")
		}
		item = tppclient.Item{ContentType: c.ContentType, Filename: c.Filename, Body: c.Body, CreatedAt: s.now()}
	case "entry":
		fetchCtx, cancel := context.WithTimeout(ctx, storeTransferTimeout)
		done := s.beginDownload()
		item, err = relay.GetEntry(fetchCtx, req.EntryID)
		done()
		cancel()
		switch {
		case errors.Is(err, tppclient.ErrStaleEntry):
			return localui.StoreItemView{}, localui.Errorf(http.StatusGone, err,
				"that entry predates this device's group key and cannot be opened")
		case err != nil:
			return localui.StoreItemView{}, s.relayError("fetch that entry", err)
		}
	default:
		return localui.StoreItemView{}, localui.Errorf(http.StatusBadRequest, nil, "unknown source %q", req.Source)
	}
	return s.saveToStore(ctx, relay, item, req.StoreDetails, req.AllowDuplicate)
}

// UploadToStore implements localui.StoreAPI: it saves a file from the page.
func (s *Service) UploadToStore(ctx context.Context, up localui.StoreUpload) (localui.StoreItemView, error) {
	relay, err := s.ready()
	if err != nil {
		return localui.StoreItemView{}, err
	}
	if len(up.Body) == 0 {
		return localui.StoreItemView{}, localui.Errorf(http.StatusBadRequest, nil, "that file is empty")
	}
	item := tppclient.Item{
		ContentType: up.ContentType,
		Filename:    clipboard.SafeName(up.Filename),
		Body:        up.Body,
		CreatedAt:   s.now(),
	}
	if item.ContentType == "" {
		item.ContentType = "application/octet-stream"
	}
	return s.saveToStore(ctx, relay, item, up.Details, up.AllowDuplicate)
}

// saveToStore checks for a duplicate, saves, and adds the item to the index.
func (s *Service) saveToStore(ctx context.Context, relay Relay, item tppclient.Item, d localui.StoreDetails, allowDuplicate bool) (localui.StoreItemView, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTransferTimeout)
	defer cancel()
	if err := s.ensureStore(ctx, relay); err != nil {
		return localui.StoreItemView{}, s.storeError("list the store", err)
	}
	if !allowDuplicate {
		if dups := s.store.index.Duplicates(sha256.Sum256(item.Body)); len(dups) > 0 {
			out := &localui.DuplicateError{}
			for _, it := range dups {
				out.Duplicates = append(out.Duplicates, s.itemView(it))
			}
			return localui.StoreItemView{}, out
		}
	}

	s.beginUpload()
	saved, err := relay.SaveToStore(ctx, item, tppclient.StoreDetails{Name: d.Name, Description: d.Description, Tags: d.Tags})
	s.endUpload(nil)
	if err != nil {
		return localui.StoreItemView{}, s.storeError("save to the store", err)
	}
	s.store.index.Put(saved)
	s.logger.InfoContext(ctx, "saved to the store", "item_id", saved.ID, "bytes", saved.BlobSize)
	s.hub.publish(localui.Event{Kind: localui.EventStore})
	return s.itemView(saved), nil
}

// storeItem finds a listed item, which carries the keys an action needs.
func (s *Service) storeItem(itemID string) (tppclient.StoreItem, error) {
	it, ok := s.store.index.Get(itemID)
	if !ok {
		return tppclient.StoreItem{}, localui.Errorf(http.StatusNotFound, nil,
			"that item is no longer in the store; the list has been refreshed")
	}
	return it, nil
}

// EditStoreItem implements localui.StoreAPI. An edit from a stale version is
// refused by the relay; the list is then refreshed so the user sees the
// other device's edit and can make theirs again.
func (s *Service) EditStoreItem(ctx context.Context, itemID string, d localui.StoreDetails) (localui.StoreItemView, error) {
	relay, err := s.ready()
	if err != nil {
		return localui.StoreItemView{}, err
	}
	it, err := s.storeItem(itemID)
	if err != nil {
		return localui.StoreItemView{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	edited, err := relay.EditStoreItem(ctx, it, tppclient.StoreDetails{Name: d.Name, Description: d.Description, Tags: d.Tags})
	if errors.Is(err, tppclient.ErrRevisionConflict) {
		s.store.invalidate()
		s.hub.publish(localui.Event{Kind: localui.EventStore})
		return localui.StoreItemView{}, localui.Errorf(http.StatusConflict, err,
			"another device edited this item first; the list now shows its version")
	}
	if err != nil {
		return localui.StoreItemView{}, s.storeError("edit the item", err)
	}
	s.store.index.Put(edited)
	s.hub.publish(localui.Event{Kind: localui.EventStore})
	return s.itemView(edited), nil
}

// DeleteStoreItem implements localui.StoreAPI. An item another device already
// deleted is simply gone.
func (s *Service) DeleteStoreItem(ctx context.Context, itemID string) error {
	relay, err := s.ready()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	err = relay.DeleteStoreItem(ctx, itemID)
	if err != nil && !errors.Is(err, tppclient.ErrNotFound) {
		return s.storeError("delete the item", err)
	}
	s.store.index.Remove(itemID)
	s.store.mu.Lock()
	for i, u := range s.store.unreadable {
		if u.ID == itemID {
			s.store.unreadable = append(s.store.unreadable[:i], s.store.unreadable[i+1:]...)
			break
		}
	}
	s.store.mu.Unlock()
	s.logger.InfoContext(ctx, "deleted from the store", "item_id", itemID)
	s.hub.publish(localui.Event{Kind: localui.EventStore})
	return nil
}

// fetchStoreItem downloads and decrypts an item.
func (s *Service) fetchStoreItem(ctx context.Context, itemID string) (tppclient.Item, error) {
	relay, err := s.ready()
	if err != nil {
		return tppclient.Item{}, err
	}
	it, err := s.storeItem(itemID)
	if err != nil {
		return tppclient.Item{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, storeTransferTimeout)
	defer cancel()
	defer s.beginDownload()()
	item, err := relay.FetchStoreItem(ctx, it)
	if err != nil {
		if errors.Is(err, tppclient.ErrNotFound) {
			s.store.index.Remove(itemID)
			s.hub.publish(localui.Event{Kind: localui.EventStore})
		}
		return tppclient.Item{}, s.storeError("fetch the item", err)
	}
	return item, nil
}

// CopyStoreItem implements localui.StoreAPI. Like a copy from the history it
// is not uploaded back as a new entry: the watcher is told it is this
// service's own write.
func (s *Service) CopyStoreItem(ctx context.Context, itemID string) (localui.SyncResult, error) {
	item, err := s.fetchStoreItem(ctx, itemID)
	if err != nil {
		return localui.SyncResult{}, err
	}
	item.Meta.CreatedAt = item.CreatedAt
	return s.applyToClipboard(ctx, localui.DirectionDownload, item)
}

// StoreItemContent implements localui.StoreAPI: the bytes for Save as file.
func (s *Service) StoreItemContent(ctx context.Context, itemID string) (localui.EntryContent, error) {
	item, err := s.fetchStoreItem(ctx, itemID)
	if err != nil {
		return localui.EntryContent{}, err
	}
	return localui.EntryContent{ContentType: item.ContentType, Body: item.Body}, nil
}

// StoreItemPreview implements localui.StoreAPI: it fetches and decrypts an
// item and renders it as the history renders an entry, under the same checks.
func (s *Service) StoreItemPreview(ctx context.Context, itemID string) (localui.EntryPreview, error) {
	it, item, format, err := s.openStoreForPreview(ctx, itemID)
	if err != nil {
		return localui.EntryPreview{}, err
	}
	view := previewView(it.ID, it.WrapEpoch, item, format)
	s.logger.DebugContext(ctx, "store item previewed", "item_id", it.ID, "render", view.Render)
	return view, nil
}

// StoreItemMedia implements localui.StoreAPI: an audio or video item's bytes
// for the page to play, typed by the preview table rather than the sender.
// Save as file is StoreItemContent.
func (s *Service) StoreItemMedia(ctx context.Context, itemID string) (localui.EntryContent, error) {
	it, item, format, err := s.openStoreForPreview(ctx, itemID)
	if err != nil {
		return localui.EntryContent{}, err
	}
	if !format.Render.Streamed() {
		return localui.EntryContent{}, localui.Errorf(http.StatusUnprocessableEntity, nil,
			"this item is not audio or video")
	}
	s.logger.DebugContext(ctx, "store item played", "item_id", it.ID, "render", string(format.Render))
	return localui.EntryContent{ContentType: format.MIME, Body: item.Body}, nil
}

// openStoreForPreview fetches and decrypts an item for a preview, and refuses
// it unless the preview table has a format for it that its bytes match.
func (s *Service) openStoreForPreview(ctx context.Context, itemID string) (tppclient.StoreItem, tppclient.Item, preview.Format, error) {
	it, err := s.storeItem(itemID)
	if err != nil {
		return tppclient.StoreItem{}, tppclient.Item{}, preview.Format{}, err
	}
	item, err := s.fetchStoreItem(ctx, itemID)
	if err != nil {
		return tppclient.StoreItem{}, tppclient.Item{}, preview.Format{}, err
	}
	format, err := previewFormat(item, "item")
	if err != nil {
		return tppclient.StoreItem{}, tppclient.Item{}, preview.Format{}, err
	}
	return it, item, format, nil
}

// storeError adds the store's own refusals to relayError's.
func (s *Service) storeError(op string, err error) error {
	switch {
	case errors.Is(err, tppclient.ErrStoreDisabled):
		return localui.Errorf(http.StatusConflict, err, "the store is turned off for this group")
	case errors.Is(err, tppclient.ErrQuotaExceeded):
		return localui.Errorf(http.StatusInsufficientStorage, err,
			"the store is full: delete some items, or ask the server admin for more room")
	case errors.Is(err, tppclient.ErrTooLarge):
		return localui.Errorf(http.StatusRequestEntityTooLarge, err, "that is larger than the store accepts")
	case errors.Is(err, tppclient.ErrStoreUnreadable):
		return localui.Errorf(http.StatusUnprocessableEntity, err, "that item could not be decrypted on this device")
	case errors.Is(err, tppclient.ErrClientOutdated):
		return localui.Errorf(http.StatusUpgradeRequired, err, "the server needs a newer version of this app")
	case errors.Is(err, tppcrypto.ErrMalformed):
		return localui.Errorf(http.StatusBadRequest, err,
			"check the details: a name is required, and each field has a length limit")
	}
	return s.relayError(op, err)
}

// itemView describes an item for the page, with the auto-download limit
// that bounds its preview.
func (s *Service) itemView(it tppclient.StoreItem) localui.StoreItemView {
	s.mu.Lock()
	limit := s.settings.AutoDownloadLimit()
	s.mu.Unlock()
	return storeItemView(it, limit)
}

// storeItemView describes an item for the page. Whether it can be previewed
// is decided as for a history entry (entryView): a format the preview table
// knows, within the auto-download limit, since a preview fetches the item.
func storeItemView(it tppclient.StoreItem, downloadLimit int64) localui.StoreItemView {
	tags := it.Tags
	if tags == nil {
		tags = []string{}
	}
	v := localui.StoreItemView{
		ID:          it.ID,
		Name:        it.Name,
		Description: it.Description,
		Tags:        tags,
		SavedAt:     it.SavedAt,
		ContentType: it.ContentType,
		Bytes:       it.BodySize,
	}
	if it.Filename != "" {
		v.Filename = clipboard.SafeName(it.Filename)
	}
	v.Kind = string(headerKind(tppclient.EntryHeader{ContentType: it.ContentType, Filename: it.Filename}))
	format, ok := preview.ForEntry(it.Filename, it.ContentType)
	switch {
	case !ok:
		v.NoPreview = "no preview for this type"
	case downloadLimit > 0 && it.BodySize > downloadLimit:
		v.NoPreview = "larger than the auto-download limit"
	default:
		v.Previewable = true
		v.Render = string(format.Render)
	}
	return v
}

// savedMark reports whether the store holds an entry's content. The store is
// listed for it once, best effort: a history page without marks is still a
// history page.
func (s *Service) savedMarks(ctx context.Context, relay Relay) {
	if err := s.ensureStore(ctx, relay); err != nil && !errors.Is(err, tppclient.ErrStoreDisabled) {
		s.logger.DebugContext(ctx, "the store could not be listed for the history's marks", "error", err)
	}
}

// savedMark reports whether the store holds an entry's content.
func (s *Service) savedMark(m tppclient.EntryMeta) bool {
	if m.Header == nil || !m.Header.HasSHA256 || !s.store.isLoaded() {
		return false
	}
	return len(s.store.index.Duplicates(m.Header.SHA256)) > 0
}
