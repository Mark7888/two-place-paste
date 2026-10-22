package sensitive

import (
	"strings"
	"testing"
)

func TestDetectors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		detector string
		in       string
		want     bool
	}{
		{"private_keys", "-----BEGIN RSA PRIVATE KEY-----\nMIIE", true},
		{"private_keys", "-----BEGIN OPENSSH PRIVATE KEY-----", true},
		{"private_keys", "-----BEGIN PRIVATE KEY-----", true},
		{"private_keys", "-----BEGIN ENCRYPTED PRIVATE KEY-----", true},
		{"private_keys", "-----BEGIN PGP PRIVATE KEY BLOCK-----", true},
		{"private_keys", "-----BEGIN PUBLIC KEY-----", false},
		{"private_keys", "-----BEGIN CERTIFICATE-----", false},
		{"private_keys", "my private key is on a USB stick", false},

		{"jwt", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl", true},
		{"jwt", "eyJhbGciOiJub25lIn0.eyJzdWIiOiIxIn0.", true},
		{"jwt", "eyJ is how JSON starts in base64", false},

		{"api_tokens", "AKIAIOSFODNN7EXAMPLE", true},
		{"api_tokens", "ghp_" + strings.Repeat("a", 36), true},
		{"api_tokens", "github_pat_" + strings.Repeat("A1_", 10), true},
		{"api_tokens", "xoxb-1234567890-abc", true},
		{"api_tokens", "sk_live_" + strings.Repeat("a", 24), true},
		{"api_tokens", "sk-ant-api03-" + strings.Repeat("a", 30), true},
		{"api_tokens", "sk-proj-" + strings.Repeat("a", 30), true},
		{"api_tokens", "AIza" + strings.Repeat("b", 35), true},
		{"api_tokens", "glpat-" + strings.Repeat("c", 20), true},
		{"api_tokens", "npm_" + strings.Repeat("d", 36), true},
		{"api_tokens", "the ghp_ prefix is GitHub's", false},
		{"api_tokens", "AKIA is an AWS prefix", false},

		{"payment_cards", "4111 1111 1111 1111", true},
		{"payment_cards", "card: 5500-0000-0000-0004.", true},
		{"payment_cards", "378282246310005", true},
		{"payment_cards", "4111 1111 1111 1112", false},        // bad checksum
		{"payment_cards", "order 1234567890123", false},        // fails Luhn
		{"payment_cards", "41111111111111111111111111", false}, // too long to be a card
		{"payment_cards", "call 555-1234", false},
		{"payment_cards", "id4111111111111111", false}, // part of an identifier
		{"payment_cards", "4111111111111111x", false},
		{"payment_cards", "4111  1111 1111 1111", false},         // a double space splits the run
		{"api_tokens", "xghp_" + strings.Repeat("a", 36), false}, // glued to a longer word
		{"api_tokens", "AKIAIOSFODNN7EXAMPLEX", false},

		{"iban", "GB82 WEST 1234 5698 7654 32", true},
		{"iban", "DE89370400440532013000", true},
		{"iban", "IBAN: HU42117730161111101800000000", true},
		{"iban", "GB82 WEST 1234 5698 7654 33", false}, // bad checksum
		{"iban", "AB12 not an iban", false},
		{"iban", "DE89 3704 0044 0532 0130 00 IBAN", true}, // a trailing word does not hide it
		{"iban", "xDE89370400440532013000", false},         // part of a longer word
	}
	for _, tt := range tests {
		d, ok := LookupDetector(tt.detector)
		if !ok {
			t.Fatalf("no detector %q", tt.detector)
		}
		if got := d.Match(tt.in); got != tt.want {
			t.Errorf("%s(%q) = %v, want %v", tt.detector, tt.in, got, tt.want)
		}
	}
}

func TestDetectorCatalogue(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, d := range Detectors() {
		if d.Name == "" || d.Title == "" || d.Description == "" || d.match == nil {
			t.Errorf("detector %+v is incomplete", d)
		}
		if seen[d.Name] {
			t.Errorf("duplicate detector %q", d.Name)
		}
		seen[d.Name] = true
	}
	if got, want := strings.Join(DefaultDetectors(), ","), "private_keys"; got != want {
		t.Errorf("DefaultDetectors = %q, want %q", got, want)
	}
}

func TestUnknownDetectorIsIgnored(t *testing.T) {
	t.Parallel()
	c, err := New(Config{Detectors: []string{"from_a_newer_version"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !c.Empty() {
		t.Error("an unknown detector was kept")
	}
}

// Every rule is linear, so a large text costs time in proportion to its
// size and never more. The benchmark pins what a full configuration costs on
// a 50 MB text, the default per-entry cap.
func BenchmarkClassifyLargeText(b *testing.B) {
	var names []string
	for _, d := range Detectors() {
		names = append(names, d.Name)
	}
	c, err := New(Config{
		DetectOS:  true,
		Detectors: names,
		Filters: []Filter{
			{ID: "1", Kind: KindContains, Target: TargetText, Value: "needle", CaseInsensitive: true, Enabled: true},
			{ID: "2", Kind: KindPrefix, Target: TargetText, Value: "pw:", CaseInsensitive: true, Enabled: true},
			{ID: "3", Kind: KindSuffix, Target: TargetText, Value: "@corp", CaseInsensitive: true, Enabled: true},
			{ID: "4", Kind: KindRegex, Target: TargetText, Value: `password\s*=`, Enabled: true},
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	content := text(strings.Repeat("The quick brown fox jumps over 13 lazy dogs.\n", 50<<20/45))
	b.SetBytes(int64(len(content.Body)))
	for b.Loop() {
		if v := c.Classify(content); v.Sensitive {
			b.Fatalf("matched: %+v", v)
		}
	}
}
