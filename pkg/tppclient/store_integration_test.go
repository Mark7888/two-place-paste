package tppclient_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// storeEvents collects what the store handlers delivered.
type storeEvents struct {
	added   chan tppclient.StoreItem
	updated chan tppclient.StoreItem
	deleted chan string
	state   chan bool
}

func newStoreEvents() *storeEvents {
	return &storeEvents{
		added:   make(chan tppclient.StoreItem, 8),
		updated: make(chan tppclient.StoreItem, 8),
		deleted: make(chan string, 8),
		state:   make(chan bool, 8),
	}
}

func (e *storeEvents) handlers() tppclient.Handlers {
	return tppclient.Handlers{
		OnStoreItemAdded:   func(it tppclient.StoreItem) { e.added <- it },
		OnStoreItemUpdated: func(it tppclient.StoreItem) { e.updated <- it },
		OnStoreItemDeleted: func(id string) { e.deleted <- id },
		OnStoreState:       func(on bool) { e.state <- on },
	}
}

func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// TestStore runs the store against a real relay (SPEC §6b, /spec/crypto.md
// §7a): off until the admin turns it on; an item saved in one frame and one in
// chunks, announced to the other device with its metadata opened; listed,
// searched and read back byte for byte; edited with a revision check; still
// readable by the remaining devices after a rekey, through the link the rekey
// carried; and deleted for everyone.
func TestStore(t *testing.T) {
	relay := startRelay(t)
	ctx := testContext(t)

	desktop, _ := newClient(t, "desktop", tppclient.Options{})
	if err := desktop.CreateGroup(ctx, relay.creationURL(t, "Store")); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	phoneEvents := newStoreEvents()
	phone, _ := newClient(t, "phone", tppclient.Options{Handlers: phoneEvents.handlers()})
	pair(ctx, t, desktop, phone)
	tablet, _ := newClient(t, "tablet", tppclient.Options{})
	tabletDevice := pair(ctx, t, desktop, tablet)

	note := tppclient.Item{ContentType: "text/plain; charset=utf-8", Body: []byte("wifi: correct horse battery staple")}
	details := tppclient.StoreDetails{Name: "Home wifi", Description: "The router in the hall", Tags: []string{"home", "network"}}

	// --- Off until the admin turns it on -----------------------------------
	if l, err := desktop.StoreLimits(ctx); err != nil || l.Enabled {
		t.Fatalf("StoreLimits before the admin turned it on = %+v, %v, want disabled", l, err)
	}
	if _, err := desktop.SaveToStore(ctx, note, details); !errors.Is(err, tppclient.ErrStoreDisabled) {
		t.Fatalf("SaveToStore with the store off = %v, want ErrStoreDisabled", err)
	}
	admin := relay.admin(t)
	handle := admin.groupHandle(t, "Store")
	admin.call(t, http.MethodPut, "/admin/api/groups/"+handle+"/store", map[string]bool{"enabled": true}, http.StatusNoContent, nil)
	if !receive(t, phoneEvents.state, "the store to be announced on") {
		t.Fatal("OnStoreState(false), want true")
	}
	limits, err := desktop.StoreLimits(ctx)
	if err != nil || !limits.Enabled || limits.AvailableBytes <= 0 {
		t.Fatalf("StoreLimits after turning it on = %+v, %v", limits, err)
	}

	// --- Save in one frame and in chunks ------------------------------------
	saved, err := desktop.SaveToStore(ctx, note, details)
	if err != nil {
		t.Fatalf("SaveToStore(note): %v", err)
	}
	if saved.Name != "Home wifi" || saved.BodySize != int64(len(note.Body)) || saved.SHA256 != sha256.Sum256(note.Body) || saved.WrapEpoch != 1 {
		t.Errorf("SaveToStore(note) = %+v", saved)
	}
	if got := receive(t, phoneEvents.added, "the phone to hear of the note"); got.ID != saved.ID || got.Name != "Home wifi" ||
		!slices.Equal(got.Tags, details.Tags) {
		t.Errorf("OnStoreItemAdded = %+v, want the note with its details", got)
	}

	big := randomBody(t, 12<<20) // over one frame: chunked
	bigItem, err := desktop.SaveToStore(ctx,
		tppclient.Item{ContentType: "application/pdf", Filename: "manual.pdf", Body: big},
		tppclient.StoreDetails{Name: "Boiler manual", Tags: []string{"home"}})
	if err != nil {
		t.Fatalf("SaveToStore(12 MiB): %v", err)
	}
	receive(t, phoneEvents.added, "the phone to hear of the manual")

	// --- List, search, read back ---------------------------------------------
	listing, err := phone.ListStore(ctx)
	if err != nil {
		t.Fatalf("ListStore: %v", err)
	}
	if len(listing.Items) != 2 || len(listing.Unreadable) != 0 {
		t.Fatalf("ListStore = %d items, %d unreadable, want 2 and 0", len(listing.Items), len(listing.Unreadable))
	}
	index := tppclient.NewStoreIndex()
	index.Reset(listing.Items)
	if got := index.Query(tppclient.StoreQuery{Text: "ROUTER"}); len(got) != 1 || got[0].ID != saved.ID {
		t.Errorf("Query(router) = %v, want the note", got)
	}
	if got := index.Query(tppclient.StoreQuery{Tags: []string{"home"}, Sort: tppclient.SortName}); len(got) != 2 || got[0].ID != bigItem.ID {
		t.Errorf("Query(tag home, by name) = %v, want the manual then the note", got)
	}
	if got := index.Duplicates(sha256.Sum256(note.Body)); len(got) != 1 || got[0].ID != saved.ID {
		t.Errorf("Duplicates(note) = %v, want the note", got)
	}

	noteOnPhone, _ := index.Get(saved.ID)
	got, err := phone.FetchStoreItem(ctx, noteOnPhone)
	if err != nil || !bytes.Equal(got.Body, note.Body) || got.ContentType != note.ContentType {
		t.Errorf("FetchStoreItem(note) = %q, %v", got.Body, err)
	}
	bigOnPhone, _ := index.Get(bigItem.ID)
	got, err = phone.FetchStoreItem(ctx, bigOnPhone)
	if err != nil || !bytes.Equal(got.Body, big) || got.Filename != "manual.pdf" {
		t.Errorf("FetchStoreItem(manual) = %d bytes named %q, %v; want the 12 MiB sent", len(got.Body), got.Filename, err)
	}

	// --- Edit: the first edit wins, a stale one is refused ---------------------
	edited, err := phone.EditStoreItem(ctx, noteOnPhone, tppclient.StoreDetails{Name: "Wifi", Tags: []string{"home"}})
	if err != nil {
		t.Fatalf("EditStoreItem: %v", err)
	}
	if edited.MetaRev != 1 || edited.Name != "Wifi" {
		t.Errorf("EditStoreItem = rev %d %q, want rev 1 Wifi", edited.MetaRev, edited.Name)
	}
	if _, err := desktop.EditStoreItem(ctx, saved, tppclient.StoreDetails{Name: "Stale"}); !errors.Is(err, tppclient.ErrRevisionConflict) {
		t.Errorf("EditStoreItem from a stale revision = %v, want ErrRevisionConflict", err)
	}
	if _, err := desktop.SaveToStore(ctx, note, tppclient.StoreDetails{Name: ""}); err == nil {
		t.Error("SaveToStore with an empty name succeeded")
	}

	// --- A rekey: older items stay readable through the link --------------------
	rev, err := desktop.Revoke(ctx, tabletDevice.ID)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := rev.Confirm(ctx); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	waitFor(t, "the phone to reach epoch 2", func() bool { return phone.Epoch() == 2 })
	// An edit pushed now is of an item wrapped at epoch 1: the phone opens it
	// through the link, off the read loop.
	if _, err := desktop.EditStoreItem(ctx, bigItem, tppclient.StoreDetails{Name: "Boiler manual (2019)", Tags: []string{"home"}}); err != nil {
		t.Fatalf("EditStoreItem after the rekey: %v", err)
	}
	if got := receive(t, phoneEvents.updated, "the phone to hear of the edit"); got.ID != bigItem.ID || got.Name != "Boiler manual (2019)" || got.MetaRev != 1 {
		t.Errorf("OnStoreItemUpdated = %q rev %d, want the renamed manual at rev 1", got.Name, got.MetaRev)
	}
	listing, err = phone.ListStore(ctx)
	if err != nil {
		t.Fatalf("ListStore after the rekey: %v", err)
	}
	if len(listing.Items) != 2 || len(listing.Unreadable) != 0 {
		t.Fatalf("ListStore after the rekey = %d items, %d unreadable, want 2 and 0", len(listing.Items), len(listing.Unreadable))
	}
	index.Reset(listing.Items)
	if it, _ := index.Get(saved.ID); it.Name != "Wifi" || it.WrapEpoch != 1 {
		t.Errorf("the note after the rekey = %+v, want it as edited, wrapped at epoch 1", it)
	}
	if it, _ := index.Get(bigItem.ID); true {
		got, err := phone.FetchStoreItem(ctx, it)
		if err != nil || !bytes.Equal(got.Body, big) {
			t.Errorf("FetchStoreItem(manual) after the rekey = %d bytes, %v", len(got.Body), err)
		}
	}
	// A save now is wrapped at the new epoch and readable as well.
	fresh, err := desktop.SaveToStore(ctx, tppclient.Item{ContentType: "text/plain; charset=utf-8", Body: []byte("after")},
		tppclient.StoreDetails{Name: "After the rekey"})
	if err != nil || fresh.WrapEpoch != 2 {
		t.Fatalf("SaveToStore after the rekey = %+v, %v", fresh, err)
	}
	receive(t, phoneEvents.added, "the phone to hear of the new item")

	// --- Delete for everyone --------------------------------------------------
	if err := desktop.DeleteStoreItem(ctx, saved.ID); err != nil {
		t.Fatalf("DeleteStoreItem: %v", err)
	}
	if id := receive(t, phoneEvents.deleted, "the phone to hear of the delete"); id != saved.ID {
		t.Errorf("OnStoreItemDeleted = %q, want %q", id, saved.ID)
	}
	if _, err := phone.FetchStoreItem(ctx, noteOnPhone); !errors.Is(err, tppclient.ErrNotFound) {
		t.Errorf("FetchStoreItem(deleted) = %v, want ErrNotFound", err)
	}
	if err := desktop.DeleteStoreItem(ctx, saved.ID); !errors.Is(err, tppclient.ErrNotFound) {
		t.Errorf("DeleteStoreItem(again) = %v, want ErrNotFound", err)
	}
}
