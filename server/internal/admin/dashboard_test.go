package admin_test

import (
	"context"
	"errors"
	"net/http"
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

	resp := d.get(c, "/admin/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin/ = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := readBody(t, resp)

	for _, want := range []string{
		`aria-current="page">Dashboard`,
		"50.0 KiB <span class=\"muted\">of 100 KiB</span>", // 10 + 2×20 KiB stored
		"3 stored entries · 2 uploads in progress, 30.0 MiB reserved",
		">2</p>",                 // groups
		"1 unused token",         // the spare
		">3</p>",                 // devices: 2 + 1
		">1</p>",                 // evictions
		"20.0 KiB removed early", // ben-1
		`<a href="/admin/groups">View groups</a>`,
		"http://minio:9000/tpp/",
		"<dd>40.0 KiB</dd>", // per-group limit
		"10 min ago, took 3ms",
		`<span class="tag good">ok</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard does not contain %q", want)
		}
	}

	// The groups are on their own screen now, not on the dashboard.
	if strings.Contains(body, ">Anna</a>") || strings.Contains(body, ">Ben</a>") {
		t.Errorf("dashboard still lists the groups")
	}

	groups := readBody(t, d.get(c, "/admin/groups"))
	for _, want := range []string{
		`aria-current="page">Groups`,
		"Anna", "Ben",
		"40.0 KiB <span class=\"muted\">· 100%</span>", // Ben at his limit
	} {
		if !strings.Contains(groups, want) {
			t.Errorf("groups screen does not contain %q", want)
		}
	}
	// Heaviest group first.
	if strings.Index(groups, "Ben") > strings.Index(groups, "Anna") {
		t.Errorf("groups screen lists Anna before Ben; want the heavier group first")
	}

	// Sizes and counts only: no entry id reaches either page.
	for _, id := range []string{"anna-inline", "anna-blob", "ben-1", "ben-2", "ben-3"} {
		if strings.Contains(body, id) || strings.Contains(groups, id) {
			t.Errorf("an admin page shows entry id %q; it must show counts only", id)
		}
	}
}

func TestDashboardSweepFailure(t *testing.T) {
	t.Parallel()

	d := newDashboardFixture(t, quota.Limits{})
	c := d.client()
	d.login(c)
	d.sweep = blob.SweepStatus{At: time.Date(2026, 9, 9, 11, 0, 0, 0, time.UTC), Err: errors.New("blob sweep: bucket unreachable")}

	body := readBody(t, d.get(c, "/admin/"))
	for _, want := range []string{`<span class="tag bad">failed</span>`, "blob sweep: bucket unreachable", "no limit"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard does not contain %q", want)
		}
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
// the page still renders.
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

	resp := f.get(c, "/admin/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin/ = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := readBody(t, resp)
	for _, want := range []string{"Could not read the storage usage.", "Could not read the storage of Carla.", "Carla"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard does not contain %q", want)
		}
	}
}

// TestDashboardWithoutStorage: with nothing wired but the store, the page
// shows groups and devices and no storage sections.
func TestDashboardWithoutStorage(t *testing.T) {
	t.Parallel()

	f := newFixture(t, "https://tpp.example.com")
	c := f.client()
	f.login(c)

	body := readBody(t, f.get(c, "/admin/"))
	if groups := readBody(t, f.get(c, "/admin/groups")); !strings.Contains(groups, "No groups yet") {
		t.Errorf("groups screen without groups does not say so")
	}
	for _, absent := range []string{"Blob storage", "Evictions", "Last sweep"} {
		if strings.Contains(body, absent) {
			t.Errorf("dashboard without a ledger shows %q", absent)
		}
	}
}
