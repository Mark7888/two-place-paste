package sensitive

import (
	"errors"
	"strings"
	"testing"

	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
)

func text(s string) clipboard.Content {
	return clipboard.Content{ContentType: clipboard.TypeText, Body: []byte(s)}
}

func file(name string) clipboard.Content {
	return clipboard.Content{ContentType: clipboard.TypeOctetStream, Filename: name, Body: []byte("x")}
}

func testSalt(t *testing.T) []byte {
	t.Helper()
	s, err := NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	b, err := DecodeSalt(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFilterKinds(t *testing.T) {
	t.Parallel()
	salt := testSalt(t)
	exact := HashExact(salt, "hunter2")
	tests := []struct {
		name    string
		f       Filter
		content clipboard.Content
		want    bool
	}{
		{"contains", Filter{Kind: KindContains, Value: "secret"}, text("a secret here"), true},
		{"contains, case differs", Filter{Kind: KindContains, Value: "SECRET"}, text("a secret here"), false},
		{"contains, case-insensitive", Filter{Kind: KindContains, Value: "SECRET", CaseInsensitive: true}, text("a secret here"), true},
		{"contains, absent", Filter{Kind: KindContains, Value: "secret"}, text("nothing"), false},
		{"prefix", Filter{Kind: KindPrefix, Value: "pw:"}, text("pw:abc"), true},
		{"prefix, trimmed", Filter{Kind: KindPrefix, Value: "pw:"}, text("  \n pw:abc"), true},
		{"prefix, not at start", Filter{Kind: KindPrefix, Value: "pw:"}, text("x pw:abc"), false},
		{"prefix, second line", Filter{Kind: KindPrefix, Value: "pw:"}, text("x\npw:abc"), false},
		{"prefix, case-insensitive", Filter{Kind: KindPrefix, Value: "PW:", CaseInsensitive: true}, text(" pw:abc"), true},
		{"prefix, case-insensitive miss", Filter{Kind: KindPrefix, Value: "PW:", CaseInsensitive: true}, text("x pw:abc"), false},
		{"suffix", Filter{Kind: KindSuffix, Value: "@corp"}, text("me@corp\n"), true},
		{"suffix, miss", Filter{Kind: KindSuffix, Value: "@corp"}, text("me@corp.com"), false},
		{"suffix, case-insensitive", Filter{Kind: KindSuffix, Value: "@CORP", CaseInsensitive: true}, text(strings.Repeat("x", 10000) + "@corp "), true},
		{"suffix, case-insensitive, Kelvin sign", Filter{Kind: KindSuffix, Value: "k", CaseInsensitive: true}, text("okK"), true},
		{"suffix, case-insensitive miss", Filter{Kind: KindSuffix, Value: "@CORP", CaseInsensitive: true}, text("@corp x"), false},
		{"regex", Filter{Kind: KindRegex, Value: `^\d{6}$`}, text("123456"), true},
		{"regex, miss", Filter{Kind: KindRegex, Value: `^\d{6}$`}, text("1234567"), false},
		{"regex, multiline", Filter{Kind: KindRegex, Value: `(?m)^token=`}, text("a\ntoken=1"), true},
		{"exact", Filter{Kind: KindExact, Hash: exact}, text("hunter2"), true},
		{"exact, trimmed", Filter{Kind: KindExact, Hash: exact}, text(" hunter2\n"), true},
		{"exact, case differs", Filter{Kind: KindExact, Hash: exact}, text("Hunter2"), false},
		{"exact, longer", Filter{Kind: KindExact, Hash: exact}, text("hunter22"), false},
		{"text filter ignores files", Filter{Kind: KindContains, Value: "x"}, file("x.txt"), false},
		{"filename suffix", Filter{Kind: KindSuffix, Target: TargetFilename, Value: ".pem"}, file("server.pem"), true},
		{"filename exact name", Filter{Kind: KindRegex, Target: TargetFilename, Value: `^id_(rsa|ed25519)$`}, file("id_rsa"), true},
		{"filename filter ignores text", Filter{Kind: KindContains, Target: TargetFilename, Value: ".env"}, text("a .env file"), false},
		{"images are not scanned", Filter{Kind: KindContains, Value: "PNG"}, clipboard.Content{ContentType: clipboard.TypeImagePNG, Body: []byte("PNG")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := tt.f
			f.ID, f.Enabled = "f1", true
			if f.Target == "" {
				f.Target = TargetText
			}
			c, err := New(Config{Filters: []Filter{f}, Salt: salt})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			v := c.Classify(tt.content)
			if v.Sensitive != tt.want {
				t.Fatalf("Sensitive = %v, want %v", v.Sensitive, tt.want)
			}
			if v.Sensitive && (v.Source != SourceFilter || v.Rule != "f1") {
				t.Errorf("verdict = %+v, want source %q rule f1", v, SourceFilter)
			}
		})
	}
}

func TestDisabledFilterIsSkipped(t *testing.T) {
	t.Parallel()
	c, err := New(Config{Filters: []Filter{{ID: "a", Kind: KindContains, Target: TargetText, Value: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if v := c.Classify(text("x")); v.Sensitive {
		t.Errorf("a disabled filter matched: %+v", v)
	}
}

func TestOrderOSThenDetectorThenFilter(t *testing.T) {
	t.Parallel()
	key := "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n"
	cfg := Config{
		DetectOS:  true,
		Detectors: []string{"private_keys"},
		Filters:   []Filter{{ID: "f", Kind: KindContains, Target: TargetText, Value: "abc", Enabled: true, Label: "Mine"}},
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	concealed := text(key)
	concealed.Concealed = "Marked private by the app that copied it"
	tests := []struct {
		name    string
		content clipboard.Content
		want    Source
		reason  string
	}{
		{"os first", concealed, SourceOS, concealed.Concealed},
		{"detector second", text(key), SourceDetector, "Private keys"},
		{"filter last", text("abc"), SourceFilter, "Mine"},
	}
	for _, tt := range tests {
		v := c.Classify(tt.content)
		if v.Source != tt.want || v.Reason != tt.reason {
			t.Errorf("%s: verdict = %+v, want source %q reason %q", tt.name, v, tt.want, tt.reason)
		}
	}
	if got := len(c.Matches(concealed)); got != 3 {
		t.Errorf("len(Matches) = %d, want 3", got)
	}

	cfg.DetectOS = false
	off, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if v := off.Classify(concealed); v.Source != SourceDetector {
		t.Errorf("with OS detection off, verdict = %+v, want the detector", v)
	}
}

func TestNilAndEmptyClassifier(t *testing.T) {
	t.Parallel()
	var c *Classifier
	if v := c.Classify(text("x")); v.Sensitive {
		t.Error("a nil classifier marked something")
	}
	e, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !e.Empty() {
		t.Error("Empty() = false for an empty config")
	}
}

func TestValidateRefuses(t *testing.T) {
	t.Parallel()
	salt := testSalt(t)
	good := Filter{ID: "a", Kind: KindContains, Target: TargetText, Value: "x"}
	tests := map[string]Filter{
		"no id":           {Kind: KindContains, Target: TargetText, Value: "x"},
		"unknown kind":    {ID: "a", Kind: "glob", Target: TargetText, Value: "x"},
		"unknown target":  {ID: "a", Kind: KindContains, Target: "body", Value: "x"},
		"empty value":     {ID: "a", Kind: KindContains, Target: TargetText},
		"bad regex":       {ID: "a", Kind: KindRegex, Target: TargetText, Value: "(secret"},
		"long value":      {ID: "a", Kind: KindContains, Target: TargetText, Value: strings.Repeat("x", MaxValueBytes+1)},
		"long label":      {ID: "a", Kind: KindContains, Target: TargetText, Value: "x", Label: strings.Repeat("é", MaxLabelRunes+1)},
		"exact w/ value":  {ID: "a", Kind: KindExact, Target: TargetText, Value: "pw", Hash: HashExact(salt, "pw")},
		"exact no hash":   {ID: "a", Kind: KindExact, Target: TargetText},
		"exact filename":  {ID: "a", Kind: KindExact, Target: TargetFilename, Hash: HashExact(salt, "pw")},
		"hash on contain": {ID: "a", Kind: KindContains, Target: TargetText, Value: "x", Hash: HashExact(salt, "pw")},
	}
	for name, f := range tests {
		err := f.Validate()
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Validate() = %v, want ErrInvalid", name, err)
		}
	}
	if err := good.Validate(); err != nil {
		t.Errorf("Validate(good) = %v, want nil", err)
	}
	if err := ValidateList([]Filter{good, good}); !errors.Is(err, ErrInvalid) {
		t.Errorf("ValidateList(duplicate ids) = %v, want ErrInvalid", err)
	}
}

// A filter's value may be the secret it protects, so no validation error may
// repeat it.
func TestValidateNeverQuotesTheValue(t *testing.T) {
	t.Parallel()
	const secret = "s3cr3t-value"
	for _, f := range []Filter{
		{ID: "a", Kind: KindRegex, Target: TargetText, Value: "(" + secret},
		{ID: "a", Kind: KindRegex, Target: TargetText, Value: secret + "[z-a]"},
		{ID: "a", Kind: KindExact, Target: TargetText, Value: secret},
	} {
		err := f.Validate()
		if err == nil {
			t.Fatalf("Validate accepted %q", f.Kind)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error %q repeats the value", err)
		}
	}
}

func TestExactNeedsSalt(t *testing.T) {
	t.Parallel()
	salt := testSalt(t)
	f := Filter{ID: "a", Kind: KindExact, Target: TargetText, Hash: HashExact(salt, "pw"), Enabled: true}
	if _, err := New(Config{Filters: []Filter{f}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("New without a salt = %v, want ErrInvalid", err)
	}
}

func TestHashExact(t *testing.T) {
	t.Parallel()
	salt := testSalt(t)
	h := HashExact(salt, "pw")
	if !validHash(h) {
		t.Fatalf("HashExact output %q is not a valid hash", h)
	}
	if strings.Contains(h, "pw") && !strings.HasPrefix(h, hashPrefix) {
		t.Errorf("hash %q contains the secret", h)
	}
	if got := HashExact(salt, " pw\t"); got != h {
		t.Errorf("HashExact trims: got %q, want %q", got, h)
	}
	if got := HashExact(testSalt(t), "pw"); got == h {
		t.Error("two salts gave the same hash")
	}
	for _, s := range []string{"", "   ", strings.Repeat("x", MaxExactBytes+1)} {
		if got := HashExact(salt, s); got != "" {
			t.Errorf("HashExact(%d bytes) = %q, want empty", len(s), got)
		}
	}
	if _, err := DecodeSalt("short"); !errors.Is(err, ErrInvalid) {
		t.Errorf("DecodeSalt(short) = %v, want ErrInvalid", err)
	}
}

func TestNewID(t *testing.T) {
	t.Parallel()
	a, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewID()
	if a == "" || a == b || len(a) > maxIDBytes {
		t.Errorf("NewID = %q, %q; want two distinct short ids", a, b)
	}
}
