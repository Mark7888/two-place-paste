package service

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/desktop/internal/sensitive"
)

func exportFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	ctx := context.Background()
	jwt := []string{"jwt"}
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{Detectors: &jwt, Filters: &[]localui.FilterView{
		{ID: "vpn", Kind: "prefix", Target: "text", Value: "vpn:", CaseInsensitive: true, Enabled: true, Label: "Work VPN"},
		{ID: "pem", Kind: "suffix", Target: "filename", Value: ".pem", Enabled: true},
		{ID: "pin", Kind: "exact", Target: "text", Value: "1234", Enabled: true, Label: "PIN"},
	}}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	return f
}

// TestExportToClipboard: everything exportable goes, marked private, as the
// service's own write; exact matches stay behind and are counted; and the
// clipboard then reads as an export everywhere it is looked at.
func TestExportToClipboard(t *testing.T) {
	t.Parallel()
	f := exportFixture(t)
	ctx := context.Background()

	res, err := f.svc.ExportFilters(ctx, localui.FilterExportRequest{To: "clipboard"})
	if err != nil {
		t.Fatalf("ExportFilters() error = %v", err)
	}
	if res.Filters != 2 || !res.Detectors || res.SkippedExact != 1 {
		t.Errorf("result = %+v, want 2 filters, the detectors, 1 exact skipped", res)
	}
	c, err := f.clip.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Concealed == "" {
		t.Error("the export was written without the private marker")
	}
	if strings.Contains(string(c.Body), "1234") || !strings.Contains(string(c.Body), `"vpn:"`) {
		t.Errorf("export body = %s", c.Body)
	}
	doc, err := sensitive.ParseExport(string(c.Body))
	if err != nil || !doc.Detectors["jwt"] || doc.Detectors["private_keys"] {
		t.Fatalf("ParseExport(clipboard) = %+v, %v", doc, err)
	}

	if chk := f.svc.CheckFilterImport(ctx); !chk.Available || chk.Filters != 2 || !chk.Detectors {
		t.Errorf("CheckFilterImport = %+v, want the export found", chk)
	}
	if cv := f.svc.Status(ctx).Clipboard; !cv.Sensitive || cv.SensitiveReason != sensitive.ReasonExport || cv.Preview != "" {
		t.Errorf("clipboard view = %+v, want it masked as an export", cv)
	}
	if n := f.relay.count(); n != 0 {
		t.Errorf("exporting uploaded something (%d entries)", n)
	}
}

func TestExportOneFilter(t *testing.T) {
	t.Parallel()
	f := exportFixture(t)
	ctx := context.Background()

	res, err := f.svc.ExportFilters(ctx, localui.FilterExportRequest{IDs: []string{"pem"}, To: "clipboard"})
	if err != nil || res.Filters != 1 || res.Detectors {
		t.Fatalf("export one = %+v, %v; want one filter and no detectors", res, err)
	}
	c, _ := f.clip.Read(ctx)
	doc, err := sensitive.ParseExport(string(c.Body))
	if err != nil || doc.Detectors != nil || len(doc.Filters) != 1 || doc.Filters[0].ID != "pem" {
		t.Errorf("export = %+v, %v", doc, err)
	}

	if _, err := f.svc.ExportFilters(ctx, localui.FilterExportRequest{IDs: []string{"pin"}, To: "clipboard"}); statusOf(err) != http.StatusBadRequest {
		t.Errorf("export of an exact match = %v, want a 400", err)
	}
	if _, err := f.svc.ExportFilters(ctx, localui.FilterExportRequest{IDs: []string{"unsaved"}, To: "clipboard"}); statusOf(err) != http.StatusNotFound {
		t.Errorf("export of an unknown filter = %v, want a 404", err)
	}
}

// TestExportToStore: straight into the store, tagged and named without the
// filter's value; the same export again is refused by name.
func TestExportToStore(t *testing.T) {
	t.Parallel()
	f := exportFixture(t)
	ctx := context.Background()
	f.enableStore()
	f.clip.set(textContent("left alone"))

	res, err := f.svc.ExportFilters(ctx, localui.FilterExportRequest{To: "store"})
	if err != nil {
		t.Fatalf("export to store = %v", err)
	}
	if res.Item == nil || res.Item.Name != "Filters: 2 custom, 1 built-in on" || len(res.Item.Tags) != 1 || res.Item.Tags[0] != "filters" {
		t.Errorf("store item = %+v", res.Item)
	}
	if c, _ := f.clip.Read(ctx); string(c.Body) != "left alone" {
		t.Error("exporting to the store touched the clipboard")
	}

	one, err := f.svc.ExportFilters(ctx, localui.FilterExportRequest{IDs: []string{"pem"}, To: "store"})
	if err != nil || one.Item.Name != "Filter: custom “ends with” filter" {
		t.Errorf("single export to store = %+v, %v", one.Item, err)
	}
	if _, err := f.svc.ExportFilters(ctx, localui.FilterExportRequest{To: "store"}); statusOf(err) != http.StatusConflict ||
		!strings.Contains(err.Error(), "Filters: 2 custom") {
		t.Errorf("same export again = %v, want a 409 naming the item", err)
	}
}

// TestPreviewFilterImport: an export from another device, compared with this
// device's draft. Nothing is saved.
func TestPreviewFilterImport(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.svc.PreviewFilterImport(ctx, localui.FilterImportDraft{}); statusOf(err) != http.StatusConflict {
		t.Fatalf("preview of an empty clipboard = %v, want a 409", err)
	}
	if chk := f.svc.CheckFilterImport(ctx); chk.Available {
		t.Error("CheckFilterImport found an export on an empty clipboard")
	}

	doc, _ := sensitive.BuildExport([]sensitive.Filter{
		{ID: "vpn", Kind: sensitive.KindPrefix, Target: sensitive.TargetText, Value: "vpn2:", CaseInsensitive: true, Enabled: true, Label: "Work VPN"},
		{ID: "new", Kind: sensitive.KindContains, Target: sensitive.TargetText, Value: "acme", Enabled: true},
	}, []string{"jwt"})
	text, _ := doc.Encode()
	f.clip.set(clipboard.Content{ContentType: clipboard.TypeText, Body: []byte(text)})

	plan, err := f.svc.PreviewFilterImport(ctx, localui.FilterImportDraft{
		Filters:   []localui.FilterView{{ID: "vpn", Kind: "prefix", Target: "text", Value: "vpn:", CaseInsensitive: true, Enabled: true, Label: "Work VPN"}},
		Detectors: []string{"private_keys"},
	})
	if err != nil {
		t.Fatalf("PreviewFilterImport() error = %v", err)
	}
	if len(plan.Filters) != 2 || plan.Filters[0].Status != "update" || plan.Filters[0].Selected ||
		plan.Filters[1].Status != "new" || !plan.Filters[1].Selected {
		t.Errorf("filter rows = %+v", plan.Filters)
	}
	statuses := map[string]string{}
	for _, d := range plan.Detectors {
		statuses[d.Name] = d.Status
	}
	if statuses["private_keys"] != "turn_off" || statuses["jwt"] != "turn_on" {
		t.Errorf("detector rows = %+v", plan.Detectors)
	}
	if view := f.svc.Settings(ctx); len(view.Filters) != 0 {
		t.Errorf("a preview saved filters: %+v", view.Filters)
	}
}

// An export copied by some other route is still never uploaded on its own.
func TestAutoWatchSkipsAnExport(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	doc, _ := sensitive.BuildExport(nil, []string{})
	text, _ := doc.Encode()
	f.svc.onClipboardChange(ctx, textContent(text), f.now())
	if n := f.relay.count(); n != 0 {
		t.Errorf("an export was uploaded automatically (%d entries)", n)
	}
	if note := f.svc.Status(ctx).SensitiveNote; note == nil || note.Reason != sensitive.ReasonExport {
		t.Errorf("SensitiveNote = %+v, want the export named", note)
	}
}

// TestImportFromAStoreItem: an exported set kept in the store is flagged on
// its preview and imports straight from the store, without the clipboard;
// an ordinary item is neither.
func TestImportFromAStoreItem(t *testing.T) {
	t.Parallel()
	f := exportFixture(t)
	ctx := context.Background()
	f.enableStore()

	res, err := f.svc.ExportFilters(ctx, localui.FilterExportRequest{To: "store"})
	if err != nil {
		t.Fatalf("export to store = %v", err)
	}
	view, err := f.svc.StoreItemPreview(ctx, res.Item.ID)
	if err != nil {
		t.Fatalf("StoreItemPreview() = %v", err)
	}
	if view.FilterExport == nil || !view.FilterExport.Available || view.FilterExport.Filters != 2 || !view.FilterExport.Detectors {
		t.Errorf("FilterExport = %+v, want the export flagged", view.FilterExport)
	}

	f.clip.set(textContent("not an export"))
	plan, err := f.svc.PreviewFilterImport(ctx, localui.FilterImportDraft{StoreItemID: res.Item.ID})
	if err != nil {
		t.Fatalf("PreviewFilterImport(store item) = %v", err)
	}
	if len(plan.Filters) != 2 || plan.Filters[0].Status != "new" {
		t.Errorf("filter rows = %+v, want the two exported filters as new", plan.Filters)
	}

	f.clip.set(textContent("an ordinary note"))
	note, err := f.svc.SaveToStore(ctx, localui.StoreSaveRequest{Source: "clipboard", StoreDetails: details("Note")})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := f.svc.StoreItemPreview(ctx, note.ID); v.FilterExport != nil {
		t.Errorf("an ordinary item was flagged as an export: %+v", v.FilterExport)
	}
	if _, err := f.svc.PreviewFilterImport(ctx, localui.FilterImportDraft{StoreItemID: note.ID}); statusOf(err) != http.StatusConflict {
		t.Errorf("import from an ordinary item = %v, want a 409", err)
	}
}
