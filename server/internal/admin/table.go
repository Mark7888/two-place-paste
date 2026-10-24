package admin

import (
	"cmp"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The admin UI's lists — groups, tags, tokens and the audit log — are
// searched, sorted and paged here, not in the browser, so the app never holds
// a whole list (docs/internal/plans/admin-react-rewrite/smart-table.md §5).
// Every list endpoint takes the same query parameters,
//
//	?q=words&sort=key&dir=asc|desc&page=N&size=25|50|100
//
// and answers with a tablePage: the rows of one page, plus what was applied
// after clamping and fallback. A parameter it does not understand falls back
// to its default; it is never an error.

// pageSizes are the page sizes a list offers. Anything else is the first.
var pageSizes = []int{25, 50, 100}

// maxQueryLen bounds the search text, so one request cannot make every row
// scan a megabyte of words.
const maxQueryLen = 200

// sortKey is one column a list can be sorted by.
type sortKey[T any] struct {
	// cmp orders two rows ascending.
	cmp func(a, b T) int

	// desc is the column's natural first direction: ascending for text,
	// descending for sizes, counts and dates.
	desc bool
}

// table describes one list: what it sorts by, what its search looks at, and
// how ties break.
type table[T any] struct {
	sorts       map[string]sortKey[T]
	defaultSort string

	// fields is the searchable text of a row.
	fields func(T) []string

	// tie orders rows the sort column calls equal, so that rows do not jump
	// between pages. Nil keeps the order the rows came in, which a stable
	// sort preserves.
	tie func(a, b T) int
}

// tablePage is one page of a list, and the search, sort and page actually
// applied to it.
type tablePage[T any] struct {
	Rows []T `json:"rows"`

	// Total counts the rows that match the search and filters, across all
	// pages.
	Total int    `json:"total"`
	Page  int    `json:"page"`
	Size  int    `json:"size"`
	Sort  string `json:"sort"`
	Dir   string `json:"dir"`
}

// tableQuery is a parsed list request.
type tableQuery struct {
	words []string
	sort  string
	desc  bool
	page  int
	size  int
}

// parse reads the list parameters, falling back to defaults for anything
// missing or unknown.
func (t table[T]) parse(v url.Values) tableQuery {
	q := tableQuery{sort: t.defaultSort, page: 1, size: pageSizes[0]}

	text := v.Get("q")
	if len(text) > maxQueryLen {
		text = text[:maxQueryLen]
	}
	q.words = strings.Fields(strings.ToLower(text))

	key, ok := t.sorts[v.Get("sort")]
	if ok {
		q.sort = v.Get("sort")
	} else {
		// An unknown key means the default sort in its own direction; a
		// direction given for a column that does not exist means nothing.
		key = t.sorts[t.defaultSort]
	}
	q.desc = key.desc
	if ok {
		switch v.Get("dir") {
		case "asc":
			q.desc = false
		case "desc":
			q.desc = true
		}
	}

	if n, err := strconv.Atoi(v.Get("page")); err == nil && n > 1 {
		q.page = n
	}
	if n, err := strconv.Atoi(v.Get("size")); err == nil && slices.Contains(pageSizes, n) {
		q.size = n
	}
	return q
}

// run searches, sorts and pages rows. rows is sorted in place.
func (t table[T]) run(rows []T, v url.Values) tablePage[T] {
	q := t.parse(v)

	if len(q.words) > 0 {
		rows = slices.DeleteFunc(rows, func(row T) bool { return !matches(t.fields(row), q.words) })
	}

	key := t.sorts[q.sort]
	slices.SortStableFunc(rows, func(a, b T) int {
		c := key.cmp(a, b)
		if q.desc {
			c = -c
		}
		if c == 0 && t.tie != nil {
			c = t.tie(a, b)
		}
		return c
	})

	total := len(rows)
	last := max(1, (total+q.size-1)/q.size)
	page := min(q.page, last)
	start := min((page-1)*q.size, total)
	end := min(start+q.size, total)

	dir := "asc"
	if q.desc {
		dir = "desc"
	}
	out := make([]T, end-start)
	copy(out, rows[start:end])
	return tablePage[T]{Rows: out, Total: total, Page: page, Size: q.size, Sort: q.sort, Dir: dir}
}

// matches reports whether every word appears in at least one field, ignoring
// case. Words are already lower case.
func matches(fields []string, words []string) bool {
	lower := make([]string, len(fields))
	for i, f := range fields {
		lower[i] = strings.ToLower(f)
	}
	for _, w := range words {
		if !slices.ContainsFunc(lower, func(f string) bool { return strings.Contains(f, w) }) {
			return false
		}
	}
	return true
}

// The comparisons the lists sort with.

// byText orders strings ignoring case, then exactly, so the order is total.
func byText(a, b string) int {
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// byTime orders times, a missing (nil or zero) one before any other: "never"
// is the oldest a last-seen time can be.
func byTime(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	return a.Compare(*b)
}

func byNumber[N cmp.Ordered](a, b N) int { return cmp.Compare(a, b) }

// mapPage turns a page of one row type into a page of another, for lists
// that sort on more than they show.
func mapPage[A, B any](p tablePage[A], f func(A) B) tablePage[B] {
	out := tablePage[B]{Rows: make([]B, 0, len(p.Rows)), Total: p.Total, Page: p.Page, Size: p.Size, Sort: p.Sort, Dir: p.Dir}
	for _, row := range p.Rows {
		out.Rows = append(out.Rows, f(row))
	}
	return out
}
