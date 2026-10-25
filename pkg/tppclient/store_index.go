package tppclient

import (
	"cmp"
	"crypto/sha256"
	"slices"
	"strings"
	"sync"
)

// StoreIndex is a group's store as one device sees it: every item's opened
// metadata, kept in memory for the Store tab to search, filter by tag, sort
// and check for duplicates. Searching is all here, on the device: the relay
// cannot search what it cannot read (SPEC §6b).
//
// A UI fills it from ListStore and keeps it current with the store handlers
// and the results of its own saves, edits and deletes. It is never persisted.
// It is safe for concurrent use.
type StoreIndex struct {
	mu    sync.Mutex
	items map[string]StoreItem
}

// StoreSort orders a query's results.
type StoreSort int

const (
	// SortNewest is the default: the most recently saved first.
	SortNewest StoreSort = iota
	// SortOldest is the least recently saved first.
	SortOldest
	// SortName is by name, A to Z, ignoring case.
	SortName
	// SortNameDesc is by name, Z to A.
	SortNameDesc
)

// StoreQuery selects and orders items.
type StoreQuery struct {
	// Text matches items whose name or description contains every word of
	// it, ignoring case. Empty matches every item.
	Text string

	// Tags matches items carrying every one of them. Empty matches every item.
	Tags []string

	Sort StoreSort
}

// TagCount is a tag and how many items carry it.
type TagCount struct {
	Tag   string
	Count int
}

// NewStoreIndex returns an empty index.
func NewStoreIndex() *StoreIndex {
	return &StoreIndex{items: make(map[string]StoreItem)}
}

// Reset replaces the whole index with a listing.
func (x *StoreIndex) Reset(items []StoreItem) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.items = make(map[string]StoreItem, len(items))
	for _, it := range items {
		x.items[it.ID] = it
	}
}

// Put adds an item, or replaces it with a newer revision. An older revision
// than the one held — an event that arrived after a later edit — is ignored.
func (x *StoreIndex) Put(it StoreItem) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if old, ok := x.items[it.ID]; ok && old.MetaRev > it.MetaRev {
		return
	}
	x.items[it.ID] = it
}

// Remove drops an item.
func (x *StoreIndex) Remove(itemID string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.items, itemID)
}

// Get returns one item.
func (x *StoreIndex) Get(itemID string) (StoreItem, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	it, ok := x.items[itemID]
	return it, ok
}

// Len is how many items the index holds.
func (x *StoreIndex) Len() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return len(x.items)
}

// Query returns the items that match, in the order asked for.
func (x *StoreIndex) Query(q StoreQuery) []StoreItem {
	words := strings.Fields(strings.ToLower(q.Text))
	x.mu.Lock()
	out := make([]StoreItem, 0, len(x.items))
	for _, it := range x.items {
		if matchesText(it, words) && hasTags(it, q.Tags) {
			out = append(out, it)
		}
	}
	x.mu.Unlock()

	newest := func(a, b StoreItem) int {
		if c := b.SavedAt.Compare(a.SavedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	}
	byName := func(a, b StoreItem) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return newest(a, b)
	}
	switch q.Sort {
	case SortOldest:
		slices.SortFunc(out, func(a, b StoreItem) int { return newest(b, a) })
	case SortName:
		slices.SortFunc(out, byName)
	case SortNameDesc:
		slices.SortFunc(out, func(a, b StoreItem) int { return byName(b, a) })
	default:
		slices.SortFunc(out, newest)
	}
	return out
}

func matchesText(it StoreItem, words []string) bool {
	if len(words) == 0 {
		return true
	}
	hay := strings.ToLower(it.Name + "\n" + it.Description)
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

func hasTags(it StoreItem, tags []string) bool {
	for _, t := range tags {
		if !slices.Contains(it.Tags, t) {
			return false
		}
	}
	return true
}

// Tags lists every tag in use with how many items carry it, by name: the tag
// chips of the Store tab, and the choices the save dialog offers.
func (x *StoreIndex) Tags() []TagCount {
	x.mu.Lock()
	counts := make(map[string]int)
	for _, it := range x.items {
		for _, t := range it.Tags {
			counts[t]++
		}
	}
	x.mu.Unlock()
	out := make([]TagCount, 0, len(counts))
	for t, n := range counts {
		out = append(out, TagCount{Tag: t, Count: n})
	}
	slices.SortFunc(out, func(a, b TagCount) int { return cmp.Compare(a.Tag, b.Tag) })
	return out
}

// Duplicates returns the items whose body has this hash, newest first: what
// the save dialog warns about, and what marks a history entry as saved.
func (x *StoreIndex) Duplicates(sum [sha256.Size]byte) []StoreItem {
	x.mu.Lock()
	var out []StoreItem
	for _, it := range x.items {
		if it.SHA256 == sum {
			out = append(out, it)
		}
	}
	x.mu.Unlock()
	slices.SortFunc(out, func(a, b StoreItem) int {
		if c := b.SavedAt.Compare(a.SavedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	return out
}
