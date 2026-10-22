package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Mark7888/two-place-paste/desktop/internal/sensitive"
)

func TestUpdateSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Settings{
		UpdateChannel: ChannelNightly,
		AutoUpdate:    true,
		NightlyCommit: strings.Repeat("ab", 20),
	}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}
}

func TestChannelDefaultsToStable(t *testing.T) {
	if got := (Settings{}).Channel(); got != ChannelStable {
		t.Fatalf("Channel() = %q, want %q", got, ChannelStable)
	}
}

func TestValidateRejectsBadUpdateSettings(t *testing.T) {
	for name, s := range map[string]Settings{
		"unknown channel":  {UpdateChannel: "canary"},
		"short commit":     {NightlyCommit: "abc1234"},
		"uppercase commit": {NightlyCommit: strings.Repeat("AB", 20)},
		"non-hex commit":   {NightlyCommit: strings.Repeat("zz", 20)},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want an error", name)
		}
	}
	if err := Save(t.TempDir(), Settings{UpdateChannel: "canary"}); err == nil {
		t.Error("Save accepted an unknown channel")
	}
}

func TestLoadMissingIsNotFound(t *testing.T) {
	if _, err := Load(t.TempDir()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load = %v, want ErrNotFound", err)
	}
}

func TestSensitiveDefaults(t *testing.T) {
	t.Parallel()
	var s Settings
	if !s.SensitiveDetection() {
		t.Error("SensitiveDetection() = false for an absent setting, want true")
	}
	if got, want := strings.Join(s.EnabledDetectors(), ","), strings.Join(sensitive.DefaultDetectors(), ","); got != want {
		t.Errorf("EnabledDetectors() = %q, want the defaults %q", got, want)
	}
	off, none := false, []string{}
	s = Settings{DetectSensitive: &off, Detectors: &none}
	if s.SensitiveDetection() {
		t.Error("SensitiveDetection() = true, want false when turned off")
	}
	if got := s.EnabledDetectors(); len(got) != 0 {
		t.Errorf("EnabledDetectors() = %v, want none", got)
	}
}

func TestSensitiveSettingsRoundTrip(t *testing.T) {
	t.Parallel()
	saltText, err := sensitive.NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	salt, err := sensitive.DecodeSalt(saltText)
	if err != nil {
		t.Fatal(err)
	}
	off := false
	detectors := []string{"jwt"}
	want := Settings{
		DetectSensitive: &off,
		Detectors:       &detectors,
		FilterSalt:      saltText,
		Filters: []sensitive.Filter{
			{ID: "a", Kind: sensitive.KindContains, Target: sensitive.TargetText, Value: "x", Enabled: true, Label: "L"},
			{ID: "b", Kind: sensitive.KindExact, Target: sensitive.TargetText, Hash: sensitive.HashExact(salt, "pw")},
		},
	}
	dir := t.TempDir()
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}
}

func TestValidateRejectsBadFilters(t *testing.T) {
	t.Parallel()
	saltText, _ := sensitive.NewSalt()
	salt, _ := sensitive.DecodeSalt(saltText)
	exact := sensitive.Filter{ID: "e", Kind: sensitive.KindExact, Target: sensitive.TargetText, Hash: sensitive.HashExact(salt, "pw"), Enabled: true}
	for name, s := range map[string]Settings{
		"bad regex":         {Filters: []sensitive.Filter{{ID: "a", Kind: sensitive.KindRegex, Target: sensitive.TargetText, Value: "("}}},
		"exact, no salt":    {Filters: []sensitive.Filter{exact}},
		"malformed salt":    {Filters: []sensitive.Filter{exact}, FilterSalt: "nope"},
		"duplicate filters": {Filters: []sensitive.Filter{exact, exact}, FilterSalt: saltText},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want an error", name)
		}
	}
	if err := (Settings{Filters: []sensitive.Filter{exact}, FilterSalt: saltText}).Validate(); err != nil {
		t.Errorf("Validate(exact with salt) = %v, want nil", err)
	}
}

func TestCloneSharesNothing(t *testing.T) {
	t.Parallel()
	on, d := true, []string{"jwt"}
	a := Settings{DetectSensitive: &on, Detectors: &d, Filters: []sensitive.Filter{{ID: "a"}}}
	b := a.Clone()
	*b.DetectSensitive = false
	(*b.Detectors)[0] = "iban"
	b.Filters[0].ID = "b"
	if !*a.DetectSensitive || (*a.Detectors)[0] != "jwt" || a.Filters[0].ID != "a" {
		t.Errorf("changing the clone changed the original: %+v", a)
	}
}
