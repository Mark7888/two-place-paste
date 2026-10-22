package quota_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/quota"
)

// TestGroupLimitOverride holds both ledgers to the precedence of
// docs/internal/plans/admin-group-management.md D3: a group's own limit, when
// it has one, replaces the default; dropping it brings the default back.
func TestGroupLimitOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		own       int64
		set       bool
		size      int64
		wantErr   error
		wantLimit int64
		wantOwn   bool
	}{
		{name: "default applies", size: 150, wantErr: quota.ErrTooLarge, wantLimit: 100},
		{name: "larger own limit admits more", own: 200, set: true, size: 150, wantLimit: 200, wantOwn: true},
		{name: "smaller own limit refuses", own: 50, set: true, size: 60, wantErr: quota.ErrTooLarge, wantLimit: 50, wantOwn: true},
		{name: "zero means no group limit", own: 0, set: true, size: 900, wantLimit: 0, wantOwn: true},
		{name: "total still applies", own: 0, set: true, size: 1100, wantErr: quota.ErrTooLarge, wantLimit: 0, wantOwn: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			forEachLedger(t, quota.Limits{TotalBytes: 1000, GroupBytes: 100}, func(t *testing.T, l quota.Ledger, _ *clock) {
				ctx := context.Background()
				if tc.set {
					if err := l.SetGroupLimit(ctx, "g", tc.own, true); err != nil {
						t.Fatalf("SetGroupLimit() error = %v", err)
					}
				}
				limit, own, err := l.GroupLimit(ctx, "g")
				if err != nil {
					t.Fatalf("GroupLimit() error = %v", err)
				}
				if limit != tc.wantLimit || own != tc.wantOwn {
					t.Errorf("GroupLimit() = %d, %v; want %d, %v", limit, own, tc.wantLimit, tc.wantOwn)
				}
				_, err = l.Reserve(ctx, quota.Reservation{EntryID: "e", GroupID: "g", Size: tc.size, ExpiresAt: t0.Add(24 * time.Hour)})
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("Reserve() error = %v, want %v", err, tc.wantErr)
				}
				// Another group keeps the default.
				if _, err := l.Reserve(ctx, quota.Reservation{EntryID: "o", GroupID: "other", Size: 150, ExpiresAt: t0.Add(24 * time.Hour)}); !errors.Is(err, quota.ErrTooLarge) {
					t.Errorf("Reserve(other group) error = %v, want %v", err, quota.ErrTooLarge)
				}
			})
		})
	}
}

// TestLoweredLimitEvictsOnNextReserve: lowering a limit below what the group
// holds evicts nothing until the group's next upload, which then evicts as
// many of its oldest entries as it takes to fit (plan D5).
func TestLoweredLimitEvictsOnNextReserve(t *testing.T) {
	t.Parallel()
	forEachLedger(t, quota.Limits{GroupBytes: 1000}, func(t *testing.T, l quota.Ledger, _ *clock) {
		ctx := context.Background()
		put(t, l, "a", "g", 300, 0)
		put(t, l, "b", "g", 300, time.Minute)
		put(t, l, "c", "g", 300, 2*time.Minute)

		if err := l.SetGroupLimit(ctx, "g", 400, true); err != nil {
			t.Fatalf("SetGroupLimit() error = %v", err)
		}
		wantUsage(t, l, "g", 900, 3)

		victims := put(t, l, "d", "g", 100, 3*time.Minute)
		if got, want := victimIDs(victims), []string{"a", "b"}; !slices.Equal(got, want) {
			t.Errorf("victims = %v, want %v", got, want)
		}
		wantUsage(t, l, "g", 400, 2)

		// Dropping the override restores the default.
		if err := l.SetGroupLimit(ctx, "g", 0, false); err != nil {
			t.Fatalf("SetGroupLimit(unset) error = %v", err)
		}
		if got := put(t, l, "e", "g", 500, 4*time.Minute); len(got) != 0 {
			t.Errorf("victims after unset = %v, want none", victimIDs(got))
		}
	})
}

func TestForgetGroup(t *testing.T) {
	t.Parallel()
	forEachLedger(t, quota.Limits{}, func(t *testing.T, l quota.Ledger, _ *clock) {
		ctx := context.Background()
		put(t, l, "a", "g", 10, 0)
		put(t, l, "b", "g", 20, time.Minute)
		put(t, l, "c", "other", 30, 0)
		if _, err := l.Reserve(ctx, quota.Reservation{EntryID: "p", GroupID: "g", Size: 5, ExpiresAt: t0.Add(24 * time.Hour)}); err != nil {
			t.Fatalf("Reserve(pending) error = %v", err)
		}

		n, err := l.ForgetGroup(ctx, "g")
		if err != nil {
			t.Fatalf("ForgetGroup() error = %v", err)
		}
		if n != 3 {
			t.Errorf("ForgetGroup() = %d, want 3", n)
		}
		wantUsage(t, l, "g", 0, 0)
		wantUsage(t, l, "other", 30, 1)
		wantUsage(t, l, "", 30, 1)
	})
}

// TestRebuildKeepsGroupLimits: a group's own limit is configuration, so
// `tpp gc --rebuild-accounting` must not wipe it with the bookkeeping.
func TestRebuildKeepsGroupLimits(t *testing.T) {
	t.Parallel()
	forEachLedger(t, quota.Limits{GroupBytes: 100}, func(t *testing.T, l quota.Ledger, _ *clock) {
		ctx := context.Background()
		if err := l.SetGroupLimit(ctx, "g", 500, true); err != nil {
			t.Fatalf("SetGroupLimit() error = %v", err)
		}
		if err := l.Rebuild(ctx, nil); err != nil {
			t.Fatalf("Rebuild() error = %v", err)
		}
		limit, own, err := l.GroupLimit(ctx, "g")
		if err != nil || limit != 500 || !own {
			t.Errorf("GroupLimit() after Rebuild = %d, %v, %v; want 500, true, nil", limit, own, err)
		}
	})
}
