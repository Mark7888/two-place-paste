package config

import (
	"errors"
	"strings"
	"testing"
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
	if got != want {
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
