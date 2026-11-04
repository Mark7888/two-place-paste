package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/desktop/internal/sensitive"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// Export and import of sensitive-content filters
// (docs/internal/plans/auto-upload-filters.md §10).
//
// An export is made from the saved settings, never from an editor's unsaved
// draft, so what leaves the device is what this device actually applies. An
// import is the other way round: it is compared with the draft and only ever
// lands in it, so the user sees the result in the editor, can test it, and
// saves it — or discards it — like any other edit.

// exportStoreTag is the tag an export saved to the store carries, so the
// store's tag filter finds every saved filter set at once.
const exportStoreTag = "filters"

// ExportFilters implements localui.API.
func (s *Service) ExportFilters(ctx context.Context, req localui.FilterExportRequest) (localui.FilterExportResult, error) {
	s.mu.Lock()
	set := s.settings.Clone()
	s.mu.Unlock()

	all := req.IDs == nil
	filters := set.Filters
	var detectors []string
	if all {
		detectors = set.EnabledDetectors()
	} else {
		byID := map[string]sensitive.Filter{}
		for _, f := range set.Filters {
			byID[f.ID] = f
		}
		filters = filters[:0:0]
		for _, id := range req.IDs {
			f, ok := byID[id]
			if !ok {
				return localui.FilterExportResult{}, localui.Errorf(http.StatusNotFound, nil,
					"that filter is not saved on this device; save the filters first")
			}
			filters = append(filters, f)
		}
	}
	doc, skipped := sensitive.BuildExport(filters, detectors)
	if !all && len(doc.Filters) == 0 {
		return localui.FilterExportResult{}, localui.Errorf(http.StatusBadRequest, nil,
			"an exact match keeps only a hash, so it cannot be exported")
	}
	text, err := doc.Encode()
	switch {
	case errors.Is(err, sensitive.ErrExportTooLarge):
		return localui.FilterExportResult{}, localui.Errorf(http.StatusRequestEntityTooLarge, err,
			"these filters are too large to export in one piece; export fewer of them, one at a time")
	case err != nil:
		return localui.FilterExportResult{}, fmt.Errorf("export the filters: %w", err)
	}
	res := localui.FilterExportResult{Filters: len(doc.Filters), Detectors: all, SkippedExact: skipped}
	content := clipboard.Content{ContentType: clipboard.TypeText, Body: []byte(text)}

	switch req.To {
	case "clipboard":
		if err := s.writeOwnClipboard(ctx, content); err != nil {
			return localui.FilterExportResult{}, err
		}
		res.Message = "copied " + exportSummary(doc) + " to the clipboard"
	case "store":
		relay, err := s.ready()
		if err != nil {
			return localui.FilterExportResult{}, err
		}
		name := req.Name
		if name == "" {
			name = exportName(doc, filters)
		}
		// Straight into the store, not through the clipboard: the export
		// holds filter values, and a round trip through the clipboard would
		// ask the protected-entry question about the user's own filters.
		//
		// The same export already in the store comes back as the store's
		// own *DuplicateError, naming the item, so the dialog can offer
		// "Save anyway", which sends AllowDuplicate.
		item, err := s.saveToStore(ctx, relay,
			tppclient.Item{ContentType: content.ContentType, Body: content.Body, CreatedAt: s.now()},
			localui.StoreDetails{Name: name, Tags: []string{exportStoreTag}}, req.AllowDuplicate)
		if err != nil {
			return localui.FilterExportResult{}, err
		}
		res.Item = &item
		res.Message = "saved " + exportSummary(doc) + " to the store as “" + item.Name + "”"
	default:
		return localui.FilterExportResult{}, localui.Errorf(http.StatusBadRequest, nil, "unknown destination %q", req.To)
	}
	s.logger.InfoContext(ctx, "filters exported", "to", req.To, "filters", res.Filters,
		"detectors", res.Detectors, "skipped_exact", res.SkippedExact)
	return res, nil
}

// writeOwnClipboard puts content the service made on the clipboard, marked
// private. Like a download, it is this service's own write, so the watcher
// does not upload it; unlike one, it is not a sync and leaves the sync notes
// alone.
func (s *Service) writeOwnClipboard(ctx context.Context, c clipboard.Content) error {
	c.Concealed = clipboard.ReasonConcealed
	s.watcher.Suppress(c)
	if err := s.clip.Write(ctx, c); err != nil {
		if errors.Is(err, clipboard.ErrUnsupported) {
			return localui.Errorf(http.StatusNotImplemented, err, "this build cannot reach a clipboard")
		}
		return localui.Errorf(http.StatusInternalServerError, err, "the clipboard could not be written")
	}
	s.watcher.Rebase(ctx, s.now())
	s.hub.publish(localui.Event{Kind: localui.EventClipboard, Message: string(c.Kind())})
	return nil
}

// exportSummary says what an export holds, for a message.
func exportSummary(doc sensitive.Export) string {
	n := len(doc.Filters)
	filters := fmt.Sprintf("%d custom filters", n)
	if n == 1 {
		filters = "1 custom filter"
	}
	if doc.Detectors == nil {
		return filters
	}
	return filters + " and the built-in filters' settings"
}

// exportName is the store item's name when the user gives none. A single
// filter is named by its label, or its kind: never its value, which may be
// private.
func exportName(doc sensitive.Export, filters []sensitive.Filter) string {
	if doc.Detectors == nil && len(filters) == 1 {
		f := filters[0]
		if f.Label != "" {
			return "Filter: " + f.Label
		}
		return "Filter: custom “" + f.KindName() + "” filter"
	}
	on := 0
	for _, v := range doc.Detectors {
		if v {
			on++
		}
	}
	return fmt.Sprintf("Filters: %d custom, %d built-in on", len(doc.Filters), on)
}

// CheckFilterImport implements localui.API. It never fails: a clipboard that
// cannot be read holds no export as far as the Import button is concerned.
func (s *Service) CheckFilterImport(ctx context.Context) localui.FilterImportCheck {
	doc, err := s.clipboardExport(ctx)
	if err != nil {
		return localui.FilterImportCheck{}
	}
	return localui.FilterImportCheck{Available: true, Filters: len(doc.Filters), Detectors: doc.Detectors != nil}
}

// PreviewFilterImport implements localui.API.
func (s *Service) PreviewFilterImport(ctx context.Context, draft localui.FilterImportDraft) (localui.FilterImportPlan, error) {
	var doc sensitive.Export
	var err error
	if draft.StoreItemID != "" {
		doc, err = s.storeExport(ctx, draft.StoreItemID)
	} else {
		doc, err = s.clipboardExport(ctx)
	}
	if err != nil {
		return localui.FilterImportPlan{}, err
	}
	current := make([]sensitive.Filter, 0, len(draft.Filters))
	for _, v := range draft.Filters {
		current = append(current, sensitive.Filter{
			ID: v.ID, Kind: sensitive.Kind(v.Kind), Target: sensitive.Target(v.Target), Value: v.Value,
			CaseInsensitive: v.CaseInsensitive, Enabled: v.Enabled, Label: v.Label,
		})
	}
	plan := sensitive.PlanImport(doc, current, draft.Detectors)

	out := localui.FilterImportPlan{
		Filters:   make([]localui.FilterImportRow, 0, len(plan.Filters)),
		Detectors: make([]localui.DetectorImportRow, 0, len(plan.Detectors)),
	}
	for _, r := range plan.Filters {
		f := r.Filter
		out.Filters = append(out.Filters, localui.FilterImportRow{
			Filter: localui.FilterView{
				ID: f.ID, Kind: string(f.Kind), Target: string(f.Target), Value: f.Value,
				CaseInsensitive: f.CaseInsensitive, Enabled: f.Enabled, Label: f.Label,
			},
			Status: string(r.Status), Selected: r.Selected, Reason: r.Reason, ExistingID: r.ExistingID,
		})
	}
	for _, r := range plan.Detectors {
		out.Detectors = append(out.Detectors, localui.DetectorImportRow{
			Name: r.Name, Title: r.Title, Status: string(r.Status), Enabled: r.Enabled, Selected: r.Selected, Reason: r.Reason,
		})
	}
	s.logger.InfoContext(ctx, "filter import previewed", "filters", len(out.Filters), "detectors", len(out.Detectors))
	return out, nil
}

// storeExport reads a filter export kept as a store item, straight from the
// store: importing it from the Store tab does not go through the clipboard.
func (s *Service) storeExport(ctx context.Context, itemID string) (sensitive.Export, error) {
	item, err := s.fetchStoreItem(ctx, itemID)
	if err != nil {
		return sensitive.Export{}, err
	}
	doc, err := exportFromItem(item)
	if err != nil {
		return sensitive.Export{}, localui.Errorf(http.StatusConflict, err, "that store item is not an exported filter set")
	}
	return doc, nil
}

// exportFromItem parses an entry or store item as a filter export.
func exportFromItem(item tppclient.Item) (sensitive.Export, error) {
	c := clipboard.Content{ContentType: item.ContentType, Filename: item.Filename, Body: item.Body}
	if c.Kind() != clipboard.KindText || len(c.Body) > sensitive.MaxExportBytes {
		return sensitive.Export{}, sensitive.ErrNotExport
	}
	doc, err := sensitive.ParseExport(c.Text())
	if err != nil {
		return sensitive.Export{}, fmt.Errorf("read the filter export: %w", err)
	}
	return doc, nil
}

// clipboardExport reads a filter export from the clipboard.
func (s *Service) clipboardExport(ctx context.Context) (sensitive.Export, error) {
	c, err := s.clip.Read(ctx)
	if err == nil && c.Kind() == clipboard.KindText && len(c.Body) <= sensitive.MaxExportBytes {
		if doc, perr := sensitive.ParseExport(c.Text()); perr == nil {
			return doc, nil
		}
	}
	return sensitive.Export{}, localui.Errorf(http.StatusConflict, sensitive.ErrNotExport,
		"the clipboard does not hold an exported filter set; copy one first, from the store or another device")
}
