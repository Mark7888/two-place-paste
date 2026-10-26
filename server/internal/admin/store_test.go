package admin_test

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/admin"
	"github.com/Mark7888/two-place-paste/server/internal/items"
	"github.com/Mark7888/two-place-paste/server/internal/quota"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

var adminStoreConfig = items.Config{MaxTotalBytes: 1 << 30, MaxGroupBytes: 100 << 20, MaxItemBytes: 50 << 20, MaxGroupItems: 1000}

// storeFixture is an admin server with the store service and a fake
// transport wired in.
type storeFixture struct {
	*fixture
	live  *fakeLive
	items *items.Service
}

func newStoreFixture(t *testing.T) *storeFixture {
	t.Helper()
	f := &storeFixture{live: newFakeLive()}
	f.fixture = newFixtureWithStore(t, "https://tpp.example.com", func(o *admin.Options, st *store.MemoryStore) {
		f.items = items.New(items.Options{
			Registry: st,
			Config:   adminStoreConfig,
			Logger:   slog.New(slog.DiscardHandler),
			// As cmd/tpp wires it: a purge is told to the devices.
			Hooks: items.Hooks{OnStoreState: f.live.AnnounceStoreState},
		})
		o.Store = f.items
		o.Live = f.live
	})
	return f
}

func (f *storeFixture) group(t *testing.T, tok store.NewToken) string {
	t.Helper()
	ctx := context.Background()
	created, err := f.store.CreateTokenWith(ctx, tok)
	if err != nil {
		t.Fatalf("CreateTokenWith() error = %v", err)
	}
	if _, err := f.store.CreateGroup(ctx, created.Value, store.NewDevice{Name: "laptop", PublicKey: []byte("pk"), WrappedGroupKey: []byte("wk")}); err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	return created.Value
}

type storeSection struct {
	Enabled           bool   `json:"enabled"`
	Emptying          bool   `json:"emptying"`
	UsedBytes         int64  `json:"used_bytes"`
	UsedItems         int64  `json:"used_items"`
	LimitBytes        int64  `json:"limit_bytes"`
	LimitSource       string `json:"limit_source"`
	TagLimitBytes     *int64 `json:"tag_limit_bytes"`
	DefaultLimitBytes int64  `json:"default_limit_bytes"`
	OwnLimit          struct {
		Enabled bool  `json:"enabled"`
		Bytes   int64 `json:"bytes"`
	} `json:"own_limit"`
	MaxItems     int64 `json:"max_items"`
	MaxItemBytes int64 `json:"max_item_bytes"`
}

func (f *storeFixture) section(t *testing.T, c *http.Client, id string) *storeSection {
	t.Helper()
	page := readJSON[struct {
		Store *storeSection `json:"store"`
	}](t, f.get(c, "/admin/api/groups/"+admin.HandleOf(id)))
	if page.Store == nil {
		t.Fatal("group page has no store section")
	}
	return page.Store
}

// TestGroupStoreSwitchAndLimits: the group page shows the store, off until
// turned on; turning it on or off is told to the group's devices and
// audited; a group and a tag can override its limit, never above the total
// (SPEC §4.4, §4.6).
func TestGroupStoreSwitchAndLimits(t *testing.T) {
	t.Parallel()
	f := newStoreFixture(t)
	ctx := context.Background()
	if _, err := f.store.CreateTag(ctx, "work"); err != nil {
		t.Fatalf("CreateTag() error = %v", err)
	}
	id := f.group(t, store.NewToken{Name: "Anna", Tag: "work"})
	c := f.client()
	csrf := f.login(c)
	base := "/admin/api/groups/" + admin.HandleOf(id)

	got := f.section(t, c, id)
	if got.Enabled || got.LimitBytes != 100<<20 || got.LimitSource != "default" || got.MaxItems != 1000 || got.MaxItemBytes != 50<<20 {
		t.Errorf("store section = %+v, want off with the 100 MiB default", got)
	}

	wantStatus(t, f.call(c, csrf, http.MethodPut, base+"/store", map[string]bool{"enabled": true}), http.StatusNoContent)
	if g, _ := f.store.GetGroup(ctx, id); !g.StoreEnabled {
		t.Error("the store is still off after turning it on")
	}
	if on, told := f.live.storeState(id); !told || !on {
		t.Errorf("devices told = %v (%v), want on", on, told)
	}

	wantStatus(t, f.call(c, csrf, http.MethodPut, "/admin/api/tags/work/store-limit", map[string]any{"enabled": true, "size": "10 MiB"}), http.StatusNoContent)
	got = f.section(t, c, id)
	if got.LimitBytes != 10<<20 || got.LimitSource != "tag" || got.TagLimitBytes == nil || *got.TagLimitBytes != 10<<20 {
		t.Errorf("store section with a tag limit = %+v, want the tag's 10 MiB", got)
	}

	wantStatus(t, f.call(c, csrf, http.MethodPut, base+"/store-limit", map[string]any{"enabled": true, "size": "20 MiB"}), http.StatusNoContent)
	got = f.section(t, c, id)
	if got.LimitBytes != 20<<20 || got.LimitSource != "group" || !got.OwnLimit.Enabled || got.OwnLimit.Bytes != 20<<20 {
		t.Errorf("store section with its own limit = %+v, want its own 20 MiB", got)
	}
	if g, _ := f.store.GetGroup(ctx, id); g.Limit != (store.LimitOverride{}) {
		t.Errorf("the blob limit changed with the store limit: %+v", g.Limit)
	}

	for name, body := range map[string]map[string]any{
		"above the total": {"enabled": true, "size": "2 GiB"},
		"not a size":      {"enabled": true, "size": "lots"},
	} {
		if resp := f.call(c, csrf, http.MethodPut, base+"/store-limit", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", name, resp.StatusCode, http.StatusBadRequest)
		}
	}

	wantStatus(t, f.call(c, csrf, http.MethodPut, base+"/store", map[string]bool{"enabled": false}), http.StatusNoContent)
	if on, told := f.live.storeState(id); !told || on {
		t.Errorf("devices told = %v (%v), want off", on, told)
	}
	if got := f.section(t, c, id); got.Enabled || got.LimitBytes != 20<<20 {
		t.Errorf("store section after turning it off = %+v, want off with its limit kept", got)
	}

	log := readJSON[struct {
		Rows []struct {
			Action string `json:"action"`
			Detail string `json:"detail"`
		} `json:"rows"`
	}](t, f.get(c, "/admin/api/audit"))
	seen := map[string]bool{}
	for _, e := range log.Rows {
		seen[e.Action] = true
	}
	for _, a := range []string{"group.store", "group.storelimit", "tag.storelimit"} {
		if !seen[a] {
			t.Errorf("audit log has no %s entry", a)
		}
	}
}

// TestRelayWithoutAStoreService: without the store, the group page has no
// store section and the store endpoints answer 501.
func TestRelayWithoutAStoreService(t *testing.T) {
	t.Parallel()
	g := newGroupFixture(t, quota.Limits{}, nil)
	id, _ := g.group(t, "Anna")
	c := g.client()
	csrf := g.login(c)

	page := readJSON[struct {
		Store *storeSection `json:"store"`
	}](t, g.get(c, "/admin/api/groups/"+admin.HandleOf(id)))
	if page.Store != nil {
		t.Errorf("store section = %+v, want none", page.Store)
	}
	wantStatus(t, g.call(c, csrf, http.MethodPut, "/admin/api/groups/"+admin.HandleOf(id)+"/store", map[string]bool{"enabled": true}), http.StatusNotImplemented)
}

// TestPurgeGroupStore: Purge store answers at once, shows the store as being
// emptied until the background reclaim is done, keeps the switch, and is
// audited (SPEC §4.6).
func TestPurgeGroupStore(t *testing.T) {
	t.Parallel()
	f := newStoreFixture(t)
	ctx := context.Background()
	id := f.group(t, store.NewToken{Name: "Anna"})
	if err := f.store.SetGroupStore(ctx, id, true); err != nil {
		t.Fatalf("SetGroupStore() error = %v", err)
	}
	c := f.client()
	csrf := f.login(c)
	base := "/admin/api/groups/" + admin.HandleOf(id)

	wantStatus(t, f.call(c, csrf, http.MethodPost, base+"/store/purge", nil), http.StatusNoContent)
	if got := f.section(t, c, id); !got.Emptying || !got.Enabled {
		t.Errorf("store section after the purge = %+v, want on and emptying", got)
	}
	if on, told := f.live.storeState(id); !told || on {
		t.Errorf("devices told = %v (%v), want closed", on, told)
	}

	if err := f.items.ReclaimQueued(ctx); err != nil {
		t.Fatalf("ReclaimQueued() error = %v", err)
	}
	if got := f.section(t, c, id); got.Emptying || !got.Enabled {
		t.Errorf("store section after the reclaim = %+v, want on and not emptying", got)
	}
	if on, _ := f.live.storeState(id); !on {
		t.Error("devices were not told the store opened again")
	}

	log := readJSON[struct {
		Rows []struct {
			Action string `json:"action"`
		} `json:"rows"`
	}](t, f.get(c, "/admin/api/audit"))
	found := false
	for _, e := range log.Rows {
		found = found || e.Action == "group.storepurge"
	}
	if !found {
		t.Error("audit log has no group.storepurge entry")
	}

	if resp := f.call(c, csrf, http.MethodPost, "/admin/api/groups/"+admin.HandleOf("missing")+"/store/purge", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("purge of a missing group: status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// TestGroupListStore: the groups list shows each group's store — on or off,
// what it holds, its cap — and sorts by it; a relay without a store has no
// store column (SPEC §4.4, §4.6).
func TestGroupListStore(t *testing.T) {
	t.Parallel()
	f := newStoreFixture(t)
	ctx := context.Background()
	on := f.group(t, store.NewToken{Name: "Anna"})
	off := f.group(t, store.NewToken{Name: "Bela"})
	if err := f.store.SetGroupStore(ctx, on, true); err != nil {
		t.Fatalf("SetGroupStore() error = %v", err)
	}
	g, err := f.store.GetGroup(ctx, on)
	if err != nil {
		t.Fatalf("GetGroup() error = %v", err)
	}
	r := store.ItemReservation{GroupID: on, ItemID: "item-1", Epoch: g.Epoch, Size: 3 << 20, Accounted: 3 << 20, StartedAt: time.Now()}
	if err := f.store.ReserveItem(ctx, r, store.ItemLimits{GroupItems: 10}); err != nil {
		t.Fatalf("ReserveItem() error = %v", err)
	}
	c := f.client()
	f.login(c)

	type row struct {
		Handle string `json:"handle"`
		Store  *struct {
			Enabled    bool  `json:"enabled"`
			UsedBytes  int64 `json:"used_bytes"`
			UsedItems  int64 `json:"used_items"`
			LimitBytes int64 `json:"limit_bytes"`
		} `json:"store"`
	}
	type list struct {
		Rows     []row `json:"rows"`
		HasStore bool  `json:"has_store"`
	}
	for _, dir := range []string{"desc", "asc"} {
		got := readJSON[list](t, f.get(c, "/admin/api/groups?sort=store&dir="+dir))
		if !got.HasStore || len(got.Rows) != 2 {
			t.Fatalf("list = %+v, want two groups and a store column", got)
		}
		first, second := admin.HandleOf(on), admin.HandleOf(off)
		if dir == "asc" {
			first, second = second, first
		}
		if got.Rows[0].Handle != first || got.Rows[1].Handle != second {
			t.Errorf("sort=store dir=%s = %s, %s, want %s, %s", dir, got.Rows[0].Handle, got.Rows[1].Handle, first, second)
		}
		for _, r := range got.Rows {
			if r.Store == nil {
				t.Fatalf("row %s has no store", r.Handle)
			}
			want := r.Handle == admin.HandleOf(on)
			if r.Store.Enabled != want || r.Store.LimitBytes != 100<<20 {
				t.Errorf("row %s store = %+v, want enabled %v with the 100 MiB default", r.Handle, *r.Store, want)
			}
			if want && (r.Store.UsedBytes != 3<<20 || r.Store.UsedItems != 1) {
				t.Errorf("row %s store = %+v, want 3 MiB in 1 item", r.Handle, *r.Store)
			}
		}
	}

	bare := newGroupFixture(t, quota.Limits{}, nil)
	bare.group(t, "Anna")
	bc := bare.client()
	bare.login(bc)
	got := readJSON[list](t, bare.get(bc, "/admin/api/groups"))
	if got.HasStore || len(got.Rows) != 1 || got.Rows[0].Store != nil {
		t.Errorf("list without a store = %+v, want no store column", got)
	}
}
