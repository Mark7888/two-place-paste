package service

import (
	"context"
	"testing"

	"github.com/Mark7888/two-place-paste/desktop/internal/config"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
)

// TestSetupDoneIsSavedWithTheWizardChoices checks the one patch the first-run
// wizard sends: its choices and setup_done land together, and setup_done can
// be cleared again for "Run setup again".
func TestSetupDoneIsSavedWithTheWizardChoices(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		patch localui.SettingsPatch
		want  bool
	}{
		{
			name: "finished",
			patch: localui.SettingsPatch{
				AutoUpdate: boolPtr(true),
				DeviceName: strPtr("  work laptop "),
				SetupDone:  boolPtr(true),
			},
			want: true,
		},
		{name: "asked again", patch: localui.SettingsPatch{SetupDone: boolPtr(false)}, want: false},
		{name: "untouched", patch: localui.SettingsPatch{AutoUpdate: boolPtr(false)}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			ctx := context.Background()

			if before := f.svc.Settings(ctx); before.SetupDone {
				t.Fatalf("SetupDone of a fresh install = true, want false")
			}

			view, err := f.svc.UpdateSettings(ctx, tt.patch)
			if err != nil {
				t.Fatalf("UpdateSettings() error = %v", err)
			}
			if view.SetupDone != tt.want {
				t.Errorf("view SetupDone = %v, want %v", view.SetupDone, tt.want)
			}
			if tt.patch.DeviceName != nil {
				if view.DeviceName != "work laptop" {
					t.Errorf("view DeviceName = %q, want %q", view.DeviceName, "work laptop")
				}
				f.relay.mu.Lock()
				got := f.relay.name
				f.relay.mu.Unlock()
				if got != "work laptop" {
					t.Errorf("client device name = %q, want %q", got, "work laptop")
				}
			}
			saved, err := config.Load(f.svc.configDir)
			if err != nil {
				t.Fatalf("config.Load() error = %v", err)
			}
			if saved.SetupDone != tt.want {
				t.Errorf("saved SetupDone = %v, want %v", saved.SetupDone, tt.want)
			}
		})
	}
}
