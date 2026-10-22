package admin_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
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

func wantStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("%s %s = %d, want %d", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, want)
	}
}

func TestGroupPageAndRename(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{GroupBytes: 1 << 20}, nil)
	id, device := g.group(t, "Anna")
	g.live.online[device] = true
	c := g.client()
	csrf := g.login(c)

	body := readBody(t, g.get(c, "/admin/groups/"+id))
	for _, want := range []string{"Anna", "Anna&#39;s laptop", `<span class="tag good">online</span>`, "Purge entries…", "Delete group…"} {
		if !strings.Contains(body, want) {
			t.Errorf("group page does not contain %q", want)
		}
	}

	resp := g.postForm(c, "/admin/groups/"+id+"/rename", url.Values{"csrf": {csrf}, "name": {"Family"}})
	wantStatus(t, resp, http.StatusSeeOther)
	got, err := g.store.GetGroup(context.Background(), id)
	if err != nil || got.Name != "Family" {
		t.Errorf("group name = %q, %v; want Family", got.Name, err)
	}
	if g.live.renamed[id] != "Family" {
		t.Errorf("devices were told %q, want Family", g.live.renamed[id])
	}
	if body := readBody(t, g.get(c, "/admin/groups/"+id)); !strings.Contains(body, "Group renamed.") {
		t.Errorf("group page after rename does not confirm it")
	}

	for name, form := range map[string]url.Values{
		"empty name": {"csrf": {csrf}, "name": {"   "}},
		"too long":   {"csrf": {csrf}, "name": {strings.Repeat("x", 121)}},
	} {
		if resp := g.postForm(c, "/admin/groups/"+id+"/rename", form); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", name, resp.StatusCode, http.StatusBadRequest)
		}
	}
	if resp := g.postForm(c, "/admin/groups/"+id+"/rename", url.Values{"name": {"x"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("rename without csrf: status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	if resp := g.get(c, "/admin/groups/nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown group page: status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// TestStorageLimitPrecedence walks default < tag < group through the admin
// screens and checks what the ledger enforces at each step
// (docs/internal/plans/admin-group-management.md D3).
func TestStorageLimitPrecedence(t *testing.T) {
	t.Parallel()
	const def, total = 1 << 20, 100 << 30
	g := newGroupFixture(t, quota.Limits{GroupBytes: def, TotalBytes: total}, nil)
	id, _ := g.group(t, "Anna")
	c := g.client()
	csrf := g.login(c)
	ctx := context.Background()

	steps := []struct {
		name     string
		path     string
		form     url.Values
		want     int64
		wantOwn  bool
		wantCode int
	}{
		{name: "create tag", path: "/admin/tags", form: url.Values{"name": {"family"}}, want: def},
		{name: "tag limit on", path: "/admin/tags/family/limit", form: url.Values{"limit_on": {"1"}, "limit": {"5 GiB"}}, want: def},
		{name: "tag the group", path: "/admin/groups/" + id + "/tag", form: url.Values{"tag": {"family"}}, want: 5 << 30, wantOwn: true},
		{name: "group limit on", path: "/admin/groups/" + id + "/limit", form: url.Values{"limit_on": {"1"}, "limit": {"2 GiB"}}, want: 2 << 30, wantOwn: true},
		{name: "group limit off keeps tag", path: "/admin/groups/" + id + "/limit", form: url.Values{"limit": {""}}, want: 5 << 30, wantOwn: true},
		{name: "tag limit off", path: "/admin/tags/family/limit", form: url.Values{"limit": {""}}, want: def},
		{name: "group limit zero", path: "/admin/groups/" + id + "/limit", form: url.Values{"limit_on": {"1"}, "limit": {"0"}}, want: 0, wantOwn: true},
		{name: "above total refused", path: "/admin/groups/" + id + "/limit", form: url.Values{"limit_on": {"1"}, "limit": {"200 GiB"}}, want: 0, wantOwn: true, wantCode: http.StatusBadRequest},
		{name: "not a size refused", path: "/admin/groups/" + id + "/limit", form: url.Values{"limit_on": {"1"}, "limit": {"lots"}}, want: 0, wantOwn: true, wantCode: http.StatusBadRequest},
		{name: "group limit off", path: "/admin/groups/" + id + "/limit", form: url.Values{}, want: def},
	}
	for _, st := range steps {
		st.form.Set("csrf", csrf)
		resp := g.postForm(c, st.path, st.form)
		wantCode := st.wantCode
		if wantCode == 0 {
			wantCode = http.StatusSeeOther
		}
		if resp.StatusCode != wantCode {
			t.Fatalf("%s: status = %d, want %d", st.name, resp.StatusCode, wantCode)
		}
		limit, own := g.ledgerLimit(t, id)
		if limit != st.want || own != st.wantOwn {
			t.Errorf("%s: ledger limit = %d (own %v), want %d (own %v)", st.name, limit, own, st.want, st.wantOwn)
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
	if resp := g.postForm(c, "/admin/tags/family/limit", url.Values{"csrf": {csrf}, "limit_on": {"1"}, "limit": {"3 GiB"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("tag limit on again: status = %d", resp.StatusCode)
	}
	if limit, _ := g.ledgerLimit(t, id); limit != 3<<30 {
		t.Fatalf("ledger limit = %d, want the tag's 3 GiB", limit)
	}
	wantStatus(t, g.postForm(c, "/admin/tags/family/delete", url.Values{"csrf": {csrf}}), http.StatusSeeOther)
	if limit, own := g.ledgerLimit(t, id); limit != def || own {
		t.Errorf("ledger limit after tag delete = %d (own %v), want the default", limit, own)
	}
	if grp, _ := g.store.GetGroup(ctx, id); grp.Tag != "" {
		t.Errorf("group tag after tag delete = %q, want none", grp.Tag)
	}
}

func TestTagNamesAreValidated(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	c := g.client()
	csrf := g.login(c)

	for _, name := range []string{"", "-", " friends", "a/b", strings.Repeat("x", 41)} {
		resp := g.postForm(c, "/admin/tags", url.Values{"csrf": {csrf}, "name": {name}})
		if name == " friends" {
			// Surrounding space is trimmed, not refused.
			wantStatus(t, resp, http.StatusSeeOther)
			continue
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("tag %q: status = %d, want %d", name, resp.StatusCode, http.StatusBadRequest)
		}
	}
	if resp := g.postForm(c, "/admin/tags", url.Values{"csrf": {csrf}, "name": {"friends"}}); resp.StatusCode != http.StatusConflict {
		t.Errorf("duplicate tag: status = %d, want %d", resp.StatusCode, http.StatusConflict)
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

	body := readBody(t, g.get(c, "/admin/groups/"+id+"/purge"))
	for _, want := range []string{"Purge entries", "Anna", "4.0 KiB of blob storage freed"} {
		if !strings.Contains(body, want) {
			t.Errorf("purge confirmation does not contain %q", want)
		}
	}
	for _, id := range []string{"a-small", "a-blob"} {
		if strings.Contains(body, id) {
			t.Errorf("purge confirmation shows entry id %q", id)
		}
	}

	wantStatus(t, g.postForm(c, "/admin/groups/"+id+"/purge", url.Values{"csrf": {csrf}}), http.StatusSeeOther)
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
	if body := readBody(t, g.get(c, "/admin/groups/"+id)); !strings.Contains(body, "Purged 2 entries") {
		t.Errorf("group page after purge does not confirm it")
	}
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
	wantStatus(t, g.postForm(c, "/admin/groups/"+id+"/limit", url.Values{"csrf": {csrf}, "limit_on": {"1"}, "limit": {"2 MiB"}}), http.StatusSeeOther)

	body := readBody(t, g.get(c, "/admin/groups/"+id+"/delete"))
	if !strings.Contains(body, "Delete group") || !strings.Contains(body, "Anna") {
		t.Errorf("delete confirmation does not name the group")
	}

	wantStatus(t, g.postForm(c, "/admin/groups/"+id+"/delete", url.Values{"csrf": {csrf}}), http.StatusSeeOther)
	if body := readBody(t, g.get(c, "/admin/")); !strings.Contains(body, "Deleted the group “Anna”.") {
		t.Errorf("dashboard after delete does not confirm it")
	}
	// Deleting it again changes nothing and says so.
	wantStatus(t, g.postForm(c, "/admin/groups/"+id+"/delete", url.Values{"csrf": {csrf}}), http.StatusSeeOther)
	if body := readBody(t, g.get(c, "/admin/")); !strings.Contains(body, "that group no longer exists") {
		t.Errorf("a second delete is not reported as a no-op")
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

	tokens := readBody(t, g.get(c, "/admin/tokens"))
	if !strings.Contains(tokens, "group deleted") {
		t.Errorf("token screen does not say the group was deleted")
	}
	audit := readBody(t, g.get(c, "/admin/audit"))
	if !strings.Contains(audit, "Group deleted") || !strings.Contains(audit, "Anna") {
		t.Errorf("audit log does not record the deletion")
	}
	if strings.Contains(audit, id) {
		t.Errorf("audit log shows a full group id, which is also the creation token")
	}
}

// TestStaleGroupsAreMarkedNotBulkDeleted: the dashboard marks groups idle
// past the stale threshold, and offers no way to delete several at once —
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
	if body := readBody(t, g.get(c, "/admin/")); strings.Contains(body, "tag warn\">stale") {
		t.Errorf("dashboard marks groups stale under the default threshold")
	}
	wantStatus(t, g.postForm(c, "/admin/settings", url.Values{"csrf": {csrf}, "stale_days": {"30"}}), http.StatusSeeOther)

	// At 30 days both groups are stale.
	body := readBody(t, g.get(c, "/admin/"))
	if !strings.Contains(body, "2 groups have had no device connect for more than 30 days") {
		t.Errorf("dashboard does not report both groups stale")
	}
	if n := strings.Count(body, `<span class="tag warn">stale</span>`); n != 2 {
		t.Errorf("dashboard marks %d groups stale, want 2", n)
	}
	for _, gone := range []string{`type="checkbox"`, "Delete selected", "/admin/groups/delete"} {
		if strings.Contains(body, gone) {
			t.Errorf("dashboard still offers bulk deletion: it contains %q", gone)
		}
	}

	// The bulk route is gone: a POST to it deletes nothing.
	resp := g.postForm(c, "/admin/groups/delete", url.Values{"csrf": {csrf}, "id": {old, other}})
	if resp.StatusCode == http.StatusSeeOther {
		t.Errorf("POST /admin/groups/delete = %d, want it refused", resp.StatusCode)
	}
	for _, id := range []string{old, other} {
		if _, err := g.store.GetGroup(context.Background(), id); err != nil {
			t.Errorf("group %s was deleted through the removed bulk route: %v", id, err)
		}
	}
}

func TestSettingsValidation(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	c := g.client()
	csrf := g.login(c)

	for _, v := range []string{"0", "-1", "abc", "3651", ""} {
		if resp := g.postForm(c, "/admin/settings", url.Values{"csrf": {csrf}, "stale_days": {v}}); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("stale_days=%q: status = %d, want %d", v, resp.StatusCode, http.StatusBadRequest)
		}
	}
	wantStatus(t, g.postForm(c, "/admin/settings", url.Values{"csrf": {csrf}, "stale_days": {"14"}}), http.StatusSeeOther)
	st, _ := g.store.GetSettings(context.Background())
	if st.StaleAfter != 14*24*time.Hour {
		t.Errorf("StaleAfter = %v, want 14 days", st.StaleAfter)
	}
	if body := readBody(t, g.get(c, "/admin/settings")); !strings.Contains(body, `value="14"`) {
		t.Errorf("settings page does not show the saved value")
	}
}

func TestTokenNoteTagAndExpiry(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	c := g.client()
	csrf := g.login(c)
	ctx := context.Background()

	wantStatus(t, g.postForm(c, "/admin/tags", url.Values{"csrf": {csrf}, "name": {"friends"}}), http.StatusSeeOther)
	wantStatus(t, g.postForm(c, "/admin/tokens", url.Values{
		"csrf": {csrf}, "name": {"Carla"}, "note": {"new phone"}, "tag": {"friends"}, "expires": {"1"},
	}), http.StatusSeeOther)

	tokens, _ := g.store.ListTokens(ctx)
	if len(tokens) != 1 {
		t.Fatalf("ListTokens() = %d tokens, want 1", len(tokens))
	}
	tok := tokens[0]
	if tok.Note != "new phone" || tok.Tag != "friends" || !tok.ExpiresAt.Equal(g.clk.now().Add(24*time.Hour)) {
		t.Errorf("token = note %q, tag %q, expires %v", tok.Note, tok.Tag, tok.ExpiresAt)
	}
	body := readBody(t, g.get(c, "/admin/tokens"))
	for _, want := range []string{"new phone", "friends", "Copy link", "expires "} {
		if !strings.Contains(body, want) {
			t.Errorf("token screen does not contain %q", want)
		}
	}

	for name, form := range map[string]url.Values{
		"unknown tag":    {"csrf": {csrf}, "name": {"x"}, "tag": {"nope"}},
		"unknown expiry": {"csrf": {csrf}, "name": {"x"}, "expires": {"5"}},
		"long note":      {"csrf": {csrf}, "name": {"x"}, "note": {strings.Repeat("n", 501)}},
	} {
		if resp := g.postForm(c, "/admin/tokens", form); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", name, resp.StatusCode, http.StatusBadRequest)
		}
	}

	// Past its expiry the token's code and link are gone. The session has
	// expired too by then, so sign in again.
	g.clk.advance(25 * time.Hour)
	c = g.client()
	g.login(c)
	body = readBody(t, g.get(c, "/admin/tokens"))
	if !strings.Contains(body, ">expired<") {
		t.Errorf("token screen does not show the token as expired")
	}
	if resp := g.get(c, "/admin/tokens/"+tok.Value+"/qr.png"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("QR of an expired token: status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestHealthCard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		redis admin.RedisInfo
		want  []string
	}{
		{
			name:  "good",
			redis: fakeRedis{config: map[string]string{"maxmemory-policy": "volatile-lru", "appendonly": "yes"}},
			want:  []string{"v9.9.9 (abc123)", "<code>volatile-lru</code> <span class=\"tag good\">ok</span>", "<code>yes</code> <span class=\"tag good\">ok</span>", "1.50M", "no limit"},
		},
		{
			name:  "unsafe",
			redis: fakeRedis{config: map[string]string{"maxmemory-policy": "allkeys-lru", "appendonly": "no"}},
			want:  []string{`<span class="tag bad">unsafe</span>`, `<span class="tag bad">off</span>`, "may evict groups"},
		},
		{
			name:  "config refused",
			redis: fakeRedis{err: errors.New("ERR unknown command")},
			want:  []string{`<span class="tag unknown">unknown</span>`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newGroupFixture(t, quota.Limits{}, tc.redis)
			c := g.client()
			g.login(c)
			body := readBody(t, g.get(c, "/admin/"))
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("dashboard does not contain %q", want)
				}
			}
		})
	}
}

func TestSweepNow(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	c := g.client()
	csrf := g.login(c)

	if body := readBody(t, g.get(c, "/admin/")); !strings.Contains(body, "Sweep now") {
		t.Errorf("dashboard has no Sweep now button")
	}
	wantStatus(t, g.postForm(c, "/admin/sweep", url.Values{"csrf": {csrf}}), http.StatusSeeOther)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sweeps != 1 {
		t.Errorf("sweeps requested = %d, want 1", g.sweeps)
	}
}

func TestDashboardTagFilter(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	anna, _ := g.group(t, "Anna")
	g.group(t, "Ben")
	c := g.client()
	csrf := g.login(c)
	wantStatus(t, g.postForm(c, "/admin/tags", url.Values{"csrf": {csrf}, "name": {"family"}}), http.StatusSeeOther)
	wantStatus(t, g.postForm(c, "/admin/groups/"+anna+"/tag", url.Values{"csrf": {csrf}, "tag": {"family"}}), http.StatusSeeOther)

	all := readBody(t, g.get(c, "/admin/"))
	if !strings.Contains(all, "Anna") || !strings.Contains(all, "Ben") || !strings.Contains(all, "Untagged") {
		t.Errorf("unfiltered dashboard does not list both groups and the untagged total")
	}
	family := readBody(t, g.get(c, "/admin/?tag=family"))
	if !strings.Contains(family, `href="/admin/groups/`+anna+`"`) || strings.Contains(family, ">Ben</a>") {
		t.Errorf("family filter does not show exactly Anna")
	}
	untagged := readBody(t, g.get(c, "/admin/?tag=-"))
	if !strings.Contains(untagged, ">Ben</a>") || strings.Contains(untagged, ">Anna</a>") {
		t.Errorf("untagged filter does not show exactly Ben")
	}
}

func TestGroupRoutesRequireASession(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	id, _ := g.group(t, "Anna")
	c := g.client()

	for _, path := range []string{
		"/admin/groups/" + id + "/rename", "/admin/groups/" + id + "/tag", "/admin/groups/" + id + "/limit",
		"/admin/groups/" + id + "/purge", "/admin/groups/" + id + "/delete",
		"/admin/tags", "/admin/tags/x/limit", "/admin/tags/x/delete", "/admin/settings", "/admin/sweep",
	} {
		resp := g.postForm(c, path, url.Values{"name": {"x"}, "id": {id}})
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/login" {
			t.Errorf("POST %s without a session = %d → %q, want a redirect to sign in", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	for _, path := range []string{"/admin/groups/" + id, "/admin/groups/" + id + "/delete", "/admin/tags", "/admin/settings", "/admin/audit"} {
		resp := g.get(c, path)
		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("GET %s without a session = %d, want a redirect", path, resp.StatusCode)
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
	g.postForm(c, "/admin/login", url.Values{"password": {"wrong password"}})
	csrf := g.login(c)
	wantStatus(t, g.postForm(c, "/admin/tokens", url.Values{"csrf": {csrf}, "name": {"Dora"}}), http.StatusSeeOther)
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
	page := readBody(t, g.get(c, "/admin/audit"))
	if strings.Contains(page, tokens[0].Value) {
		t.Errorf("audit page shows a token value")
	}
}
