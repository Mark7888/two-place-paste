package admin_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Mark7888/two-place-paste/server/internal/admin"
	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/entries"
	"github.com/Mark7888/two-place-paste/server/internal/quota"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// fakeLive stands in for the WebSocket transport and records what the admin
// screens asked of it.
type fakeLive struct {
	mu      sync.Mutex
	online  map[string]bool
	renamed map[string]string
	closed  []string
}

func newFakeLive() *fakeLive {
	return &fakeLive{online: make(map[string]bool), renamed: make(map[string]string)}
}

func (l *fakeLive) OnlineDevices(string) map[string]bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]bool, len(l.online))
	for k, v := range l.online {
		out[k] = v
	}
	return out
}

func (l *fakeLive) AnnounceRename(_ context.Context, groupID, name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.renamed[groupID] = name
}

func (l *fakeLive) CloseGroup(groupID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = append(l.closed, groupID)
}

// fakeRedis answers the health card's two reads.
type fakeRedis struct {
	config map[string]string
	err    error
}

func (r fakeRedis) ConfigGet(_ context.Context, p string) *redis.MapStringStringCmd {
	if r.err != nil {
		return redis.NewMapStringStringResult(nil, r.err)
	}
	return redis.NewMapStringStringResult(map[string]string{p: r.config[p]}, nil)
}

func (r fakeRedis) Info(context.Context, ...string) *redis.StringCmd {
	return redis.NewStringResult("# Memory\r\nused_memory_human:1.50M\r\nmaxmemory_human:0B\r\n", r.err)
}

// groupFixture is an admin server with everything wired: a real entry
// service, ledger and blob store, and a fake transport.
type groupFixture struct {
	*fixture
	svc    *entries.Service
	ledger *quota.Memory
	live   *fakeLive
	sweeps int
	mu     sync.Mutex
}

func newGroupFixture(t *testing.T, limits quota.Limits, rds admin.RedisInfo) *groupFixture {
	t.Helper()
	g := &groupFixture{ledger: quota.NewMemory(quota.Options{Limits: limits}), live: newFakeLive()}
	ephemeral := entries.NewMemoryStore()
	g.svc = entries.NewService(ephemeral, blob.NewDisk(t.TempDir(), 1<<20), entries.Options{
		InlineMaxBytes: 1024,
		MaxBytes:       1 << 20,
		Quota:          g.ledger,
		Logger:         slog.New(slog.DiscardHandler),
	})
	g.fixture = newFixtureWithOptions(t, "https://tpp.example.com", func(o *admin.Options) {
		o.Dashboard = admin.Dashboard{
			Ledger:     g.ledger,
			Entries:    ephemeral,
			TotalLimit: limits.TotalBytes,
			GroupLimit: limits.GroupBytes,
		}
		o.Live = g.live
		o.Purger = g.svc
		o.Limits = g.ledger
		o.Sweep = func() bool {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.sweeps++
			return true
		}
		o.Redis = rds
		o.Version = "v9.9.9 (abc123)"
	})
	return g
}

func (g *groupFixture) group(t *testing.T, name string) (groupID, deviceID string) {
	t.Helper()
	ctx := context.Background()
	tok, err := g.store.CreateToken(ctx, name)
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	created, err := g.store.CreateGroup(ctx, tok.Value, store.NewDevice{Name: name + "'s laptop", PublicKey: []byte("pk"), WrappedGroupKey: []byte("wk")})
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	return tok.Value, created.Device.ID
}

func (g *groupFixture) put(t *testing.T, groupID, entryID string, size int) {
	t.Helper()
	if _, err := g.svc.Put(context.Background(), groupID, 1, entryID, int64(size), strings.NewReader(strings.Repeat("c", size))); err != nil {
		t.Fatalf("Put(%s) error = %v", entryID, err)
	}
}

func (g *groupFixture) ledgerLimit(t *testing.T, groupID string) (int64, bool) {
	t.Helper()
	limit, own, err := g.ledger.GroupLimit(context.Background(), groupID)
	if err != nil {
		t.Fatalf("GroupLimit() error = %v", err)
	}
	return limit, own
}

// groupPage is the group endpoint's JSON, as a test reads it.
type groupPage struct {
	Group struct {
		Name    string `json:"name"`
		Tag     string `json:"tag"`
		Devices int    `json:"devices"`
		Stale   bool   `json:"stale"`
		Entries int64  `json:"entries"`
		Usage   *struct {
			UsedBytes int64 `json:"used_bytes"`
		} `json:"usage"`
		LimitBytes  int64  `json:"limit_bytes"`
		LimitSource string `json:"limit_source"`
	} `json:"group"`
	Devices []struct {
		Name   string `json:"name"`
		Online bool   `json:"online"`
	} `json:"devices"`
	OwnLimit struct {
		Enabled bool  `json:"enabled"`
		Bytes   int64 `json:"bytes"`
	} `json:"own_limit"`
	TagLimitBytes *int64   `json:"tag_limit_bytes"`
	Tags          []string `json:"tags"`
	CanPurge      bool     `json:"can_purge"`
}

func TestGroupPageAndRename(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{GroupBytes: 1 << 20}, nil)
	id, device := g.group(t, "Anna")
	g.live.online[device] = true
	c := g.client()
	csrf := g.login(c)

	page := readJSON[groupPage](t, g.get(c, "/admin/api/groups/"+id))
	if page.Group.Name != "Anna" || len(page.Devices) != 1 || page.Devices[0].Name != "Anna's laptop" || !page.Devices[0].Online {
		t.Errorf("group page = %+v, want Anna with her laptop online", page)
	}
	if !page.CanPurge || page.Group.LimitBytes != 1<<20 || page.Group.LimitSource != "default" {
		t.Errorf("group page = %+v, want purge available and the 1 MiB default limit", page)
	}

	wantStatus(t, g.call(c, csrf, http.MethodPut, "/admin/api/groups/"+id+"/name", map[string]string{"name": "  Family "}), http.StatusNoContent)
	got, err := g.store.GetGroup(context.Background(), id)
	if err != nil || got.Name != "Family" {
		t.Errorf("group name = %q, %v; want Family", got.Name, err)
	}
	if g.live.renamed[id] != "Family" {
		t.Errorf("devices were told %q, want Family", g.live.renamed[id])
	}

	for name, newName := range map[string]string{
		"empty name": "   ",
		"too long":   strings.Repeat("x", 121),
		"control":    "a\x07b",
	} {
		resp := g.call(c, csrf, http.MethodPut, "/admin/api/groups/"+id+"/name", map[string]string{"name": newName})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", name, resp.StatusCode, http.StatusBadRequest)
		}
	}
	wantStatus(t, g.call(c, "", http.MethodPut, "/admin/api/groups/"+id+"/name", map[string]string{"name": "x"}), http.StatusForbidden)

	resp := g.get(c, "/admin/api/groups/nope")
	wantStatus(t, resp, http.StatusNotFound)
	if msg := apiError(t, resp); msg != "That group does not exist. It may have been deleted." {
		t.Errorf("unknown group error = %q", msg)
	}
	wantStatus(t, g.call(c, csrf, http.MethodPut, "/admin/api/groups/nope/name", map[string]string{"name": "x"}), http.StatusNotFound)
}

// TestStorageLimitPrecedence walks default < tag < group through the admin
// API and checks what the ledger enforces at each step
// (docs/internal/plans/admin-group-management.md D3).
func TestStorageLimitPrecedence(t *testing.T) {
	t.Parallel()
	const def, total = 1 << 20, 100 << 30
	g := newGroupFixture(t, quota.Limits{GroupBytes: def, TotalBytes: total}, nil)
	id, _ := g.group(t, "Anna")
	c := g.client()
	csrf := g.login(c)
	ctx := context.Background()

	type limit struct {
		Enabled bool   `json:"enabled"`
		Size    string `json:"size"`
	}
	steps := []struct {
		name     string
		method   string
		path     string
		body     any
		want     int64
		wantOwn  bool
		wantCode int
	}{
		{name: "create tag", method: http.MethodPost, path: "/admin/api/tags", body: map[string]string{"name": "family"}, want: def, wantCode: http.StatusCreated},
		{name: "tag limit on", method: http.MethodPut, path: "/admin/api/tags/family/limit", body: limit{true, "5 GiB"}, want: def},
		{name: "tag the group", method: http.MethodPut, path: "/admin/api/groups/" + id + "/tag", body: map[string]string{"tag": "family"}, want: 5 << 30, wantOwn: true},
		{name: "group limit on", method: http.MethodPut, path: "/admin/api/groups/" + id + "/limit", body: limit{true, "2 GiB"}, want: 2 << 30, wantOwn: true},
		{name: "group limit off keeps tag", method: http.MethodPut, path: "/admin/api/groups/" + id + "/limit", body: limit{false, ""}, want: 5 << 30, wantOwn: true},
		{name: "tag limit off", method: http.MethodPut, path: "/admin/api/tags/family/limit", body: limit{false, ""}, want: def},
		{name: "group limit zero", method: http.MethodPut, path: "/admin/api/groups/" + id + "/limit", body: limit{true, "0"}, want: 0, wantOwn: true},
		{name: "above total refused", method: http.MethodPut, path: "/admin/api/groups/" + id + "/limit", body: limit{true, "200 GiB"}, want: 0, wantOwn: true, wantCode: http.StatusBadRequest},
		{name: "not a size refused", method: http.MethodPut, path: "/admin/api/groups/" + id + "/limit", body: limit{true, "lots"}, want: 0, wantOwn: true, wantCode: http.StatusBadRequest},
		{name: "group limit off", method: http.MethodPut, path: "/admin/api/groups/" + id + "/limit", body: limit{}, want: def},
	}
	for _, st := range steps {
		resp := g.call(c, csrf, st.method, st.path, st.body)
		wantCode := st.wantCode
		if wantCode == 0 {
			wantCode = http.StatusNoContent
		}
		if resp.StatusCode != wantCode {
			t.Fatalf("%s: status = %d, want %d", st.name, resp.StatusCode, wantCode)
		}
		got, own := g.ledgerLimit(t, id)
		if got != st.want || own != st.wantOwn {
			t.Errorf("%s: ledger limit = %d (own %v), want %d (own %v)", st.name, got, own, st.want, st.wantOwn)
		}
	}

	// The bytes survive unticking: the group's stored override still holds
	// the last value saved while it was on.
	grp, _ := g.store.GetGroup(ctx, id)
	if grp.Limit.Enabled || grp.Limit.Bytes != 0 {
		t.Errorf("group override = %+v, want off with 0 kept", grp.Limit)
	}

	// Deleting the tag untags the group; with no override left, the default
	// applies again.
	wantStatus(t, g.call(c, csrf, http.MethodPut, "/admin/api/tags/family/limit", limit{true, "3 GiB"}), http.StatusNoContent)
	if got, _ := g.ledgerLimit(t, id); got != 3<<30 {
		t.Fatalf("ledger limit = %d, want the tag's 3 GiB", got)
	}
	page := readJSON[groupPage](t, g.get(c, "/admin/api/groups/"+id))
	if page.TagLimitBytes == nil || *page.TagLimitBytes != 3<<30 || page.Group.LimitSource != "tag" {
		t.Errorf("group page = tag limit %v, source %q; want 3 GiB from the tag", page.TagLimitBytes, page.Group.LimitSource)
	}
	wantStatus(t, g.call(c, csrf, http.MethodDelete, "/admin/api/tags/family", nil), http.StatusNoContent)
	if got, own := g.ledgerLimit(t, id); got != def || own {
		t.Errorf("ledger limit after tag delete = %d (own %v), want the default", got, own)
	}
	if grp, _ := g.store.GetGroup(ctx, id); grp.Tag != "" {
		t.Errorf("group tag after tag delete = %q, want none", grp.Tag)
	}
	wantStatus(t, g.call(c, csrf, http.MethodPut, "/admin/api/groups/"+id+"/tag", map[string]string{"tag": "family"}), http.StatusBadRequest)
	wantStatus(t, g.call(c, csrf, http.MethodPut, "/admin/api/tags/family/limit", limit{true, "1 GiB"}), http.StatusNotFound)
	wantStatus(t, g.call(c, csrf, http.MethodDelete, "/admin/api/tags/family", nil), http.StatusNotFound)
}

func TestTagNamesAreValidated(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	c := g.client()
	csrf := g.login(c)

	for _, name := range []string{"", "-", " friends", "a/b", strings.Repeat("x", 41)} {
		resp := g.call(c, csrf, http.MethodPost, "/admin/api/tags", map[string]string{"name": name})
		if name == " friends" {
			// Surrounding space is trimmed, not refused.
			wantStatus(t, resp, http.StatusCreated)
			if got := readJSON[map[string]string](t, resp)["name"]; got != "friends" {
				t.Errorf("created tag = %q, want friends", got)
			}
			continue
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("tag %q: status = %d, want %d", name, resp.StatusCode, http.StatusBadRequest)
		}
	}
	resp := g.call(c, csrf, http.MethodPost, "/admin/api/tags", map[string]string{"name": "friends"})
	wantStatus(t, resp, http.StatusConflict)
	if msg := apiError(t, resp); msg != "There is already a tag called “friends”." {
		t.Errorf("duplicate tag error = %q", msg)
	}
}

func TestTagScreen(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{GroupBytes: 1 << 20, TotalBytes: 1 << 30}, nil)
	anna, _ := g.group(t, "Anna")
	g.group(t, "Ben")
	g.put(t, anna, "a-blob", 4096)
	c := g.client()
	csrf := g.login(c)
	wantStatus(t, g.call(c, csrf, http.MethodPost, "/admin/api/tags", map[string]string{"name": "family"}), http.StatusCreated)
	wantStatus(t, g.call(c, csrf, http.MethodPost, "/admin/api/tags", map[string]string{"name": "empty"}), http.StatusCreated)
	wantStatus(t, g.call(c, csrf, http.MethodPut, "/admin/api/groups/"+anna+"/tag", map[string]string{"tag": "family"}), http.StatusNoContent)

	type tagRow struct {
		Name      string `json:"name"`
		Groups    int    `json:"groups"`
		Devices   int    `json:"devices"`
		UsedBytes int64  `json:"used_bytes"`
	}
	got := readJSON[struct {
		Tags              []tagRow `json:"tags"`
		DefaultLimitBytes int64    `json:"default_limit_bytes"`
		TotalLimitBytes   int64    `json:"total_limit_bytes"`
	}](t, g.get(c, "/admin/api/tags"))
	want := []tagRow{{Name: "empty"}, {Name: "family", Groups: 1, Devices: 1, UsedBytes: 4096}}
	if len(got.Tags) != len(want) {
		t.Fatalf("tags = %+v, want %+v", got.Tags, want)
	}
	for i := range want {
		if got.Tags[i] != want[i] {
			t.Errorf("tag %d = %+v, want %+v", i, got.Tags[i], want[i])
		}
	}
	if got.DefaultLimitBytes != 1<<20 || got.TotalLimitBytes != 1<<30 {
		t.Errorf("limits = %d / %d, want the configured 1 MiB / 1 GiB", got.DefaultLimitBytes, got.TotalLimitBytes)
	}
}

func TestPurgeGroup(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	id, _ := g.group(t, "Anna")
	other, _ := g.group(t, "Ben")
	g.put(t, id, "a-small", 100)
	g.put(t, id, "a-blob", 4096)
	g.put(t, other, "b-blob", 4096)
	c := g.client()
	csrf := g.login(c)

	// What the confirmation page shows: the group and the blob storage a purge
	// frees, and no entry id.
	body := readBody(t, g.get(c, "/admin/api/groups/"+id))
	if !strings.Contains(body, `"used_bytes":4096`) {
		t.Errorf("group page does not show the 4 KiB a purge frees: %s", body)
	}
	for _, entry := range []string{"a-small", "a-blob"} {
		if strings.Contains(body, entry) {
			t.Errorf("group page shows entry id %q", entry)
		}
	}

	wantStatus(t, g.call(c, "", http.MethodPost, "/admin/api/groups/"+id+"/purge", nil), http.StatusForbidden)
	resp := g.call(c, csrf, http.MethodPost, "/admin/api/groups/"+id+"/purge", nil)
	wantStatus(t, resp, http.StatusOK)
	res := readJSON[struct {
		Entries int   `json:"entries"`
		Bytes   int64 `json:"bytes"`
	}](t, resp)
	if res.Entries != 2 {
		t.Errorf("purge removed %d entries, want 2", res.Entries)
	}
	ctx := context.Background()
	if _, _, err := g.svc.Latest(ctx, id); !errors.Is(err, entries.ErrNotFound) {
		t.Errorf("Latest() after purge error = %v, want %v", err, entries.ErrNotFound)
	}
	if _, _, err := g.svc.Latest(ctx, other); err != nil {
		t.Errorf("the other group lost its entries: %v", err)
	}
	if _, err := g.store.GetGroup(ctx, id); err != nil {
		t.Errorf("the purged group itself is gone: %v", err)
	}
	wantStatus(t, g.call(c, csrf, http.MethodPost, "/admin/api/groups/nope/purge", nil), http.StatusNotFound)
}

func TestPurgeUnavailable(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "https://tpp.example.com")
	c := f.client()
	csrf := f.login(c)
	tok, _ := f.store.CreateToken(context.Background(), "Anna")
	if _, err := f.store.CreateGroup(context.Background(), tok.Value, store.NewDevice{PublicKey: []byte("pk"), WrappedGroupKey: []byte("wk")}); err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	if page := readJSON[groupPage](t, f.get(c, "/admin/api/groups/"+tok.Value)); page.CanPurge {
		t.Error("group page offers purging without a purger")
	}
	wantStatus(t, f.call(c, csrf, http.MethodPost, "/admin/api/groups/"+tok.Value+"/purge", nil), http.StatusNotImplemented)
}

func TestDeleteGroup(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{GroupBytes: 1 << 20}, nil)
	id, device := g.group(t, "Anna")
	keep, _ := g.group(t, "Ben")
	g.put(t, id, "a-blob", 4096)
	c := g.client()
	csrf := g.login(c)
	ctx := context.Background()

	// An override that has to go with the group.
	wantStatus(t, g.call(c, csrf, http.MethodPut, "/admin/api/groups/"+id+"/limit", map[string]any{"enabled": true, "size": "2 MiB"}), http.StatusNoContent)

	wantStatus(t, g.call(c, "", http.MethodDelete, "/admin/api/groups/"+id, nil), http.StatusForbidden)
	resp := g.call(c, csrf, http.MethodDelete, "/admin/api/groups/"+id, nil)
	wantStatus(t, resp, http.StatusOK)
	if got := readJSON[map[string]string](t, resp)["name"]; got != "Anna" {
		t.Errorf("deleted group name = %q, want Anna", got)
	}
	// Deleting it again changes nothing and says so.
	resp = g.call(c, csrf, http.MethodDelete, "/admin/api/groups/"+id, nil)
	wantStatus(t, resp, http.StatusNotFound)
	if msg := apiError(t, resp); !strings.Contains(msg, "that group no longer exists") {
		t.Errorf("a second delete is not reported as a no-op: %q", msg)
	}
	if _, err := g.store.GetGroup(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetGroup() after delete error = %v, want %v", err, store.ErrNotFound)
	}
	if dead, _ := g.store.DeviceTombstoned(ctx, device); !dead {
		t.Errorf("the deleted group's device has no tombstone")
	}
	if len(g.live.closed) != 1 || g.live.closed[0] != id {
		t.Errorf("closed sockets of %v, want [%s]", g.live.closed, id)
	}
	if u, _ := g.ledger.GroupUsage(ctx, id); u.Bytes != 0 {
		t.Errorf("deleted group still accounts %d bytes", u.Bytes)
	}
	if _, own := g.ledgerLimit(t, id); own {
		t.Errorf("deleted group still has its own limit in the ledger")
	}
	if _, err := g.store.GetGroup(ctx, keep); err != nil {
		t.Errorf("the other group is gone too: %v", err)
	}

	tokens := readJSON[tokenList](t, g.get(c, "/admin/api/tokens"))
	var status string
	for _, tok := range tokens.Tokens {
		if tok.Value == id {
			status = tok.Status
		}
	}
	if status != "group_deleted" {
		t.Errorf("token of the deleted group has status %q, want group_deleted", status)
	}
	audit := readBody(t, g.get(c, "/admin/api/audit"))
	if !strings.Contains(audit, `"action":"group.delete"`) || !strings.Contains(audit, "Anna") {
		t.Errorf("audit log does not record the deletion: %s", audit)
	}
	if strings.Contains(audit, id) {
		t.Errorf("audit log shows a full group id, which is also the creation token")
	}
}

// groupListJSON is the groups endpoint's JSON, as a test reads it.
type groupListJSON struct {
	Groups []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Stale bool   `json:"stale"`
		Usage *struct {
			UsedBytes int64 `json:"used_bytes"`
			OverLimit bool  `json:"over_limit"`
		} `json:"usage"`
		LimitBytes int64 `json:"limit_bytes"`
	} `json:"groups"`
	Tags []struct {
		Name   string `json:"name"`
		Filter string `json:"filter"`
		Groups int    `json:"groups"`
	} `json:"tags"`
	Filter         string   `json:"filter"`
	GroupCount     int      `json:"group_count"`
	StaleCount     int      `json:"stale_count"`
	StaleAfterDays int      `json:"stale_after_days"`
	HasUsage       bool     `json:"has_usage"`
	Problems       []string `json:"problems"`
}

func (l groupListJSON) names() []string {
	out := make([]string, 0, len(l.Groups))
	for _, g := range l.Groups {
		out = append(out, g.Name)
	}
	return out
}

// TestStaleGroupsAreMarkedNotBulkDeleted: the groups screen marks groups idle
// past the stale threshold, and there is no way to delete several at once —
// a group is deleted from its own page, one at a time.
func TestStaleGroupsAreMarkedNotBulkDeleted(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	old, _ := g.group(t, "Old")
	other, _ := g.group(t, "Other")

	// Move the admin's clock 40 days past the groups' creation (the store
	// stamps them with the wall clock), before signing in so the session
	// lives on the moved clock.
	g.clk.advance(time.Since(g.clk.now()) + 40*24*time.Hour)
	c := g.client()
	csrf := g.login(c)

	// The default threshold is 90 days: nothing is stale yet.
	if list := readJSON[groupListJSON](t, g.get(c, "/admin/api/groups")); list.StaleCount != 0 || list.StaleAfterDays != 90 {
		t.Errorf("stale count = %d after %d days, want 0 under the 90-day default", list.StaleCount, list.StaleAfterDays)
	}
	wantStatus(t, g.call(c, csrf, http.MethodPut, "/admin/api/settings", map[string]int{"stale_days": 30}), http.StatusOK)

	// At 30 days both groups are stale; the dashboard counts them.
	if dash := readJSON[dashboardJSON](t, g.get(c, "/admin/api/dashboard")); dash.StaleCount != 2 {
		t.Errorf("dashboard stale count = %d, want 2", dash.StaleCount)
	}
	list := readJSON[groupListJSON](t, g.get(c, "/admin/api/groups"))
	if list.StaleCount != 2 || list.StaleAfterDays != 30 {
		t.Errorf("groups stale = %d after %d days, want 2 after 30", list.StaleCount, list.StaleAfterDays)
	}
	for _, grp := range list.Groups {
		if !grp.Stale {
			t.Errorf("group %s is not marked stale", grp.Name)
		}
	}

	// There is no bulk route: a request to delete several deletes nothing.
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		resp := g.call(c, csrf, method, "/admin/api/groups", map[string][]string{"id": {old, other}})
		if resp.StatusCode < 400 {
			t.Errorf("%s /admin/api/groups = %d, want it refused", method, resp.StatusCode)
		}
	}
	for _, id := range []string{old, other} {
		if _, err := g.store.GetGroup(context.Background(), id); err != nil {
			t.Errorf("group %s was deleted by a bulk request: %v", id, err)
		}
	}
}

func TestSettingsValidation(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	c := g.client()
	csrf := g.login(c)

	for _, v := range []any{0, -1, 1.5, 3651, "abc", nil} {
		resp := g.call(c, csrf, http.MethodPut, "/admin/api/settings", map[string]any{"stale_days": v})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("stale_days=%v: status = %d, want %d", v, resp.StatusCode, http.StatusBadRequest)
		}
	}
	resp := g.call(c, csrf, http.MethodPut, "/admin/api/settings", map[string]int{"stale_days": 14})
	wantStatus(t, resp, http.StatusOK)
	st, _ := g.store.GetSettings(context.Background())
	if st.StaleAfter != 14*24*time.Hour {
		t.Errorf("StaleAfter = %v, want 14 days", st.StaleAfter)
	}
	got := readJSON[struct {
		StaleDays   int `json:"stale_days"`
		DefaultDays int `json:"default_days"`
		MaxDays     int `json:"max_days"`
	}](t, g.get(c, "/admin/api/settings"))
	if got.StaleDays != 14 || got.DefaultDays != 90 || got.MaxDays != 3650 {
		t.Errorf("settings = %+v, want 14 days saved, 90 by default, at most 3650", got)
	}
}

func TestTokenNoteTagAndExpiry(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	c := g.client()
	csrf := g.login(c)
	ctx := context.Background()

	wantStatus(t, g.call(c, csrf, http.MethodPost, "/admin/api/tags", map[string]string{"name": "friends"}), http.StatusCreated)
	wantStatus(t, g.call(c, csrf, http.MethodPost, "/admin/api/tokens", map[string]string{
		"name": "Carla", "note": "new phone", "tag": "friends", "expires": "1",
	}), http.StatusCreated)

	tokens, _ := g.store.ListTokens(ctx)
	if len(tokens) != 1 {
		t.Fatalf("ListTokens() = %d tokens, want 1", len(tokens))
	}
	tok := tokens[0]
	if tok.Note != "new phone" || tok.Tag != "friends" || !tok.ExpiresAt.Equal(g.clk.now().Add(24*time.Hour)) {
		t.Errorf("token = note %q, tag %q, expires %v", tok.Note, tok.Tag, tok.ExpiresAt)
	}
	list := readJSON[tokenList](t, g.get(c, "/admin/api/tokens"))
	if len(list.Tags) != 1 || list.Tags[0] != "friends" {
		t.Errorf("token screen tags = %v, want [friends]", list.Tags)
	}
	got := list.Tokens[0]
	if got.Note != "new phone" || got.Tag != "friends" || got.ExpiresAt == nil || !got.ExpiresAt.Equal(tok.ExpiresAt) || got.Status != "unused" {
		t.Errorf("listed token = %+v", got)
	}

	for name, body := range map[string]map[string]string{
		"no name":        {"name": " "},
		"unknown tag":    {"name": "x", "tag": "nope"},
		"unknown expiry": {"name": "x", "expires": "5"},
		"long note":      {"name": "x", "note": strings.Repeat("n", 501)},
	} {
		if resp := g.call(c, csrf, http.MethodPost, "/admin/api/tokens", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", name, resp.StatusCode, http.StatusBadRequest)
		}
	}

	// Past its expiry the token's code and link are gone. The session has
	// expired too by then, so sign in again.
	g.clk.advance(25 * time.Hour)
	c = g.client()
	g.login(c)
	list = readJSON[tokenList](t, g.get(c, "/admin/api/tokens"))
	if got := list.Tokens[0]; got.Status != "expired" || got.URL != "" {
		t.Errorf("expired token = %+v, want status expired and no URL", got)
	}
	if resp := g.get(c, "/admin/tokens/"+tok.Value+"/qr.png"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("QR of an expired token: status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestHealthCard(t *testing.T) {
	t.Parallel()

	type redisHealth struct {
		Policy      string `json:"policy"`
		PolicyState string `json:"policy_state"`
		PolicyNote  string `json:"policy_note"`
		AOF         string `json:"aof"`
		AOFState    string `json:"aof_state"`
		Memory      string `json:"memory"`
		MaxMemory   string `json:"max_memory"`
	}
	tests := []struct {
		name  string
		redis admin.RedisInfo
		want  redisHealth
		note  string
	}{
		{
			name:  "good",
			redis: fakeRedis{config: map[string]string{"maxmemory-policy": "volatile-lru", "appendonly": "yes"}},
			want:  redisHealth{Policy: "volatile-lru", PolicyState: "good", AOF: "yes", AOFState: "good", Memory: "1.50M", MaxMemory: "no limit"},
		},
		{
			name:  "unsafe",
			redis: fakeRedis{config: map[string]string{"maxmemory-policy": "allkeys-lru", "appendonly": "no"}},
			want:  redisHealth{Policy: "allkeys-lru", PolicyState: "bad", AOF: "no", AOFState: "bad", Memory: "1.50M", MaxMemory: "no limit"},
			note:  "may evict groups",
		},
		{
			name:  "config refused",
			redis: fakeRedis{err: errors.New("ERR unknown command")},
			want:  redisHealth{Policy: "unknown", PolicyState: "unknown", AOF: "unknown", AOFState: "unknown", Memory: "unknown", MaxMemory: "unknown"},
			note:  "did not allow",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newGroupFixture(t, quota.Limits{}, tc.redis)
			c := g.client()
			g.login(c)
			g.clk.advance(90 * time.Minute)
			dash := readJSON[struct {
				Health struct {
					Version       string       `json:"version"`
					UptimeSeconds int64        `json:"uptime_seconds"`
					Redis         *redisHealth `json:"redis"`
				} `json:"health"`
			}](t, g.get(c, "/admin/api/dashboard"))
			h := dash.Health
			if h.Version != "v9.9.9 (abc123)" || h.UptimeSeconds != 90*60 {
				t.Errorf("health = version %q, uptime %ds; want v9.9.9 (abc123), 90 min", h.Version, h.UptimeSeconds)
			}
			if h.Redis == nil {
				t.Fatal("health has no Redis section")
			}
			got := *h.Redis
			note := got.PolicyNote
			got.PolicyNote = ""
			if got != tc.want {
				t.Errorf("redis health = %+v, want %+v", got, tc.want)
			}
			if !strings.Contains(note, tc.note) {
				t.Errorf("policy note = %q, want it to mention %q", note, tc.note)
			}
		})
	}
}

func TestSweepNow(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	c := g.client()
	csrf := g.login(c)

	if dash := readJSON[dashboardJSON](t, g.get(c, "/admin/api/dashboard")); !dash.CanSweep {
		t.Errorf("dashboard has no Sweep now button")
	}
	wantStatus(t, g.call(c, "", http.MethodPost, "/admin/api/sweep", nil), http.StatusForbidden)
	resp := g.call(c, csrf, http.MethodPost, "/admin/api/sweep", nil)
	wantStatus(t, resp, http.StatusOK)
	if !readJSON[struct {
		Queued bool `json:"queued"`
	}](t, resp).Queued {
		t.Error("sweep was not queued")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sweeps != 1 {
		t.Errorf("sweeps requested = %d, want 1", g.sweeps)
	}
}

func TestSweepUnavailable(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "https://tpp.example.com")
	c := f.client()
	csrf := f.login(c)
	if dash := readJSON[dashboardJSON](t, f.get(c, "/admin/api/dashboard")); dash.CanSweep {
		t.Error("dashboard offers a sweep without a sweeper")
	}
	wantStatus(t, f.call(c, csrf, http.MethodPost, "/admin/api/sweep", nil), http.StatusNotImplemented)
}

func TestDashboardTagFilter(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	anna, _ := g.group(t, "Anna")
	g.group(t, "Ben")
	c := g.client()
	csrf := g.login(c)

	// Without tags there is nothing to filter by.
	if list := readJSON[groupListJSON](t, g.get(c, "/admin/api/groups")); len(list.Tags) != 0 {
		t.Errorf("tag filter without tags = %+v, want none", list.Tags)
	}
	wantStatus(t, g.call(c, csrf, http.MethodPost, "/admin/api/tags", map[string]string{"name": "family"}), http.StatusCreated)
	wantStatus(t, g.call(c, csrf, http.MethodPut, "/admin/api/groups/"+anna+"/tag", map[string]string{"tag": "family"}), http.StatusNoContent)

	all := readJSON[groupListJSON](t, g.get(c, "/admin/api/groups"))
	if got := strings.Join(all.names(), ","); len(all.Groups) != 2 || !strings.Contains(got, "Anna") || !strings.Contains(got, "Ben") {
		t.Errorf("unfiltered groups = %v, want Anna and Ben", got)
	}
	if len(all.Tags) != 2 || all.Tags[0].Name != "family" || all.Tags[0].Groups != 1 || all.Tags[1].Name != "Untagged" || all.Tags[1].Filter != "-" {
		t.Errorf("tag filter = %+v, want family (1) then Untagged", all.Tags)
	}
	family := readJSON[groupListJSON](t, g.get(c, "/admin/api/groups?tag=family"))
	if got := family.names(); len(got) != 1 || got[0] != "Anna" || family.Filter != "family" || family.GroupCount != 2 {
		t.Errorf("family filter = %v (filter %q, of %d), want exactly Anna of 2", got, family.Filter, family.GroupCount)
	}
	untagged := readJSON[groupListJSON](t, g.get(c, "/admin/api/groups?tag=-"))
	if got := untagged.names(); len(got) != 1 || got[0] != "Ben" {
		t.Errorf("untagged filter = %v, want exactly Ben", got)
	}
}

func TestGroupRoutesRequireASession(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	id, _ := g.group(t, "Anna")
	c := g.client()

	for _, r := range []struct{ method, path string }{
		{http.MethodPut, "/admin/api/groups/" + id + "/name"},
		{http.MethodPut, "/admin/api/groups/" + id + "/tag"},
		{http.MethodPut, "/admin/api/groups/" + id + "/limit"},
		{http.MethodPost, "/admin/api/groups/" + id + "/purge"},
		{http.MethodDelete, "/admin/api/groups/" + id},
		{http.MethodPost, "/admin/api/tags"},
		{http.MethodPut, "/admin/api/tags/x/limit"},
		{http.MethodDelete, "/admin/api/tags/x"},
		{http.MethodPut, "/admin/api/settings"},
		{http.MethodPost, "/admin/api/sweep"},
		{http.MethodGet, "/admin/api/groups"},
		{http.MethodGet, "/admin/api/groups/" + id},
		{http.MethodGet, "/admin/api/tags"},
		{http.MethodGet, "/admin/api/settings"},
	} {
		resp := g.call(c, "", r.method, r.path, map[string]string{"name": "x"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d, want %d", r.method, r.path, resp.StatusCode, http.StatusUnauthorized)
		}
	}
	if _, err := g.store.GetGroup(context.Background(), id); err != nil {
		t.Errorf("a request without a session changed the group: %v", err)
	}
}

// TestAuditNeverHoldsSecrets: the audit log names what was done, never a
// token value or the password (CLAUDE.md, security rules).
func TestAuditNeverHoldsSecrets(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	c := g.client()

	// A failed sign-in, then a good one.
	g.tryLogin(c, "wrong password")
	csrf := g.login(c)
	wantStatus(t, g.call(c, csrf, http.MethodPost, "/admin/api/tokens", map[string]string{"name": "Dora"}), http.StatusCreated)
	tokens, _ := g.store.ListTokens(context.Background())

	entries, err := g.store.ListAudit(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListAudit() error = %v", err)
	}
	actions := make(map[string]bool)
	for _, e := range entries {
		actions[e.Action] = true
		for _, secret := range []string{tokens[0].Value, testPassword, "wrong password", csrf} {
			if strings.Contains(e.Target+e.Detail, secret) {
				t.Errorf("audit entry %+v holds a secret", e)
			}
		}
	}
	for _, want := range []string{"login.failed", "login", "token.create"} {
		if !actions[want] {
			t.Errorf("audit log has no %q entry", want)
		}
	}
	resp := g.get(c, "/admin/api/audit")
	wantStatus(t, resp, http.StatusOK)
	page := readBody(t, resp)
	if strings.Contains(page, tokens[0].Value) {
		t.Errorf("audit page shows a token value")
	}
	if !strings.Contains(page, `"kept":1000`) || !strings.Contains(page, `"target":"Dora"`) {
		t.Errorf("audit page = %s, want the kept count and the new token's name", page)
	}
}
