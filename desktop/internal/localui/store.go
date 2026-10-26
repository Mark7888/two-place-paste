package localui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The store (SPEC §6b): items the group keeps on purpose, beside its 24-hour
// history. Everything here is decrypted in the service and searched there; the
// page sees what it shows, and the bytes of an item only when the user saves
// it as a file.

// StoreAPI is the store, as the UI drives it. Nil in Options means this build
// has none, and the store routes answer 501.
type StoreAPI interface {
	// Store lists the store: its limits, the items that match the query,
	// the tags in use, and the items that could not be opened.
	Store(ctx context.Context, q StoreQuery) (StoreView, error)

	// SaveToStore saves the clipboard or a history entry with the details
	// the user typed. A body already in the store is refused with a
	// *DuplicateError unless the request allows it.
	SaveToStore(ctx context.Context, req StoreSaveRequest) (StoreItemView, error)

	// UploadToStore saves a file the user dropped or picked.
	UploadToStore(ctx context.Context, up StoreUpload) (StoreItemView, error)

	// EditStoreItem replaces an item's name, description and tags.
	EditStoreItem(ctx context.Context, itemID string, d StoreDetails) (StoreItemView, error)

	// DeleteStoreItem deletes an item for every device of the group.
	DeleteStoreItem(ctx context.Context, itemID string) error

	// CopyStoreItem puts an item on the local clipboard.
	CopyStoreItem(ctx context.Context, itemID string) (SyncResult, error)

	// StoreItemContent decrypts an item for the user to save as a file.
	StoreItemContent(ctx context.Context, itemID string) (EntryContent, error)

	// StoreItemPreview decrypts an item and renders it for display, as
	// EntryPreview does an entry. StoreItemMedia returns an audio or video
	// item's bytes for the preview to play, as EntryContent does.
	StoreItemPreview(ctx context.Context, itemID string) (EntryPreview, error)
	StoreItemMedia(ctx context.Context, itemID string) (EntryContent, error)
}

// StoreQuery selects and orders the listing. Sort is "newest" (the default),
// "oldest", "name", "name_desc", "largest" or "smallest".
type StoreQuery struct {
	Text string
	Tags []string
	Sort string
}

// StoreDetails is what the user types about an item.
type StoreDetails struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// StoreView is the Store tab's model. When Enabled is false the admin has not
// turned the store on for this group, and nothing else is set.
type StoreView struct {
	Enabled bool `json:"enabled"`

	Limits *StoreLimitsView `json:"limits,omitempty"`

	// Items match the query; Total counts every item, matching or not.
	Items []StoreItemView `json:"items"`
	Total int             `json:"total"`

	// Tags are every tag in use, with counts, by name.
	Tags []StoreTagView `json:"tags"`

	// Unreadable are items listed by the relay that this device could not
	// open. The page shows them with a Delete action.
	Unreadable []StoreUnreadableView `json:"unreadable"`
}

// StoreLimitsView is the store's room, in bytes the relay counts.
type StoreLimitsView struct {
	MaxBytes       int64 `json:"max_bytes"`
	UsedBytes      int64 `json:"used_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
	MaxItems       int64 `json:"max_items"`
	UsedItems      int64 `json:"used_items"`
	MaxItemBytes   int64 `json:"max_item_bytes"`
}

// StoreItemView is one item, as the page lists it.
type StoreItemView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Tags        []string  `json:"tags"`
	SavedAt     time.Time `json:"saved_at"`

	// Kind is "text", "image" or "file", as for a history entry.
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	Filename    string `json:"filename,omitempty"`
	// Bytes is the plaintext size.
	Bytes int64 `json:"bytes"`

	// Previewable, NoPreview and Render are as for a history entry
	// (EntryView): whether the tab offers a preview, why not, and how it is
	// drawn.
	Previewable bool   `json:"previewable"`
	NoPreview   string `json:"no_preview,omitempty"`
	Render      string `json:"render,omitempty"`
}

// StoreTagView is a tag and how many items carry it.
type StoreTagView struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// StoreUnreadableView is an item that did not open.
type StoreUnreadableView struct {
	ID      string    `json:"id"`
	SavedAt time.Time `json:"saved_at"`
	Reason  string    `json:"reason"`
}

// StoreSaveRequest saves what is already on this device: the clipboard, or a
// history entry by id.
type StoreSaveRequest struct {
	// Source is "clipboard" or "entry".
	Source  string `json:"source"`
	EntryID string `json:"entry_id,omitempty"`

	StoreDetails
	// AllowDuplicate is the dialog's "Save anyway".
	AllowDuplicate bool `json:"allow_duplicate"`
}

// StoreUpload is a file the page sent.
type StoreUpload struct {
	ContentType string
	Filename    string
	Body        []byte

	Details        StoreDetails
	AllowDuplicate bool
}

// DuplicateError refuses a save whose body the store already holds. The page
// shows the items and offers "Save anyway".
type DuplicateError struct {
	Duplicates []StoreItemView
}

func (e *DuplicateError) Error() string {
	return "the store already holds this content"
}

// maxStoreUploadBytes bounds a file the page uploads. The relay's own limit is
// lower, and the service checks it; this only keeps a runaway request from
// filling memory.
const maxStoreUploadBytes = 512 << 20

func (s *Server) storeOff(w http.ResponseWriter) bool {
	if s.store == nil {
		writeError(w, http.StatusNotImplemented, "this build has no store")
		return true
	}
	return false
}

func (s *Server) handleStore(w http.ResponseWriter, r *http.Request) {
	if s.storeOff(w) {
		return
	}
	v := r.URL.Query()
	q := StoreQuery{Text: v.Get("q"), Tags: v["tag"], Sort: v.Get("sort")}
	switch q.Sort {
	case "", "newest", "oldest", "name", "name_desc", "largest", "smallest":
	default:
		writeError(w, http.StatusBadRequest, "unknown sort")
		return
	}
	view, err := s.store.Store(r.Context(), q)
	if err != nil {
		s.fail(w, r, "list the store", err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleStoreSave(w http.ResponseWriter, r *http.Request) {
	if s.storeOff(w) {
		return
	}
	var req StoreSaveRequest
	if !decode(w, r, &req) {
		return
	}
	switch {
	case req.Source == "clipboard" && req.EntryID == "":
	case req.Source == "entry" && req.EntryID != "":
	default:
		writeError(w, http.StatusBadRequest, `source must be "clipboard", or "entry" with an entry_id`)
		return
	}
	item, err := s.store.SaveToStore(r.Context(), req)
	if err != nil {
		s.storeFail(w, r, "save to the store", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// handleStoreUpload takes a file as multipart form data: "details" (the JSON
// of StoreDetails), "allow_duplicate" ("true" or absent) and "file". The file
// is read through a limit, never into a buffer sized from the request.
func (s *Server) handleStoreUpload(w http.ResponseWriter, r *http.Request) {
	if s.storeOff(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxStoreUploadBytes+maxRequestBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "the upload is not multipart form data")
		return
	}
	var (
		up       StoreUpload
		haveFile bool
		haveMeta bool
	)
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "the upload could not be read")
			return
		}
		switch part.FormName() {
		case "details":
			dec := json.NewDecoder(io.LimitReader(part, maxRequestBytes))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&up.Details); err != nil {
				writeError(w, http.StatusBadRequest, "details is not the JSON this endpoint expects")
				return
			}
			haveMeta = true
		case "allow_duplicate":
			v, _ := io.ReadAll(io.LimitReader(part, 16))
			up.AllowDuplicate = strings.TrimSpace(string(v)) == "true"
		case "file":
			body, err := io.ReadAll(io.LimitReader(part, maxStoreUploadBytes+1))
			if err != nil {
				writeError(w, http.StatusBadRequest, "the file could not be read")
				return
			}
			if len(body) > maxStoreUploadBytes {
				writeError(w, http.StatusRequestEntityTooLarge, "that file is larger than the store accepts")
				return
			}
			up.Body = body
			up.Filename = part.FileName()
			up.ContentType = uploadContentType(part.Header.Get("Content-Type"))
			haveFile = true
		}
		_ = part.Close()
	}
	if !haveFile || !haveMeta {
		writeError(w, http.StatusBadRequest, "the upload needs details and a file")
		return
	}
	item, err := s.store.UploadToStore(r.Context(), up)
	if err != nil {
		s.storeFail(w, r, "save the file to the store", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// uploadContentType keeps a browser's media type when it is a plausible one;
// a browser sends none for a type it does not know.
func uploadContentType(v string) string {
	if mt, params, err := mime.ParseMediaType(v); err == nil && mt != "" {
		return mime.FormatMediaType(mt, params)
	}
	return "application/octet-stream"
}

func (s *Server) handleStoreEdit(w http.ResponseWriter, r *http.Request) {
	if s.storeOff(w) {
		return
	}
	var body struct {
		ItemID string `json:"item_id"`
		StoreDetails
	}
	if !decode(w, r, &body) {
		return
	}
	if body.ItemID == "" {
		writeError(w, http.StatusBadRequest, "item_id is required")
		return
	}
	item, err := s.store.EditStoreItem(r.Context(), body.ItemID, body.StoreDetails)
	if err != nil {
		s.fail(w, r, "edit the item", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// storeItemID decodes the {"item_id": ...} body the item actions share.
func storeItemID(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		ItemID string `json:"item_id"`
	}
	if !decode(w, r, &body) {
		return "", false
	}
	if body.ItemID == "" {
		writeError(w, http.StatusBadRequest, "item_id is required")
		return "", false
	}
	return body.ItemID, true
}

func (s *Server) handleStoreDelete(w http.ResponseWriter, r *http.Request) {
	if s.storeOff(w) {
		return
	}
	id, ok := storeItemID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteStoreItem(r.Context(), id); err != nil {
		s.fail(w, r, "delete the item", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStoreCopy(w http.ResponseWriter, r *http.Request) {
	if s.storeOff(w) {
		return
	}
	id, ok := storeItemID(w, r)
	if !ok {
		return
	}
	res, err := s.store.CopyStoreItem(r.Context(), id)
	if err != nil {
		s.fail(w, r, "copy the item", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleStoreContent returns an item's bytes for Save as file. Like
// EntryContent it is a POST, and the bytes come back as an attachment the
// browser will not render.
func (s *Server) handleStoreContent(w http.ResponseWriter, r *http.Request) {
	if s.storeOff(w) {
		return
	}
	id, ok := storeItemID(w, r)
	if !ok {
		return
	}
	content, err := s.store.StoreItemContent(r.Context(), id)
	if err != nil {
		s.fail(w, r, "read the item", err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", content.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(content.Body)))
	h.Set("Content-Disposition", "attachment")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content.Body)
}

// handleStorePreview renders an item for the Store tab's preview, as
// handleEntryPreview does an entry.
func (s *Server) handleStorePreview(w http.ResponseWriter, r *http.Request) {
	if s.storeOff(w) {
		return
	}
	id, ok := storeItemID(w, r)
	if !ok {
		return
	}
	view, err := s.store.StoreItemPreview(r.Context(), id)
	if err != nil {
		s.fail(w, r, "preview the item", err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleStoreMedia sends an audio or video item's bytes for its preview to
// play, under the same headers as handleEntryContent: never a page.
func (s *Server) handleStoreMedia(w http.ResponseWriter, r *http.Request) {
	if s.storeOff(w) {
		return
	}
	id, ok := storeItemID(w, r)
	if !ok {
		return
	}
	content, err := s.store.StoreItemMedia(r.Context(), id)
	if err != nil {
		s.fail(w, r, "play the item", err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", content.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(content.Body)))
	h.Set("Content-Disposition", "attachment")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content.Body)
}

// storeFail answers a refused duplicate with the items it duplicates, so the
// dialog can name them; anything else as fail does.
func (s *Server) storeFail(w http.ResponseWriter, r *http.Request, op string, err error) {
	var dup *DuplicateError
	if errors.As(err, &dup) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":      dup.Error(),
			"duplicates": dup.Duplicates,
		})
		return
	}
	s.fail(w, r, op, err)
}
