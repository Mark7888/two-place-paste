package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// TestHistoryShowsTypesAndGatesPreviews: every row says what it is from its
// header, without a fetch, and only text and images within the auto-download
// limit offer a preview.
func TestHistoryShowsTypesAndGatesPreviews(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{AutoDownloadMaxBytes: int64Ptr(100)}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}

	put := func(item tppclient.Item) string {
		t.Helper()
		item.CreatedAt = f.now()
		meta, err := f.relay.PutEntry(ctx, item)
		if err != nil {
			t.Fatalf("PutEntry() error = %v", err)
		}
		return meta.ID
	}
	text := put(tppclient.Item{ContentType: clipboard.TypeText, Body: []byte("a note")})
	image := put(tppclient.Item{ContentType: clipboard.TypeImagePNG, Body: make([]byte, 50)})
	bigText := put(tppclient.Item{ContentType: clipboard.TypeText, Body: make([]byte, 101)})
	file := put(tppclient.Item{ContentType: "application/pdf", Filename: "report.pdf", Body: []byte("%PDF")})
	other := put(tppclient.Item{ContentType: "application/json", Body: []byte("{}")})
	f.relay.push(tppclient.Item{ContentType: clipboard.TypeText, Body: []byte("from an older client")})

	page, err := f.svc.History(ctx, localui.HistoryQuery{})
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	rows := map[string]localui.EntryView{}
	for _, e := range page.Entries {
		rows[e.ID] = e
	}

	for _, tt := range []struct {
		id, kind    string
		previewable bool
	}{
		{text, "text", true},
		{image, "image", true},
		{bigText, "text", false},
		{file, "file", false},
		{other, "file", false},
		{"pushed", "", false},
	} {
		got := rows[tt.id]
		if got.Kind != tt.kind || got.Previewable != tt.previewable {
			t.Errorf("%s: kind %q previewable %v, want %q %v", tt.id, got.Kind, got.Previewable, tt.kind, tt.previewable)
		}
		if !got.Previewable && got.NoPreview == "" {
			t.Errorf("%s: no preview, and no reason given", tt.id)
		}
	}
	if r := rows[file]; r.Filename != "report.pdf" || r.ContentType != "application/pdf" || r.Bytes == nil || *r.Bytes != 4 {
		t.Errorf("file row = %+v, want its name, type and size", r)
	}
	if r := rows["pushed"]; r.Bytes != nil || r.ContentType != "" {
		t.Errorf("an entry without a header = %+v, want no type", r)
	}

	// The service refuses a preview the list would not offer for its type.
	_, err = f.svc.EntryPreview(ctx, file)
	var se *localui.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusUnprocessableEntity {
		t.Errorf("EntryPreview(file) error = %v, want 422", err)
	}
}
