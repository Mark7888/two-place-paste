package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/admin"
	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/entries"
	"github.com/Mark7888/two-place-paste/server/internal/quota"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// dashboardFixture is an admin server whose dashboard reads a real entry
// service, ledger and blob store, so what it renders is what a deployment
// would.
type dashboardFixture struct {
	*fixture
	svc    *entries.Service
	ledger *quota.Memory
	sweep  blob.SweepStatus
}

func newDashboardFixture(t *testing.T, limits quota.Limits) *dashboardFixture {
	t.Helper()
	d := &dashboardFixture{ledger: quota.NewMemory(quota.Options{Limits: limits})}
	ephemeral := entries.NewMemoryStore()
	d.svc = entries.NewService(ephemeral, blob.NewDisk(t.TempDir(), 1<<20), entries.Options{
		InlineMaxBytes: 1024,
		MaxBytes:       1 << 20,
		Quota:          d.ledger,
	})
	d.fixture = newFixtureWithDashboard(t, "https://tpp.example.com", admin.Dashboard{
		Ledger:         d.ledger,
		Entries:        ephemeral,
		SweepStatus:    func() blob.SweepStatus { return d.sweep },
		Uploads:        func() (int, int64) { return 2, 30 << 20 },
		Backend:        "s3",
		Location:       "http://minio:9000/tpp/",
		TotalLimit:     limits.TotalBytes,
		GroupLimit:     limits.GroupBytes,
		EntryMaxBytes:  1 << 20,
		InlineMaxBytes: 1024,
	})
	return d
}

func (d *dashboardFixture) group(t *testing.T, name string, devices int) string {
	t.Helper()
	ctx := context.Background()
	tok, err := d.store.CreateToken(ctx, name)
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	if _, err := d.store.CreateGroup(ctx, tok.Value, store.NewDevice{Name: "first", PublicKey: []byte("pk"), WrappedGroupKey: []byte("wk")}); err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	for i := 1; i < devices; i++ {
		if _, err := d.store.AddDevice(ctx, tok.Value, store.NewDevice{Name: "more", PublicKey: []byte("pk")}); err != nil {
			t.Fatalf("AddDevice() error = %v", err)
		}
	}
	return tok.Value
}

func (d *dashboardFixture) put(t *testing.T, groupID, entryID string, size int) {
	t.Helper()
	body := strings.NewReader(strings.Repeat("c", size))
	if _, err := d.svc.Put(context.Background(), groupID, 1, entryID, int64(size), body); err != nil {
		t.Fatalf("Put(%s) error = %v", entryID, err)
	}
}

// dashboardJSON is the dashboard endpoint's JSON, as a test reads it.
type dashboardJSON struct {
	GroupCount   int `json:"group_count"`
	DeviceCount  int `json:"device_count"`
	UnusedTokens int `json:"unused_tokens"`
	StaleCount   int `json:"stale_count"`
	Storage      *struct {
		Backend         string `json:"backend"`
		Location        string `json:"location"`
		UsedBytes       int64  `json:"used_bytes"`
		LimitBytes      int64  `json:"limit_bytes"`
		Entries         int64  `json:"entries"`
		GroupLimitBytes int64  `json:"group_limit_bytes"`
		EntryMaxBytes   int64  `json:"entry_max_bytes"`
		InlineMaxBytes  int64  `json:"inline_max_bytes"`
		Evictions       int64  `json:"evictions"`
		EvictedBytes    int64  `json:"evicted_bytes"`
		Uploads         *struct {
			Count         int   `json:"count"`
			ReservedBytes int64 `json:"reserved_bytes"`
		} `json:"uploads"`
		LastSweep *struct {
			At     time.Time `json:"at"`
			TookMS int64     `json:"took_ms"`
			Error  string    `json:"error"`
		} `json:"last_sweep"`
	} `json:"storage"`
	CanSweep bool     `json:"can_sweep"`
	Problems []string `json:"problems"`
}

func TestDashboard(t *testing.T) {
	t.Parallel()

	d := newDashboardFixture(t, quota.Limits{TotalBytes: 100 << 10, GroupBytes: 40 << 10})
	c := d.client()
	d.login(c)

	small := d.group(t, "Anna", 2)
	big := d.group(t, "Ben", 1)
	if _, err := d.store.CreateToken(context.Background(), "spare"); err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	d.put(t, small, "anna-inline", 100)
	d.put(t, small, "anna-blob", 10<<10)
	d.put(t, big, "ben-1", 20<<10)
	d.put(t, big, "ben-2", 20<<10)
	d.put(t, big, "ben-3", 20<<10) // evicts ben-1: the group limit is 40 KiB
	d.sweep = blob.SweepStatus{At: time.Date(2026, 9, 9, 11, 50, 0, 0, time.UTC), Took: 3 * time.Millisecond}

	resp := d.get(c, "/admin/api/dashboard")
	wantStatus(t, resp, http.StatusOK)
	body := readBody(t, resp)
	var dash dashboardJSON
	if err := json.Unmarshal([]byte(body), &dash); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}

	if dash.GroupCount != 2 || dash.DeviceCount != 3 || dash.UnusedTokens != 1 {
		t.Errorf("counts = %d groups, %d devices, %d unused tokens; want 2, 3 (2 + 1), 1 (the spare)",
			dash.GroupCount, dash.DeviceCount, dash.UnusedTokens)
	}
	st := dash.Storage
	if st == nil {
		t.Fatal("dashboard has no storage section")
	}
	if st.UsedBytes != 50<<10 || st.LimitBytes != 100<<10 || st.Entries != 3 {
		t.Errorf("storage = %d of %d bytes in %d entries; want 50 KiB (10 + 2×20) of 100 KiB in 3", st.UsedBytes, st.LimitBytes, st.Entries)
	}
	if st.Uploads == nil || st.Uploads.Count != 2 || st.Uploads.ReservedBytes != 30<<20 {
		t.Errorf("uploads = %+v, want 2 holding 30 MiB", st.Uploads)
	}
	if st.Evictions != 1 || st.EvictedBytes != 20<<10 {
		t.Errorf("evictions = %d (%d bytes), want ben-1: 1 (20 KiB)", st.Evictions, st.EvictedBytes)
	}
	if st.Backend != "s3" || st.Location != "http://minio:9000/tpp/" || st.GroupLimitBytes != 40<<10 || st.EntryMaxBytes != 1<<20 || st.InlineMaxBytes != 1024 {
		t.Errorf("storage settings = %+v", st)
	}
	if st.LastSweep == nil || !st.LastSweep.At.Equal(d.sweep.At) || st.LastSweep.TookMS != 3 || st.LastSweep.Error != "" {
		t.Errorf("last sweep = %+v, want 11:50 taking 3ms, no error", st.LastSweep)
	}

	// The groups are on their own screen, not on the dashboard.
	if strings.Contains(body, "Anna") || strings.Contains(body, "Ben") {
		t.Errorf("dashboard lists the groups")
	}

	groups := readJSON[groupListJSON](t, d.get(c, "/admin/api/groups"))
	if len(groups.Groups) != 2 || !groups.HasUsage {
		t.Fatalf("groups = %+v, want two with storage", groups.Groups)
	}
	// Heaviest group first, Ben at his limit.
	ben := groups.Groups[0]
	if ben.Name != "Ben" || ben.Usage == nil || ben.Usage.UsedBytes != 40<<10 || ben.LimitBytes != 40<<10 || ben.Usage.OverLimit {
		t.Errorf("first group = %+v, want Ben at his 40 KiB limit", ben)
	}
	if groups.Groups[1].Name != "Anna" {
		t.Errorf("second group = %q, want Anna", groups.Groups[1].Name)
	}

	// Sizes and counts only: no entry id reaches either screen.
	raw := readBody(t, d.get(c, "/admin/api/groups"))
	for _, id := range []string{"anna-inline", "anna-blob", "ben-1", "ben-2", "ben-3"} {
		if strings.Contains(body, id) || strings.Contains(raw, id) {
			t.Errorf("an admin endpoint shows entry id %q; it must show counts only", id)
		}
	}
}

func TestDashboardSweepFailure(t *testing.T) {
	t.Parallel()

	d := newDashboardFixture(t, quota.Limits{})
	c := d.client()
	d.login(c)
	d.sweep = blob.SweepStatus{At: time.Date(2026, 9, 9, 11, 0, 0, 0, time.UTC), Err: errors.New("blob sweep: bucket unreachable")}

	dash := readJSON[dashboardJSON](t, d.get(c, "/admin/api/dashboard"))
	if dash.Storage == nil || dash.Storage.LastSweep == nil || dash.Storage.LastSweep.Error != "blob sweep: bucket unreachable" {
		t.Errorf("last sweep = %+v, want the sweep's error", dash.Storage)
	}
	if dash.Storage.LimitBytes != 0 {
		t.Errorf("total limit = %d, want none", dash.Storage.LimitBytes)
	}
}

// failingLedger fails every read, as Redis would when it is down.
type failingLedger struct{}

func (failingLedger) Usage(context.Context) (quota.Usage, error) {
	return quota.Usage{}, errors.New("down")
}
func (failingLedger) GroupUsage(context.Context, string) (quota.Usage, error) {
	return quota.Usage{}, errors.New("down")
}
func (failingLedger) Evictions(context.Context) (quota.Evictions, error) {
	return quota.Evictions{}, errors.New("down")
}

// TestDashboardDegrades: a part that cannot be read is named, and the rest of
// the screen still renders.
func TestDashboardDegrades(t *testing.T) {
	t.Parallel()

	f := newFixtureWithDashboard(t, "https://tpp.example.com", admin.Dashboard{Ledger: failingLedger{}})
	c := f.client()
	f.login(c)
	tok, err := f.store.CreateToken(context.Background(), "Carla")
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	if _, err := f.store.ConsumeToken(context.Background(), tok.Value); err != nil {
		t.Fatalf("ConsumeToken() error = %v", err)
	}

	resp := f.get(c, "/admin/api/dashboard")
	wantStatus(t, resp, http.StatusOK)
	dash := readJSON[dashboardJSON](t, resp)
	for _, want := range []string{"Could not read the storage usage.", "Could not read the storage of Carla."} {
		if !slices.Contains(dash.Problems, want) {
			t.Errorf("dashboard problems = %v, want %q", dash.Problems, want)
		}
	}
	if dash.GroupCount != 1 {
		t.Errorf("group count = %d, want Carla's group counted anyway", dash.GroupCount)
	}
	groups := readJSON[groupListJSON](t, f.get(c, "/admin/api/groups"))
	if len(groups.Groups) != 1 || groups.Groups[0].Name != "Carla" || !slices.Contains(groups.Problems, "Could not read the storage of Carla.") {
		t.Errorf("groups = %+v, problems %v; want Carla listed and the problem named", groups.Groups, groups.Problems)
	}
}

// TestDashboardWithoutStorage: with nothing wired but the store, the screens
// show groups and devices and no storage.
func TestDashboardWithoutStorage(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	c := f.client()
	f.login(c)

	dash := readJSON[dashboardJSON](t, f.get(c, "/admin/api/dashboard"))
	if dash.Storage != nil {
		t.Errorf("dashboard without a ledger has storage %+v", dash.Storage)
	}
	if len(dash.Problems) != 0 {
		t.Errorf("dashboard problems = %v, want none", dash.Problems)
	}
	groups := readJSON[groupListJSON](t, f.get(c, "/admin/api/groups"))
	if len(groups.Groups) != 0 || groups.HasUsage {
		t.Errorf("groups = %+v (usage %v), want none and no storage column", groups.Groups, groups.HasUsage)
	}
}
