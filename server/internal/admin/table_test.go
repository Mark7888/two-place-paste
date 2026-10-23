package admin

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// fruit is a row for exercising the list engine on its own.
type fruit struct {
	id     string
	name   string
	colour string
	weight int
}

var fruitTable = table[fruit]{
	sorts: map[string]sortKey[fruit]{
		"name":   {cmp: func(a, b fruit) int { return byText(a.name, b.name) }},
		"weight": {cmp: func(a, b fruit) int { return byNumber(a.weight, b.weight) }, desc: true},
	},
	defaultSort: "weight",
	fields:      func(f fruit) []string { return []string{f.name, f.colour} },
	tie: func(a, b fruit) int {
		if c := byText(a.name, b.name); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	},
}

func fruits() []fruit {
	return []fruit{
		{"1", "Apple", "red", 150},
		{"2", "banana", "yellow", 120},
		{"3", "Cherry", "red", 5},
		{"4", "apple", "green", 150},
		{"5", "Date", "brown", 7},
	}
}

func ids(rows []fruit) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.id)
	}
	return strings.Join(out, ",")
}

func TestTableRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
		ids   string
		total int
		page  int
		size  int
		sort  string
		dir   string
	}{
		{name: "defaults: heaviest first, ties by name then id", query: "", ids: "1,4,2,5,3", total: 5, page: 1, size: 25, sort: "weight", dir: "desc"},
		{name: "ascending", query: "sort=weight&dir=asc", ids: "3,5,2,1,4", total: 5, page: 1, size: 25, sort: "weight", dir: "asc"},
		{name: "text in its natural direction", query: "sort=name", ids: "1,4,2,3,5", total: 5, page: 1, size: 25, sort: "name", dir: "asc"},
		{name: "text descending", query: "sort=name&dir=desc", ids: "5,3,2,4,1", total: 5, page: 1, size: 25, sort: "name", dir: "desc"},
		{name: "one word, any case, any field", query: "q=RED", ids: "1,3", total: 2, page: 1, size: 25, sort: "weight", dir: "desc"},
		{name: "every word must match", query: "q=apple+green", ids: "4", total: 1, page: 1, size: 25, sort: "weight", dir: "desc"},
		{name: "words match in different fields", query: "q=re+app", ids: "1,4", total: 2, page: 1, size: 25, sort: "weight", dir: "desc"},
		{name: "no match", query: "q=kiwi", ids: "", total: 0, page: 1, size: 25, sort: "weight", dir: "desc"},
		{name: "unknown sort falls back with its own direction", query: "sort=colour&dir=asc", ids: "1,4,2,5,3", total: 5, page: 1, size: 25, sort: "weight", dir: "desc"},
		{name: "unknown direction keeps the natural one", query: "sort=name&dir=up", ids: "1,4,2,3,5", total: 5, page: 1, size: 25, sort: "name", dir: "asc"},
		{name: "unknown size is the default", query: "size=7", ids: "1,4,2,5,3", total: 5, page: 1, size: 25, sort: "weight", dir: "desc"},
		{name: "page past the end is the last page", query: "page=9", ids: "1,4,2,5,3", total: 5, page: 1, size: 25, sort: "weight", dir: "desc"},
		{name: "page below one is the first", query: "page=-3", ids: "1,4,2,5,3", total: 5, page: 1, size: 25, sort: "weight", dir: "desc"},
		{name: "unknown parameters are ignored", query: "colour=red&x=1", ids: "1,4,2,5,3", total: 5, page: 1, size: 25, sort: "weight", dir: "desc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatalf("parse %q: %v", tt.query, err)
			}
			got := fruitTable.run(fruits(), v)
			if ids(got.Rows) != tt.ids || got.Total != tt.total || got.Page != tt.page || got.Size != tt.size || got.Sort != tt.sort || got.Dir != tt.dir {
				t.Errorf("run(%q) = rows %s, total %d, page %d, size %d, %s %s; want rows %s, total %d, page %d, size %d, %s %s",
					tt.query, ids(got.Rows), got.Total, got.Page, got.Size, got.Sort, got.Dir,
					tt.ids, tt.total, tt.page, tt.size, tt.sort, tt.dir)
			}
			if got.Rows == nil {
				t.Error("rows = nil, want an empty list so the JSON says []")
			}
		})
	}
}

// TestTablePaging walks every page of a long list: each row appears exactly
// once, and a page past the end clamps to the last.
func TestTablePaging(t *testing.T) {
	t.Parallel()

	rows := make([]fruit, 0, 112)
	for i := range 112 {
		// Many equal weights, so the order rests on the tie-break.
		rows = append(rows, fruit{id: fmt.Sprintf("%03d", i), name: fmt.Sprintf("fruit %d", i%7), weight: i % 3})
	}
	seen := make(map[string]int)
	for page := 1; page <= 5; page++ {
		got := fruitTable.run(slices.Clone(rows), url.Values{"page": {fmt.Sprint(page)}})
		wantLen := 25
		if page == 5 {
			wantLen = 12
		}
		if len(got.Rows) != wantLen || got.Total != 112 || got.Page != page {
			t.Fatalf("page %d = %d rows of %d (page %d), want %d of 112", page, len(got.Rows), got.Total, got.Page, wantLen)
		}
		for _, r := range got.Rows {
			seen[r.id]++
		}
	}
	if len(seen) != 112 {
		t.Errorf("pages hold %d distinct rows, want all 112", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("row %s appears on %d pages, want 1", id, n)
		}
	}

	got := fruitTable.run(slices.Clone(rows), url.Values{"page": {"6"}, "size": {"50"}})
	if got.Page != 3 || got.Size != 50 || len(got.Rows) != 12 {
		t.Errorf("page 6 of 50 = page %d, size %d, %d rows; want the last page, 3, with 12", got.Page, got.Size, len(got.Rows))
	}
	if got := fruitTable.run(nil, nil); got.Page != 1 || got.Total != 0 || len(got.Rows) != 0 {
		t.Errorf("empty list = %+v, want page 1 of nothing", got)
	}
}

func TestHandles(t *testing.T) {
	t.Parallel()

	a, b := handleOf("token-a"), handleOf("token-b")
	if len(a) != handleLen || a == b || handleOf("token-a") != a {
		t.Errorf("handles %q, %q: want %d characters, distinct, and stable", a, b, handleLen)
	}
	if strings.Contains(a, "token") {
		t.Errorf("handle %q shows the value", a)
	}
}
