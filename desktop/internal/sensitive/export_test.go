package sensitive

import (
	"errors"
	"strings"
	"testing"

	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
)

func TestExportRoundTrip(t *testing.T) {
	t.Parallel()
	salt := testSalt(t)
	filters := []Filter{
		{ID: "a", Kind: KindContains, Target: TargetText, Value: "vpn", CaseInsensitive: true, Enabled: true, Label: "VPN"},
		{ID: "b", Kind: KindExact, Target: TargetText, Hash: HashExact(salt, "pw"), Enabled: true},
		{ID: "c", Kind: KindSuffix, Target: TargetFilename, Value: ".pem"},
	}
	doc, skipped := BuildExport(filters, []string{"jwt"})
	if skipped != 1 || len(doc.Filters) != 2 {
		t.Fatalf("BuildExport = %d filters, %d skipped; want 2 and the exact match skipped", len(doc.Filters), skipped)
	}
	if len(doc.Detectors) != len(Detectors()) || !doc.Detectors["jwt"] || doc.Detectors["private_keys"] {
		t.Errorf("detectors = %v, want every detector with only jwt on", doc.Detectors)
	}
	text, err := doc.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, HashExact(salt, "pw")) {
		t.Error("the export carries an exact match's hash")
	}
	got, err := ParseExport("\n" + text + "\n")
	if err != nil {
		t.Fatalf("ParseExport = %v", err)
	}
	if got.Version != ExportVersion || len(got.Filters) != 2 || got.Filters[0] != doc.Filters[0] {
		t.Errorf("ParseExport = %+v, want the export back", got)
	}

	// One filter's export says nothing about detectors.
	single, _ := BuildExport(filters[:1], nil)
	if single.Detectors != nil {
		t.Errorf("a single filter's export carries detectors: %v", single.Detectors)
	}
}

func TestParseExportRefuses(t *testing.T) {
	t.Parallel()
	for name, text := range map[string]string{
		"plain text":   "hello",
		"other json":   `{"format":"something-else","version":1,"filters":[]}`,
		"broken":       `{"format":"twoplacepaste-filters", "version":1, "filters":[`,
		"no version":   `{"format":"twoplacepaste-filters","filters":[]}`,
		"too large":    `{"format":"twoplacepaste-filters","version":1,"x":"` + strings.Repeat("x", MaxExportBytes) + `"}`,
		"mentions it":  `the format is called "twoplacepaste-filters"`,
		"array of one": `[{"format":"twoplacepaste-filters","version":1}]`,
	} {
		if _, err := ParseExport(text); !errors.Is(err, ErrNotExport) {
			t.Errorf("%s: ParseExport = %v, want ErrNotExport", name, err)
		}
	}
	// A newer version is read; what it adds is ignored here.
	if _, err := ParseExport(`{"format":"twoplacepaste-filters","version":7,"filters":[],"future":true}`); err != nil {
		t.Errorf("ParseExport(newer version) = %v, want it read", err)
	}
}

func TestPlanImport(t *testing.T) {
	t.Parallel()
	salt := testSalt(t)
	current := []Filter{
		{ID: "same", Kind: KindContains, Target: TargetText, Value: "vpn", CaseInsensitive: true, Enabled: false, Label: "VPN"},
		{ID: "edited", Kind: KindPrefix, Target: TargetText, Value: "pw:", Enabled: true},
		{ID: "local", Kind: KindRegex, Target: TargetText, Value: `^\d{6}$`, Enabled: true},
		{ID: "exact", Kind: KindExact, Target: TargetText, Hash: HashExact(salt, "x"), Enabled: true},
	}
	doc := Export{Format: ExportFormat, Version: 1, Filters: []ExportFilter{
		{ID: "same", Kind: KindContains, Target: TargetText, Value: "vpn", CaseInsensitive: true, Enabled: true, Label: "VPN"},
		{ID: "edited", Kind: KindPrefix, Target: TargetText, Value: "pass:", Enabled: true, Label: "Passwords"},
		{ID: "other-id", Kind: KindRegex, Target: TargetText, Value: `^\d{6}$`, Enabled: true},
		{ID: "fresh", Kind: KindSuffix, Target: TargetFilename, Value: ".key", Enabled: true},
		{ID: "bad", Kind: KindRegex, Target: TargetText, Value: "(open-secret", Enabled: true},
		{ID: "future", Kind: "glob", Target: TargetText, Value: "*.x", Enabled: true},
		{ID: "hashed", Kind: KindExact, Target: TargetText, Enabled: true},
		{ID: "fresh", Kind: KindContains, Target: TargetText, Value: "again", Enabled: true},
	}, Detectors: map[string]bool{"private_keys": false, "jwt": true, "iban": false, "quantum_keys": true}}

	plan := PlanImport(doc, current, []string{"private_keys"})
	want := []struct {
		status   ImportStatus
		selected bool
		existing string
	}{
		{ImportDuplicate, false, "same"},
		{ImportUpdate, false, "edited"},
		{ImportDuplicate, false, "local"},
		{ImportNew, true, ""},
		{ImportInvalid, false, ""},
		{ImportInvalid, false, ""},
		{ImportInvalid, false, ""},
		{ImportInvalid, false, ""},
	}
	if len(plan.Filters) != len(want) {
		t.Fatalf("plan has %d filter rows, want %d", len(plan.Filters), len(want))
	}
	for i, w := range want {
		r := plan.Filters[i]
		if r.Status != w.status || r.Selected != w.selected || r.ExistingID != w.existing {
			t.Errorf("row %d (%s) = %s selected=%v existing=%q, want %s selected=%v existing=%q",
				i, r.Filter.ID, r.Status, r.Selected, r.ExistingID, w.status, w.selected, w.existing)
		}
		if strings.Contains(r.Reason, "open-secret") {
			t.Errorf("row %d's reason repeats the value: %q", i, r.Reason)
		}
	}
	if r := plan.Filters[1].Reason; r != "changes the value, label" {
		t.Errorf("update reason = %q", r)
	}

	got := map[string]ImportDetectorRow{}
	for _, d := range plan.Detectors {
		got[d.Name] = d
	}
	for name, w := range map[string]struct {
		status   ImportStatus
		selected bool
	}{
		"private_keys": {ImportTurnOff, false},
		"jwt":          {ImportTurnOn, true},
		"iban":         {ImportUnchanged, false},
		"quantum_keys": {ImportInvalid, false},
	} {
		if d := got[name]; d.Status != w.status || d.Selected != w.selected || d.Enabled != doc.Detectors[name] {
			t.Errorf("detector %s = %s selected=%v, want %s selected=%v", name, d.Status, d.Selected, w.status, w.selected)
		}
	}
	if _, listed := got["api_tokens"]; listed {
		t.Error("a detector the export does not name was listed")
	}
}

func TestPlanImportRespectsTheCap(t *testing.T) {
	t.Parallel()
	var current []Filter
	for i := range MaxFilters - 1 {
		current = append(current, Filter{ID: string(rune('A'+i%26)) + strings.Repeat("x", i/26+1), Kind: KindContains, Target: TargetText, Value: "v" + strings.Repeat("y", i)})
	}
	doc := Export{Format: ExportFormat, Version: 1, Filters: []ExportFilter{
		{ID: "n1", Kind: KindContains, Target: TargetText, Value: "new one"},
		{ID: "n2", Kind: KindContains, Target: TargetText, Value: "new two"},
	}}
	plan := PlanImport(doc, current, nil)
	if plan.Filters[0].Status != ImportNew || plan.Filters[1].Status != ImportInvalid {
		t.Errorf("rows = %s, %s; want one new and one over the cap", plan.Filters[0].Status, plan.Filters[1].Status)
	}
}

func TestClassifyRecognisesAnExport(t *testing.T) {
	t.Parallel()
	doc, _ := BuildExport([]Filter{{ID: "a", Kind: KindContains, Target: TargetText, Value: "x"}}, nil)
	text, _ := doc.Encode()
	c, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	// Recognised with every setting off, and before the OS marker the app
	// itself puts on its export.
	content := clipboard.Content{ContentType: clipboard.TypeText, Body: []byte(text), Concealed: clipboard.ReasonConcealed}
	if v := c.Classify(content); v.Source != SourceExport || v.Reason != ReasonExport {
		t.Errorf("Classify(export) = %+v, want an export", v)
	}
	if v := c.Classify(clipboard.Content{ContentType: clipboard.TypeText, Body: []byte(`{"format":"other"}`)}); v.Sensitive {
		t.Errorf("Classify(other json) = %+v, want nothing", v)
	}
}

// An export an import would refuse is not made: 200 filters whose values are
// control characters escape to well past what ParseExport reads.
func TestEncodeRefusesAnUnimportableExport(t *testing.T) {
	t.Parallel()
	var filters []Filter
	for i := range MaxFilters {
		filters = append(filters, Filter{
			ID:   strings.Repeat("i", 1+i%60) + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Kind: KindContains, Target: TargetText, Value: strings.Repeat("\x01", MaxValueBytes),
		})
	}
	doc, _ := BuildExport(filters, nil)
	if _, err := doc.Encode(); !errors.Is(err, ErrExportTooLarge) {
		t.Fatalf("Encode(oversized) = %v, want ErrExportTooLarge", err)
	}
	small, _ := BuildExport(filters[:1], nil)
	if _, err := small.Encode(); err != nil {
		t.Errorf("Encode(one filter) = %v", err)
	}
}
