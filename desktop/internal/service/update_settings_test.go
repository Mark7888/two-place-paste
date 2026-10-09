package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Mark7888/two-place-paste/desktop/internal/config"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
)

// recordingPreferences is an updater that only remembers what it was told.
type recordingPreferences struct {
	mu   sync.Mutex
	seen []config.Settings
}

func (r *recordingPreferences) SetPreferences(s config.Settings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, s)
}

func (r *recordingPreferences) last() (config.Settings, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.seen) == 0 {
		return config.Settings{}, false
	}
	return r.seen[len(r.seen)-1], true
}

func strPtr(s string) *string { return &s }

func TestUpdateSettingsReachTheUpdater(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	prefs := &recordingPreferences{}
	f.svc.updates = prefs
	ctx := context.Background()

	view, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{
		UpdateChannel: strPtr("beta"),
		AutoUpdate:    boolPtr(true),
	})
	if err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if view.UpdateChannel != "beta" || !view.AutoUpdate {
		t.Fatalf("UpdateSettings() = %+v, want beta with auto-update", view)
	}
	got, ok := prefs.last()
	if !ok || got.Channel() != "beta" || !got.AutoUpdate {
		t.Fatalf("the updater was told %+v (told: %v)", got, ok)
	}
	saved, err := config.Load(f.svc.configDir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.UpdateChannel != "beta" || !saved.AutoUpdate {
		t.Fatalf("saved %+v", saved)
	}
}

func TestANightlyCommitIsNormalisedAndChecked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	full := strings.Repeat("ab", 20)
	view, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{
		UpdateChannel: strPtr("nightly"),
		NightlyCommit: strPtr("  " + strings.ToUpper(full) + " "),
	})
	if err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if view.NightlyCommit != full {
		t.Fatalf("NightlyCommit = %q, want %q", view.NightlyCommit, full)
	}

	for _, bad := range []localui.SettingsPatch{
		{UpdateChannel: strPtr("canary")},
		{NightlyCommit: strPtr("not-a-hash")},
	} {
		if _, err := f.svc.UpdateSettings(ctx, bad); !isStatus(err, 400) {
			t.Errorf("UpdateSettings(%+v) error = %v, want a 400", bad, err)
		}
	}
	saved, err := config.Load(f.svc.configDir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Channel() != "nightly" || saved.NightlyCommit != full {
		t.Fatalf("a refused change was saved: %+v", saved)
	}
}

func isStatus(err error, code int) bool {
	var se *localui.StatusError
	return errors.As(err, &se) && se.Code == code
}

// resolvingPreferences is an updater that can expand a short hash.
type resolvingPreferences struct {
	recordingPreferences
	full string
}

func (r *resolvingPreferences) ResolveCommit(_ context.Context, hash string) (string, error) {
	if !strings.HasPrefix(r.full, hash) {
		return "", localui.Errorf(400, nil, "no commit %s", hash)
	}
	return r.full, nil
}

func TestAShortNightlyCommitIsResolved(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	full := strings.Repeat("c0", 20)
	f.svc.updates = &resolvingPreferences{full: full}
	ctx := context.Background()

	view, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{NightlyCommit: strPtr("C0C0C0C")})
	if err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if view.NightlyCommit != full {
		t.Fatalf("NightlyCommit = %q, want the resolved %q", view.NightlyCommit, full)
	}
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{NightlyCommit: strPtr("abcdef1")}); !isStatus(err, 400) {
		t.Fatalf("an unknown short hash: %v, want a 400", err)
	}
}

func TestAShortCommitNeedsAResolver(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.svc.updates = &recordingPreferences{}
	if _, err := f.svc.UpdateSettings(context.Background(), localui.SettingsPatch{NightlyCommit: strPtr("abcdef1")}); !isStatus(err, 400) {
		t.Fatalf("UpdateSettings() = %v, want a 400", err)
	}
}
