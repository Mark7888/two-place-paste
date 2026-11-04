package sensitive

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Export and import of filters (docs/internal/plans/auto-upload-filters.md
// §10): a JSON document the user copies or keeps in the store, and takes to
// another device of the group.
//
// JSON rather than base64, so that what is about to be imported can be read
// first, and a format marker so that a clipboard holding one is recognised
// without guessing.
//
// An export holds filter values in clear: a "contains" filter's value may be
// the user's own private text. It travels inside the group's encryption when
// it is saved to the store, and the service marks it private when it puts it
// on the clipboard; it is never uploaded automatically (Classify).

// ExportFormat marks a document as a filter export.
const ExportFormat = "twoplacepaste-filters"

// ExportVersion is the version this build writes. A newer one is still read:
// what it adds is skipped, field by field, and said so.
const ExportVersion = 1

// MaxExportBytes bounds an export read from the clipboard or the store: 200
// filters of a kibibyte each, escaped, with room to spare.
const MaxExportBytes = 512 << 10

// Export is the document.
type Export struct {
	Format  string `json:"format"`
	Version int    `json:"version"`

	// Detectors maps a built-in detector's name to whether it is on. Absent
	// means the export says nothing about detectors — a single filter's
	// export — which is not the same as all of them off.
	Detectors map[string]bool `json:"detectors,omitempty"`

	Filters []ExportFilter `json:"filters"`
}

// ExportFilter is one custom filter. Its ID is kept across devices, so a
// filter exported, changed elsewhere and imported back is recognised as the
// same filter.
type ExportFilter struct {
	ID              string `json:"id"`
	Kind            Kind   `json:"kind"`
	Target          Target `json:"target"`
	Value           string `json:"value"`
	CaseInsensitive bool   `json:"case_insensitive"`
	Enabled         bool   `json:"enabled"`
	Label           string `json:"label,omitempty"`
}

// Exportable reports whether a filter can be exported. An exact match keeps
// only a hash, salted for this install, which means nothing on another device.
func (f Filter) Exportable() bool { return f.Kind != KindExact }

// BuildExport makes an export of the given filters and, when detectors is not
// nil, of every built-in detector's state. Exact matches are left out and
// counted in skipped.
func BuildExport(filters []Filter, detectors []string) (doc Export, skipped int) {
	doc = Export{Format: ExportFormat, Version: ExportVersion, Filters: []ExportFilter{}}
	for _, f := range filters {
		if !f.Exportable() {
			skipped++
			continue
		}
		doc.Filters = append(doc.Filters, ExportFilter{
			ID: f.ID, Kind: f.Kind, Target: f.Target, Value: f.Value,
			CaseInsensitive: f.CaseInsensitive, Enabled: f.Enabled, Label: f.Label,
		})
	}
	if detectors != nil {
		on := map[string]bool{}
		for _, n := range detectors {
			on[n] = true
		}
		doc.Detectors = map[string]bool{}
		for _, d := range Detectors() {
			doc.Detectors[d.Name] = on[d.Name]
		}
	}
	return doc, skipped
}

// ErrExportTooLarge refuses an export larger than an import reads.
var ErrExportTooLarge = errors.New("sensitive: the export is too large to import")

// Encode renders an export as indented JSON: it is meant to be read.
//
// An export is refused past MaxExportBytes, the most ParseExport reads: one
// that could not be imported again is no use. 200 filters fit in practice,
// but a value of control characters is escaped to six bytes each, so the
// limit can be reached.
func (e Export) Encode() (string, error) {
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode the filter export: %w", err)
	}
	if len(b) > MaxExportBytes {
		return "", fmt.Errorf("%w: %d bytes, an import reads at most %d", ErrExportTooLarge, len(b), MaxExportBytes)
	}
	return string(b), nil
}

// ErrNotExport is what ParseExport answers for anything that is not one.
var ErrNotExport = errors.New("sensitive: not a filter export")

// LooksLikeExport is the cheap test Classify runs on every clipboard change:
// a JSON object that names the format. ParseExport is the real one.
func LooksLikeExport(text string) bool {
	if len(text) > MaxExportBytes {
		return false
	}
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") && strings.Contains(t, `"`+ExportFormat+`"`)
}

// ParseExport reads an export. Fields a newer version adds are ignored here;
// a newer filter kind or detector is dealt with by PlanImport.
func ParseExport(text string) (Export, error) {
	if !LooksLikeExport(text) {
		return Export{}, ErrNotExport
	}
	var e Export
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &e); err != nil || e.Format != ExportFormat || e.Version < 1 {
		return Export{}, ErrNotExport
	}
	return e, nil
}

// ImportStatus is what importing one row would do.
type ImportStatus string

// The statuses of an import row.
const (
	// ImportNew adds a filter this device does not have.
	ImportNew ImportStatus = "new"

	// ImportUpdate replaces a filter with the same ID whose content differs:
	// the same filter, changed on another device.
	ImportUpdate ImportStatus = "update"

	// ImportDuplicate is a filter this device already has, by ID or by
	// content. Nothing to do.
	ImportDuplicate ImportStatus = "duplicate"

	// ImportInvalid cannot be imported; Reason says why.
	ImportInvalid ImportStatus = "invalid"

	// ImportTurnOn and ImportTurnOff change a built-in detector.
	ImportTurnOn  ImportStatus = "turn_on"
	ImportTurnOff ImportStatus = "turn_off"

	// ImportUnchanged is a detector already in the state the export gives.
	ImportUnchanged ImportStatus = "unchanged"
)

// ImportFilterRow is one custom filter of an import.
type ImportFilterRow struct {
	Filter ExportFilter
	Status ImportStatus

	// Selected is whether the row starts ticked: new filters are, updates
	// are not (they overwrite something), and the rest cannot be ticked.
	Selected bool

	// Reason says why an invalid row cannot be imported, or, for an
	// update, what it changes. Never the value.
	Reason string

	// ExistingID is the filter on this device a duplicate or update matches.
	ExistingID string
}

// ImportDetectorRow is one built-in detector of an import.
type ImportDetectorRow struct {
	Name   string
	Title  string // empty for a detector this build does not have
	Status ImportStatus
	// Enabled is the detector's state in the export.
	Enabled bool
	// Selected: turning one on starts ticked, turning one off does not. An
	// import should not quietly lower the protection.
	Selected bool
	Reason   string
}

// ImportPlan is what an import would do against the current filters.
type ImportPlan struct {
	Filters   []ImportFilterRow
	Detectors []ImportDetectorRow
}

// sameContent compares what a filter matches and how it is shown. The
// enabled switch is left out: importing a filter that is on, onto a device
// where it is off, is not a new filter.
func sameContent(a ExportFilter, b Filter) bool {
	return a.Kind == b.Kind && a.Target == b.Target && a.Value == b.Value &&
		a.CaseInsensitive == b.CaseInsensitive && a.Label == b.Label
}

// sameMatch compares only what a filter matches: two filters that match the
// same thing are duplicates whatever their labels.
func sameMatch(a ExportFilter, b Filter) bool {
	return a.Kind == b.Kind && a.Target == b.Target && a.Value == b.Value && a.CaseInsensitive == b.CaseInsensitive
}

// PlanImport compares an export with the current filters and detectors, and
// says what importing each row would do. It changes nothing; the caller
// applies the rows the user ticks.
func PlanImport(e Export, current []Filter, enabledDetectors []string) ImportPlan {
	plan := ImportPlan{Filters: []ImportFilterRow{}, Detectors: []ImportDetectorRow{}}
	byID := map[string]Filter{}
	for _, f := range current {
		byID[f.ID] = f
	}
	room := MaxFilters - len(current)
	seenIDs := map[string]bool{}

	for _, ef := range e.Filters {
		row := ImportFilterRow{Filter: ef}
		f := Filter{
			ID: ef.ID, Kind: ef.Kind, Target: ef.Target, Value: ef.Value,
			CaseInsensitive: ef.CaseInsensitive, Enabled: ef.Enabled, Label: ef.Label,
		}
		switch {
		case ef.Kind == KindExact:
			row.Status, row.Reason = ImportInvalid, "an exact match keeps only a hash and cannot be imported"
		case seenIDs[ef.ID]:
			row.Status, row.Reason = ImportInvalid, "listed twice in this export"
		default:
			if err := f.Validate(); err != nil {
				row.Status, row.Reason = ImportInvalid, importReason(err)
			}
		}
		seenIDs[ef.ID] = true
		if row.Status == ImportInvalid {
			plan.Filters = append(plan.Filters, row)
			continue
		}

		if have, ok := byID[ef.ID]; ok {
			row.ExistingID = have.ID
			if sameContent(ef, have) {
				row.Status = ImportDuplicate
			} else {
				row.Status, row.Reason = ImportUpdate, describeUpdate(ef, have)
			}
			plan.Filters = append(plan.Filters, row)
			continue
		}
		for _, have := range current {
			if sameMatch(ef, have) {
				row.Status, row.ExistingID = ImportDuplicate, have.ID
				break
			}
		}
		if row.Status == "" {
			if room > 0 {
				row.Status, row.Selected = ImportNew, true
				room--
			} else {
				row.Status, row.Reason = ImportInvalid, fmt.Sprintf("this device already has the most filters it can keep (%d)", MaxFilters)
			}
		}
		plan.Filters = append(plan.Filters, row)
	}

	if e.Detectors != nil {
		on := map[string]bool{}
		for _, n := range enabledDetectors {
			on[n] = true
		}
		// In this build's order, then any it does not know, in name order.
		known := map[string]bool{}
		for _, d := range Detectors() {
			known[d.Name] = true
			want, listed := e.Detectors[d.Name]
			if !listed {
				continue
			}
			row := ImportDetectorRow{Name: d.Name, Title: d.Title, Enabled: want}
			switch {
			case want == on[d.Name]:
				row.Status = ImportUnchanged
			case want:
				row.Status, row.Selected = ImportTurnOn, true
			default:
				row.Status = ImportTurnOff
			}
			plan.Detectors = append(plan.Detectors, row)
		}
		var unknown []string
		for name := range e.Detectors {
			if !known[name] {
				unknown = append(unknown, name)
			}
		}
		slices.Sort(unknown)
		for _, name := range unknown {
			plan.Detectors = append(plan.Detectors, ImportDetectorRow{
				Name: name, Status: ImportInvalid, Enabled: e.Detectors[name], Reason: "a built-in filter this version does not have",
			})
		}
	}
	return plan
}

// importReason is a validation failure as the import dialog says it.
func importReason(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "invalid filter: "); i >= 0 {
		msg = msg[i+len("invalid filter: "):]
	}
	if strings.HasPrefix(msg, "unknown kind") {
		return "a kind of filter this version does not have"
	}
	return msg
}

// describeUpdate says what an update changes, in words and without values.
func describeUpdate(ef ExportFilter, have Filter) string {
	var parts []string
	if ef.Kind != have.Kind {
		parts = append(parts, "kind")
	}
	if ef.Target != have.Target {
		parts = append(parts, "what it applies to")
	}
	if ef.Value != have.Value {
		parts = append(parts, "value")
	}
	if ef.CaseInsensitive != have.CaseInsensitive {
		parts = append(parts, "ignore case")
	}
	if ef.Label != have.Label {
		parts = append(parts, "label")
	}
	return "changes the " + strings.Join(parts, ", ")
}
