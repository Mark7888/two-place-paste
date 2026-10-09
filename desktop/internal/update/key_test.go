package update

import "testing"

// The committed key must parse, or every build ships an updater that refuses
// everything.
func TestTheEmbeddedSigningKeyParses(t *testing.T) {
	key, err := SigningKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 {
		t.Fatalf("key is %d bytes, want 32", len(key))
	}
}

func TestAssetName(t *testing.T) {
	for _, tc := range []struct {
		goos, goarch string
		rosetta      bool
		want         string
	}{
		{"darwin", "arm64", false, "TwoPlacePaste-macOS-arm64.zip"},
		{"darwin", "amd64", false, "TwoPlacePaste-macOS-x64.zip"},
		{"darwin", "amd64", true, "TwoPlacePaste-macOS-arm64.zip"},
		{"windows", "amd64", false, "TwoPlacePaste-Windows-x64.exe"},
	} {
		got, err := assetName(tc.goos, tc.goarch, tc.rosetta)
		if err != nil || got != tc.want {
			t.Errorf("assetName(%s, %s, %v) = %q, %v; want %q", tc.goos, tc.goarch, tc.rosetta, got, err, tc.want)
		}
	}
	if _, err := assetName("linux", "amd64", false); err == nil {
		t.Error("assetName(linux) succeeded; no Linux builds are published")
	}
}
