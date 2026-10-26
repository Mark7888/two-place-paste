package tppclient_test

import (
	"crypto/sha256"
	"slices"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

func indexItem(id, name, desc string, saved int, rev uint64, body string, tags ...string) tppclient.StoreItem {
	return tppclient.StoreItem{
		ID:           id,
		SavedAt:      time.Date(2026, 10, 1, 12, 0, saved, 0, time.UTC),
		StoreDetails: tppclient.StoreDetails{Name: name, Description: desc, Tags: tags},
		SHA256:       sha256.Sum256([]byte(body)),
		MetaRev:      rev,
	}
}

func ids(items []tppclient.StoreItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// TestStoreIndexSortBySize: by plaintext size either way, with items of the
// same size newest first.
func TestStoreIndexSortBySize(t *testing.T) {
	t.Parallel()
	sized := func(id string, saved int, size int64) tppclient.StoreItem {
		it := indexItem(id, id, "", saved, 0, id)
		it.BodySize = size
		return it
	}
	x := tppclient.NewStoreIndex()
	x.Reset([]tppclient.StoreItem{sized("a", 1, 500), sized("b", 2, 10), sized("c", 3, 500), sized("d", 4, 7000)})
	tests := []struct {
		name string
		sort tppclient.StoreSort
		want []string
	}{
		{"largest first", tppclient.SortLargest, []string{"d", "c", "a", "b"}},
		{"smallest first", tppclient.SortSmallest, []string{"b", "c", "a", "d"}},
	}
	for _, tt := range tests {
		if got := ids(x.Query(tppclient.StoreQuery{Sort: tt.sort})); !slices.Equal(got, tt.want) {
			t.Errorf("%s = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestStoreIndexQuery: search is every word in the name or description,
// ignoring case; tags all have to match; and each sort is stable.
func TestStoreIndexQuery(t *testing.T) {
	t.Parallel()
	x := tppclient.NewStoreIndex()
	x.Reset([]tppclient.StoreItem{
		indexItem("a", "Home wifi", "The router in the hall", 1, 0, "w", "home", "network"),
		indexItem("b", "boiler manual", "PDF from the installer", 2, 0, "m", "home"),
		indexItem("c", "Work VPN", "Config for the office router", 3, 0, "v", "work", "network"),
		indexItem("d", "Address", "", 3, 0, "addr"),
	})

	tests := []struct {
		name string
		q    tppclient.StoreQuery
		want []string
	}{
		{"everything, newest first", tppclient.StoreQuery{}, []string{"d", "c", "b", "a"}},
		{"oldest first", tppclient.StoreQuery{Sort: tppclient.SortOldest}, []string{"a", "b", "c", "d"}},
		{"by name, ignoring case", tppclient.StoreQuery{Sort: tppclient.SortName}, []string{"d", "b", "a", "c"}},
		{"by name, descending", tppclient.StoreQuery{Sort: tppclient.SortNameDesc}, []string{"c", "a", "b", "d"}},
		{"one word in a description", tppclient.StoreQuery{Text: "ROUTER"}, []string{"c", "a"}},
		{"every word must match", tppclient.StoreQuery{Text: "router office"}, []string{"c"}},
		{"a tag", tppclient.StoreQuery{Tags: []string{"home"}}, []string{"b", "a"}},
		{"every tag must match", tppclient.StoreQuery{Tags: []string{"home", "network"}}, []string{"a"}},
		{"text and tag", tppclient.StoreQuery{Text: "router", Tags: []string{"work"}}, []string{"c"}},
		{"nothing matches", tppclient.StoreQuery{Text: "nope"}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ids(x.Query(tt.q)); !slices.Equal(got, tt.want) {
				t.Errorf("Query(%+v) = %v, want %v", tt.q, got, tt.want)
			}
		})
	}
}

// TestStoreIndexUpkeep: tags are counted, duplicates found by hash, and a
// late event never rolls an item back to an older revision.
func TestStoreIndexUpkeep(t *testing.T) {
	t.Parallel()
	x := tppclient.NewStoreIndex()
	x.Put(indexItem("a", "Note", "", 1, 0, "same", "home"))
	x.Put(indexItem("b", "Copy", "", 2, 0, "same", "home", "work"))
	x.Put(indexItem("c", "Other", "", 3, 0, "other"))

	if got, want := x.Tags(), []tppclient.TagCount{{Tag: "home", Count: 2}, {Tag: "work", Count: 1}}; !slices.Equal(got, want) {
		t.Errorf("Tags() = %v, want %v", got, want)
	}
	if got := ids(x.Duplicates(sha256.Sum256([]byte("same")))); !slices.Equal(got, []string{"b", "a"}) {
		t.Errorf("Duplicates(same) = %v, want [b a]", got)
	}

	x.Put(indexItem("a", "Renamed", "", 1, 2, "same"))
	x.Put(indexItem("a", "Late event", "", 1, 1, "same"))
	if it, _ := x.Get("a"); it.Name != "Renamed" || it.MetaRev != 2 {
		t.Errorf("Get(a) = %q rev %d, want Renamed rev 2", it.Name, it.MetaRev)
	}
	x.Remove("b")
	if x.Len() != 2 {
		t.Errorf("Len() after Remove = %d, want 2", x.Len())
	}
	if got := x.Tags(); len(got) != 0 {
		t.Errorf("Tags() after the edits = %v, want none", got)
	}
}
