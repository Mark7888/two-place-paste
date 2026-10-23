package admin_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/admin"
	"github.com/Mark7888/two-place-paste/server/internal/quota"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// The four list endpoints share one engine (table.go, covered by
// table_test.go); these tests pin what each list searches, sorts and filters
// by, end to end.

func names[T any](rows []T, name func(T) string) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, name(r))
	}
	return strings.Join(out, ",")
}

type groupRowName = struct {
	Handle string `json:"handle"`
	Name   string `json:"name"`
	Stale  bool   `json:"stale"`
	Usage  *struct {
		UsedBytes int64 `json:"used_bytes"`
		OverLimit bool  `json:"over_limit"`
	} `json:"usage"`
	LimitBytes int64 `json:"limit_bytes"`
}

func groupNames(l groupListJSON) string {
	return names(l.Groups, func(g groupRowName) string { return g.Name })
}

func TestGroupList(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	anna, _ := g.group(t, "Anna")
	ben, _ := g.group(t, "Ben")
	g.group(t, "Carla")
	g.put(t, anna, "a-blob", 2048)
	g.put(t, ben, "b-blob", 4096)
	c := g.client()
	csrf := g.login(c)
	wantStatus(t, g.call(c, csrf, http.MethodPost, "/admin/api/tags", map[string]string{"name": "family"}), http.StatusCreated)
	wantStatus(t, g.call(c, csrf, http.MethodPut, "/admin/api/groups/"+admin.HandleOf(anna)+"/tag", map[string]string{"tag": "family"}), http.StatusNoContent)

	tests := []struct {
		query string
		want  string
		sort  string
		dir   string
	}{
		{query: "", want: "Ben,Anna,Carla", sort: "storage", dir: "desc"},
		{query: "sort=storage&dir=asc", want: "Carla,Anna,Ben", sort: "storage", dir: "asc"},
		{query: "sort=name", want: "Anna,Ben,Carla", sort: "name", dir: "asc"},
		{query: "sort=name&dir=desc", want: "Carla,Ben,Anna", sort: "name", dir: "desc"},
		{query: "sort=tag&dir=desc", want: "Anna,Ben,Carla", sort: "tag", dir: "desc"},
		{query: "sort=devices", want: "Anna,Ben,Carla", sort: "devices", dir: "desc"},
		{query: "sort=entries", want: "Anna,Ben,Carla", sort: "entries", dir: "desc"},
		{query: "sort=last_active", want: "Anna,Ben,Carla", sort: "last_active", dir: "desc"},
		{query: "sort=oldest&dir=asc", want: "Carla,Anna,Ben", sort: "oldest", dir: "asc"},
		{query: "sort=bogus&dir=asc", want: "Ben,Anna,Carla", sort: "storage", dir: "desc"},
		// Search: name, tag, short id and device name, every word.
		{query: "q=ann", want: "Anna", sort: "storage", dir: "desc"},
		{query: "q=FAMILY", want: "Anna", sort: "storage", dir: "desc"},
		{query: "q=" + url.QueryEscape("carla's laptop"), want: "Carla", sort: "storage", dir: "desc"},
		{query: "q=laptop+ben", want: "Ben", sort: "storage", dir: "desc"},
		{query: "q=" + url.QueryEscape(shortOf(ben)), want: "Ben", sort: "storage", dir: "desc"},
		{query: "q=nobody", want: "", sort: "storage", dir: "desc"},
		// Filters, alone and with a search.
		{query: "tag=family", want: "Anna", sort: "storage", dir: "desc"},
		{query: "tag=-&sort=name", want: "Ben,Carla", sort: "name", dir: "asc"},
		{query: "tag=-&q=carla", want: "Carla", sort: "storage", dir: "desc"},
	}
	for _, tt := range tests {
		resp := g.get(c, "/admin/api/groups?"+tt.query)
		wantStatus(t, resp, http.StatusOK)
		got := readJSON[groupListJSON](t, resp)
		if groupNames(got) != tt.want || got.Sort != tt.sort || got.Dir != tt.dir {
			t.Errorf("?%s = %s (%s %s), want %s (%s %s)", tt.query, groupNames(got), got.Sort, got.Dir, tt.want, tt.sort, tt.dir)
		}
		if got.Total != len(got.Groups) {
			t.Errorf("?%s total = %d, want %d", tt.query, got.Total, len(got.Groups))
		}
		// The filter row counts every group whatever the search.
		if len(got.Tags) != 2 || got.Tags[0].Groups != 1 || got.Tags[1].Groups != 2 || got.GroupCount != 3 {
			t.Errorf("?%s tag row = %+v of %d, want family 1 and untagged 2 of 3", tt.query, got.Tags, got.GroupCount)
		}
	}

	// No row carries a full group id, which is also a creation token.
	body := readBody(t, g.get(c, "/admin/api/groups"))
	for _, id := range []string{anna, ben} {
		if strings.Contains(body, id) {
			t.Errorf("the group list holds a full group id")
		}
	}
}

func shortOf(id string) string { return id[:8] }

// TestGroupListPaging: a page past the end is clamped, and the size is one of
// the offered ones.
func TestGroupListPaging(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	for i := range 30 {
		g.group(t, "G"+strings.Repeat("x", i))
	}
	c := g.client()
	g.login(c)

	got := readJSON[groupListJSON](t, g.get(c, "/admin/api/groups?page=2"))
	if len(got.Groups) != 5 || got.Total != 30 || got.Page != 2 || got.Size != 25 {
		t.Errorf("page 2 = %d rows of %d (page %d, size %d), want 5 of 30", len(got.Groups), got.Total, got.Page, got.Size)
	}
	got = readJSON[groupListJSON](t, g.get(c, "/admin/api/groups?page=7&size=50"))
	if len(got.Groups) != 30 || got.Page != 1 || got.Size != 50 {
		t.Errorf("page 7 of 50 = %d rows (page %d, size %d), want all 30 on page 1", len(got.Groups), got.Page, got.Size)
	}
	got = readJSON[groupListJSON](t, g.get(c, "/admin/api/groups?size=13"))
	if got.Size != 25 {
		t.Errorf("size 13 = %d, want the default 25", got.Size)
	}
}

func TestGroupListStaleFilter(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	g.group(t, "Old")
	g.clk.advance(time.Since(g.clk.now()) + 40*24*time.Hour)
	c := g.client()
	csrf := g.login(c)
	wantStatus(t, g.call(c, csrf, http.MethodPut, "/admin/api/settings", map[string]int{"stale_days": 30}), http.StatusOK)
	all := readJSON[groupListJSON](t, g.get(c, "/admin/api/groups"))
	stale := readJSON[groupListJSON](t, g.get(c, "/admin/api/groups?stale=1"))
	if stale.Total != 1 || groupNames(stale) != "Old" || !stale.Stale || all.Stale {
		t.Errorf("stale filter = %s (stale %v), unfiltered stale %v; want only Old", groupNames(stale), stale.Stale, all.Stale)
	}
	// The filters combine: stale and under a tag it does not have is nothing.
	wantStatus(t, g.call(c, csrf, http.MethodPost, "/admin/api/tags", map[string]string{"name": "family"}), http.StatusCreated)
	if got := readJSON[groupListJSON](t, g.get(c, "/admin/api/groups?stale=1&tag=family")); got.Total != 0 {
		t.Errorf("stale under family = %s, want none", groupNames(got))
	}
}

func TestTagList(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	anna, _ := g.group(t, "Anna")
	g.put(t, anna, "a-blob", 4096)
	c := g.client()
	csrf := g.login(c)
	for _, name := range []string{"family", "friends", "work"} {
		wantStatus(t, g.call(c, csrf, http.MethodPost, "/admin/api/tags", map[string]string{"name": name}), http.StatusCreated)
	}
	wantStatus(t, g.call(c, csrf, http.MethodPut, "/admin/api/groups/"+admin.HandleOf(anna)+"/tag", map[string]string{"tag": "work"}), http.StatusNoContent)
	wantStatus(t, g.call(c, csrf, http.MethodPut, "/admin/api/tags/friends/limit", map[string]any{"enabled": true, "size": "1 GiB"}), http.StatusNoContent)
	wantStatus(t, g.call(c, csrf, http.MethodPut, "/admin/api/tags/family/limit", map[string]any{"enabled": true, "size": "0"}), http.StatusNoContent)

	type tagRow struct {
		Name string `json:"name"`
	}
	type tagPage struct {
		Rows  []tagRow `json:"rows"`
		Total int      `json:"total"`
		Sort  string   `json:"sort"`
		Dir   string   `json:"dir"`
	}
	tests := []struct{ query, want string }{
		{"", "family,friends,work"},
		{"sort=name&dir=desc", "work,friends,family"},
		{"sort=groups", "work,family,friends"},
		{"sort=devices", "work,family,friends"},
		{"sort=storage", "work,family,friends"},
		// No per-group limit at all, then the largest size, then the default.
		{"sort=limit", "family,friends,work"},
		{"sort=limit&dir=asc", "work,friends,family"},
		{"q=fr", "friends"},
		{"q=f+ly", "family"},
	}
	for _, tt := range tests {
		got := readJSON[tagPage](t, g.get(c, "/admin/api/tags?"+tt.query))
		if n := names(got.Rows, func(r tagRow) string { return r.Name }); n != tt.want {
			t.Errorf("?%s = %s, want %s", tt.query, n, tt.want)
		}
	}
}

func TestTokenList(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	c := g.client()
	csrf := g.login(c)
	ctx := context.Background()

	mint := func(name, note, expires string) {
		t.Helper()
		wantStatus(t, g.call(c, csrf, http.MethodPost, "/admin/api/tokens", map[string]string{"name": name, "note": note, "expires": expires}), http.StatusCreated)
		g.clk.advance(time.Minute)
	}
	mint("Anna", "new laptop", "never")
	mint("Ben", "", "1")
	mint("Carla", "phone", "30")
	mint("Dora", "", "never")
	// Ben's day runs out; Dora's token creates a group.
	g.clk.advance(25 * time.Hour)
	c = g.client()
	csrf = g.login(c)
	tokens, _ := g.store.ListTokens(ctx)
	for _, tok := range tokens {
		if tok.Name == "Dora" {
			if _, err := g.store.CreateGroup(ctx, tok.Value, store.NewDevice{PublicKey: []byte("pk"), WrappedGroupKey: []byte("wk")}); err != nil {
				t.Fatalf("CreateGroup() error = %v", err)
			}
		}
	}

	tests := []struct{ query, want string }{
		{"", "Dora,Carla,Ben,Anna"},
		{"sort=created&dir=asc", "Anna,Ben,Carla,Dora"},
		{"sort=name", "Anna,Ben,Carla,Dora"},
		{"sort=status", "Anna,Carla,Ben,Dora"},
		{"sort=expires", "Carla,Ben,Anna,Dora"},
		{"q=laptop", "Anna"},
		{"q=expired", "Ben"},
		{"q=USED", "Dora,Carla,Anna"},
		{"status=unused", "Carla,Anna"},
		{"status=expired", "Ben"},
		{"status=used", "Dora"},
		{"status=used&q=dora", "Dora"},
	}
	for _, tt := range tests {
		got := readJSON[tokenList](t, g.get(c, "/admin/api/tokens?"+tt.query))
		if n := names(got.Tokens, func(r tokenRow) string { return r.Name }); n != tt.want {
			t.Errorf("?%s = %s, want %s", tt.query, n, tt.want)
		}
	}
}

func TestAuditList(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	c := g.client()
	g.tryLogin(c, "wrong")
	g.clk.advance(time.Minute)
	csrf := g.login(c)
	g.clk.advance(time.Minute)
	wantStatus(t, g.call(c, csrf, http.MethodPost, "/admin/api/tags", map[string]string{"name": "family"}), http.StatusCreated)

	type auditRow struct {
		Action string `json:"action"`
		Label  string `json:"label"`
	}
	type auditPage struct {
		Rows    []auditRow `json:"rows"`
		Kept    int        `json:"kept"`
		Actions []struct {
			Action string `json:"action"`
			Label  string `json:"label"`
		} `json:"actions"`
	}
	tests := []struct{ query, want string }{
		{"", "tag.create,login,login.failed"},
		{"sort=when&dir=asc", "login.failed,login,tag.create"},
		{"sort=action", "login.failed,login,tag.create"},
		{"q=family", "tag.create"},
		// The search matches the words on screen as well as the stored action.
		{"q=" + url.QueryEscape("sign-in failed"), "login.failed"},
		{"q=signed", "login"},
		{"action=login", "login"},
		{"action=nope", ""},
	}
	for _, tt := range tests {
		got := readJSON[auditPage](t, g.get(c, "/admin/api/audit?"+tt.query))
		if n := names(got.Rows, func(r auditRow) string { return r.Action }); n != tt.want {
			t.Errorf("?%s = %s, want %s", tt.query, n, tt.want)
		}
		if got.Kept != store.AuditLimit || len(got.Actions) == 0 {
			t.Errorf("?%s kept %d, %d actions; want %d and the action list", tt.query, got.Kept, len(got.Actions), store.AuditLimit)
		}
	}
}
