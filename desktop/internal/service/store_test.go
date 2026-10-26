package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// statusOf is the HTTP status a service error would answer with.
func statusOf(err error) int {
	var se *localui.StatusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

func (f *fixture) enableStore() {
	f.relay.mu.Lock()
	f.relay.storeLocked().enabled = true
	f.relay.mu.Unlock()
}

func details(name string, tags ...string) localui.StoreDetails {
	return localui.StoreDetails{Name: name, Tags: tags}
}

// TestStoreOffSaysSo: until the admin turns the store on, the tab is told so
// and a save is refused in words, not as a failure.
func TestStoreOffSaysSo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	view, err := f.svc.Store(ctx, localui.StoreQuery{})
	if err != nil || view.Enabled {
		t.Fatalf("Store() with the store off = %+v, %v, want disabled", view, err)
	}
	f.clip.set(textContent("hello"))
	_, err = f.svc.SaveToStore(ctx, localui.StoreSaveRequest{Source: "clipboard", StoreDetails: details("Note")})
	if got := statusOf(err); got != http.StatusConflict {
		t.Errorf("SaveToStore with the store off = %v (status %d), want 409", err, got)
	}
}

// TestSaveSearchAndDuplicates: the clipboard, a history entry and a file are
// saved; the listing searches and filters; and a body already in the store is
// refused until the user says "Save anyway".
func TestSaveSearchAndDuplicates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.enableStore()

	f.clip.set(textContent("wifi: hunter2"))
	note, err := f.svc.SaveToStore(ctx, localui.StoreSaveRequest{
		Source: "clipboard", StoreDetails: localui.StoreDetails{Name: "Home wifi", Description: "the router", Tags: []string{"home"}},
	})
	if err != nil {
		t.Fatalf("SaveToStore(clipboard) error = %v", err)
	}
	if note.Kind != "text" || note.Bytes != int64(len("wifi: hunter2")) {
		t.Errorf("SaveToStore(clipboard) = %+v", note)
	}

	meta, err := f.relay.PutEntry(ctx, tppclient.Item{ContentType: "image/png", Body: []byte("png"), CreatedAt: f.now()})
	if err != nil {
		t.Fatalf("PutEntry() error = %v", err)
	}
	if _, err := f.svc.SaveToStore(ctx, localui.StoreSaveRequest{Source: "entry", EntryID: meta.ID, StoreDetails: details("Logo", "work")}); err != nil {
		t.Fatalf("SaveToStore(entry) error = %v", err)
	}
	if _, err := f.svc.UploadToStore(ctx, localui.StoreUpload{
		ContentType: "application/pdf", Filename: "../manual.pdf", Body: []byte("%PDF"), Details: details("Manual", "home"),
	}); err != nil {
		t.Fatalf("UploadToStore() error = %v", err)
	}

	view, err := f.svc.Store(ctx, localui.StoreQuery{})
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if view.Total != 3 || len(view.Items) != 3 || view.Limits == nil {
		t.Fatalf("Store() = %d of %d items, limits %v; want all three", len(view.Items), view.Total, view.Limits)
	}
	if got, want := view.Tags, []localui.StoreTagView{{Tag: "home", Count: 2}, {Tag: "work", Count: 1}}; !slices.Equal(got, want) {
		t.Errorf("Store().Tags = %v, want %v", got, want)
	}
	for _, it := range view.Items {
		if it.Name == "Manual" && it.Filename != "manual.pdf" {
			t.Errorf("the uploaded file is named %q, want the bare name manual.pdf", it.Filename)
		}
	}
	view, _ = f.svc.Store(ctx, localui.StoreQuery{Text: "router", Tags: []string{"home"}})
	if len(view.Items) != 1 || view.Items[0].Name != "Home wifi" || view.Total != 3 {
		t.Errorf("Store(router, home) = %v of %d, want the wifi note", view.Items, view.Total)
	}

	_, err = f.svc.SaveToStore(ctx, localui.StoreSaveRequest{Source: "clipboard", StoreDetails: details("Again")})
	var dup *localui.DuplicateError
	if !errors.As(err, &dup) || len(dup.Duplicates) != 1 || dup.Duplicates[0].Name != "Home wifi" {
		t.Fatalf("SaveToStore(duplicate) = %v, want a DuplicateError naming the note", err)
	}
	if _, err := f.svc.SaveToStore(ctx, localui.StoreSaveRequest{
		Source: "clipboard", StoreDetails: details("Again"), AllowDuplicate: true,
	}); err != nil {
		t.Errorf("SaveToStore(duplicate, save anyway) error = %v", err)
	}

	// The history marks what the store holds, by the hash in the header.
	page, err := f.svc.History(ctx, localui.HistoryQuery{})
	if err != nil || len(page.Entries) != 1 || !page.Entries[0].Saved {
		t.Errorf("History() = %+v, %v; want the logo entry marked saved", page.Entries, err)
	}
	if lists := f.relay.storeLists; lists != 1 {
		t.Errorf("the store was listed %d times, want once", lists)
	}
}

// TestStoreItemActions: copy writes the clipboard, an edit from a stale
// version is refused and refreshes the list, and a delete is final — also
// for an item another device deleted first.
func TestStoreItemActions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.enableStore()
	f.clip.set(textContent("the text"))
	saved, err := f.svc.SaveToStore(ctx, localui.StoreSaveRequest{Source: "clipboard", StoreDetails: details("Text")})
	if err != nil {
		t.Fatalf("SaveToStore() error = %v", err)
	}

	f.clip.set(textContent("something else"))
	if _, err := f.svc.CopyStoreItem(ctx, saved.ID); err != nil {
		t.Fatalf("CopyStoreItem() error = %v", err)
	}
	if c, _ := f.clip.Read(ctx); string(c.Body) != "the text" {
		t.Errorf("clipboard after CopyStoreItem = %q, want the item", c.Body)
	}
	content, err := f.svc.StoreItemContent(ctx, saved.ID)
	if err != nil || string(content.Body) != "the text" {
		t.Errorf("StoreItemContent() = %q, %v", content.Body, err)
	}

	edited, err := f.svc.EditStoreItem(ctx, saved.ID, details("Renamed", "x"))
	if err != nil || edited.Name != "Renamed" || !slices.Equal(edited.Tags, []string{"x"}) {
		t.Fatalf("EditStoreItem() = %+v, %v", edited, err)
	}
	// Another device edits it; this one still holds revision 1.
	f.relay.mu.Lock()
	f.relay.store.items[0].MetaRev, f.relay.store.items[0].Name = 2, "Theirs"
	f.relay.mu.Unlock()
	if _, err := f.svc.EditStoreItem(ctx, saved.ID, details("Mine")); statusOf(err) != http.StatusConflict {
		t.Errorf("EditStoreItem(stale) = %v, want 409", err)
	}
	view, _ := f.svc.Store(ctx, localui.StoreQuery{})
	if len(view.Items) != 1 || view.Items[0].Name != "Theirs" {
		t.Errorf("Store() after the conflict = %v, want the other device's version", view.Items)
	}

	if err := f.svc.DeleteStoreItem(ctx, saved.ID); err != nil {
		t.Fatalf("DeleteStoreItem() error = %v", err)
	}
	if err := f.svc.DeleteStoreItem(ctx, saved.ID); err != nil {
		t.Errorf("DeleteStoreItem(already gone) error = %v, want nil", err)
	}
	if view, _ := f.svc.Store(ctx, localui.StoreQuery{}); len(view.Items) != 0 {
		t.Errorf("Store() after the delete = %v, want empty", view.Items)
	}
	if _, err := f.svc.CopyStoreItem(ctx, saved.ID); statusOf(err) != http.StatusNotFound {
		t.Errorf("CopyStoreItem(deleted) = %v, want 404", err)
	}
}

// TestStoreEventsKeepTheIndex: items another device saves or deletes reach
// the index, and a reconnect lists the store again.
func TestStoreEventsKeepTheIndex(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.enableStore()
	h := f.svc.ClientHandlers()
	if _, err := f.svc.Store(ctx, localui.StoreQuery{}); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	h.OnStoreItemAdded(tppclient.StoreItem{ID: "theirs", StoreDetails: tppclient.StoreDetails{Name: "From the phone"}})
	view, _ := f.svc.Store(ctx, localui.StoreQuery{})
	if len(view.Items) != 1 || view.Items[0].Name != "From the phone" {
		t.Errorf("Store() after an added event = %v", view.Items)
	}
	h.OnStoreItemDeleted("theirs")
	if view, _ := f.svc.Store(ctx, localui.StoreQuery{}); len(view.Items) != 0 {
		t.Errorf("Store() after a deleted event = %v", view.Items)
	}

	before := f.relay.storeLists
	h.OnConnected()
	f.svc.catchUps.Wait()
	_, _ = f.svc.Store(ctx, localui.StoreQuery{})
	if f.relay.storeLists != before+1 {
		t.Errorf("a reconnect did not list the store again: %d listings, want %d", f.relay.storeLists, before+1)
	}
}

// TestStoreItemPreview: a store item previews as a history entry does — from
// the preview table, within the auto-download limit, and only when its bytes
// are what its name claims; audio and video are played from their own bytes,
// typed by the table.
func TestStoreItemPreview(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.enableStore()
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{AutoDownloadMaxBytes: int64Ptr(1 << 20)}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}

	f.clip.set(textContent("wifi: hunter2"))
	note, err := f.svc.SaveToStore(ctx, localui.StoreSaveRequest{Source: "clipboard", StoreDetails: details("Home wifi")})
	if err != nil {
		t.Fatalf("SaveToStore() error = %v", err)
	}
	upload := func(name, contentType string, body []byte) localui.StoreItemView {
		t.Helper()
		v, err := f.svc.UploadToStore(ctx, localui.StoreUpload{
			ContentType: contentType, Filename: name, Body: body, Details: details(name),
		})
		if err != nil {
			t.Fatalf("UploadToStore(%s) error = %v", name, err)
		}
		return v
	}
	song := upload("song.mp3", "audio/mpeg", []byte("ID3 not really a song"))
	fake := upload("logo.png", "image/png", []byte("<html>not a png</html>"))
	manual := upload("manual.pdf", "application/pdf", []byte("%PDF"))
	big := upload("big.txt", "text/plain", bytes.Repeat([]byte("a"), 2<<20))

	tests := []struct {
		name        string
		item        localui.StoreItemView
		previewable bool
		render      string
	}{
		{"clipboard text", note, true, "text"},
		{"audio file", song, true, "audio"},
		{"a .png by name, decided before fetching", fake, true, "image"},
		{"a type with no preview", manual, false, ""},
		{"over the auto-download limit", big, false, ""},
	}
	for _, tt := range tests {
		if tt.item.Previewable != tt.previewable || tt.item.Render != tt.render {
			t.Errorf("%s: previewable = %v render = %q, want %v %q", tt.name, tt.item.Previewable, tt.item.Render, tt.previewable, tt.render)
		}
		if !tt.previewable && tt.item.NoPreview == "" {
			t.Errorf("%s: no reason given for no preview", tt.name)
		}
	}

	view, err := f.svc.StoreItemPreview(ctx, note.ID)
	if err != nil || view.Render != "text" || view.Text != "wifi: hunter2" || view.ID != note.ID {
		t.Errorf("StoreItemPreview(text) = %+v, %v", view, err)
	}
	if view, err := f.svc.StoreItemPreview(ctx, song.ID); err != nil || view.Render != "audio" || view.Text != "" {
		t.Errorf("StoreItemPreview(audio) = %+v, %v, want a description only", view, err)
	}
	if _, err := f.svc.StoreItemPreview(ctx, fake.ID); statusOf(err) != http.StatusUnprocessableEntity {
		t.Errorf("StoreItemPreview(HTML named .png) = %v, want 422", err)
	}

	media, err := f.svc.StoreItemMedia(ctx, song.ID)
	if err != nil || media.ContentType != "audio/mpeg" || string(media.Body) != "ID3 not really a song" {
		t.Errorf("StoreItemMedia(audio) = %q %q, %v", media.ContentType, media.Body, err)
	}
	if _, err := f.svc.StoreItemMedia(ctx, note.ID); statusOf(err) != http.StatusUnprocessableEntity {
		t.Errorf("StoreItemMedia(text) = %v, want 422", err)
	}
	if _, err := f.svc.StoreItemPreview(ctx, "gone"); statusOf(err) != http.StatusNotFound {
		t.Errorf("StoreItemPreview(unknown) = %v, want 404", err)
	}
}
