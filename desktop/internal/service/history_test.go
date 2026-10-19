package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// TestHistoryShowsTypesAndGatesPreviews: every row says what it is from its
// header, without a fetch, and only formats the preview table knows, within
// the auto-download limit, offer a preview.
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
	pngFile := put(tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "shot.png", Body: []byte(pngMagic)})
	goFile := put(tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "main.go", Body: []byte("package main")})
	mp3File := put(tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "song.mp3", Body: []byte("ID3")})
	bigFile := put(tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "big.txt", Body: make([]byte, 101)})
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
		id, kind, render string
		previewable      bool
	}{
		{text, "text", "text", true},
		{image, "image", "image", true},
		{bigText, "text", "", false},
		{file, "file", "", false},
		{other, "file", "", false},
		{pngFile, "file", "image", true},
		{goFile, "file", "text", true},
		{mp3File, "file", "audio", true},
		{bigFile, "file", "", false},
		{"pushed", "", "", false},
	} {
		got := rows[tt.id]
		if got.Kind != tt.kind || got.Previewable != tt.previewable || got.Render != tt.render {
			t.Errorf("%s: kind %q render %q previewable %v, want %q %q %v",
				tt.id, got.Kind, got.Render, got.Previewable, tt.kind, tt.render, tt.previewable)
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

const pngMagic = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"

// TestEntryPreviewOfFiles: a file in the preview table is drawn by the table's
// renderer under the table's media type, and a file whose bytes are not what
// its name says is refused rather than drawn.
func TestEntryPreviewOfFiles(t *testing.T) {
	t.Parallel()

	longSource := strings.Repeat("x", previewFileRunes+10)
	tests := []struct {
		name    string
		item    tppclient.Item
		wantErr int
		check   func(t *testing.T, v localui.EntryPreview)
	}{
		{
			name: "text file",
			item: tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "notes.md", Body: []byte("\ufeff# Notes")},
			check: func(t *testing.T, v localui.EntryPreview) {
				t.Helper()
				if v.Kind != "file" || v.Render != "text" || v.Format != "Markdown" || v.Text != "# Notes" || v.Truncated {
					t.Errorf("EntryPreview() = %+v, want the file's text without its BOM", v)
				}
			},
		},
		{
			name: "long text file",
			item: tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "main.go", Body: []byte(longSource)},
			check: func(t *testing.T, v localui.EntryPreview) {
				t.Helper()
				if !v.Truncated || utf8.RuneCountInString(v.Text) != previewFileRunes+1 {
					t.Errorf("EntryPreview() text = %d runes truncated %v, want %d and true",
						utf8.RuneCountInString(v.Text), v.Truncated, previewFileRunes+1)
				}
			},
		},
		{
			name: "image file",
			item: tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "shot.png", Body: []byte(pngMagic)},
			check: func(t *testing.T, v localui.EntryPreview) {
				t.Helper()
				if v.Kind != "file" || v.Render != "image" || !strings.HasPrefix(v.ImageDataURL, "data:image/png;base64,") {
					t.Errorf("EntryPreview() = %+v, want a PNG data URL", v)
				}
			},
		},
		{
			name: "svg file",
			item: tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "logo.svg", Body: []byte("<svg/>")},
			check: func(t *testing.T, v localui.EntryPreview) {
				t.Helper()
				if !strings.HasPrefix(v.ImageDataURL, "data:image/svg+xml;base64,") {
					t.Errorf("EntryPreview() data URL = %.40q, want an SVG one", v.ImageDataURL)
				}
			},
		},
		{
			name: "clipboard image keeps working",
			item: tppclient.Item{ContentType: clipboard.TypeImagePNG, Body: []byte(pngMagic)},
			check: func(t *testing.T, v localui.EntryPreview) {
				t.Helper()
				if v.Kind != "image" || v.Render != "image" || v.ImageDataURL == "" {
					t.Errorf("EntryPreview() = %+v, want an image", v)
				}
			},
		},
		{
			name: "audio file comes without its body",
			item: tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "song.mp3", Body: []byte("ID3\x04")},
			check: func(t *testing.T, v localui.EntryPreview) {
				t.Helper()
				if v.Kind != "file" || v.Render != "audio" || v.Format != "MP3 audio" || v.Text != "" || v.ImageDataURL != "" {
					t.Errorf("EntryPreview() = %+v, want an audio description and no body", v)
				}
			},
		},
		{
			name:    "a page named as a picture",
			item:    tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "shot.png", Body: []byte("<html><script>alert(1)</script>")},
			wantErr: http.StatusUnprocessableEntity,
		},
		{
			name:    "binary named as text",
			item:    tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "notes.txt", Body: []byte("\x00\x01\x02")},
			wantErr: http.StatusUnprocessableEntity,
		},
		{
			name:    "unknown file type",
			item:    tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "setup.exe", Body: []byte("MZ")},
			wantErr: http.StatusUnprocessableEntity,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			ctx := context.Background()
			item := tt.item
			item.CreatedAt = f.now()
			meta, err := f.relay.PutEntry(ctx, item)
			if err != nil {
				t.Fatalf("PutEntry() error = %v", err)
			}
			view, err := f.svc.EntryPreview(ctx, meta.ID)
			if tt.wantErr != 0 {
				var se *localui.StatusError
				if !errors.As(err, &se) || se.Code != tt.wantErr {
					t.Fatalf("EntryPreview() error = %v, want %d", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("EntryPreview() error = %v", err)
			}
			tt.check(t, view)
		})
	}
}

// TestEntryContentPlaysOnlyAudioAndVideo: the bytes the page plays come under
// the table's media type, and nothing but audio and video comes out this way.
func TestEntryContentPlaysOnlyAudioAndVideo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		item     tppclient.Item
		wantType string
		wantErr  int
	}{
		{name: "mp3", item: tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "song.mp3", Body: []byte("ID3\x04")}, wantType: "audio/mpeg"},
		{name: "webm", item: tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "clip.webm", Body: []byte("\x1a\x45\xdf\xa3\x9f\x42\x82\x84webm")}, wantType: "video/webm"},
		{name: "unnamed audio", item: tppclient.Item{ContentType: "audio/ogg", Body: []byte("OggS\x00")}, wantType: "audio/ogg"},
		{name: "text file", item: tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "notes.txt", Body: []byte("hi")}, wantErr: http.StatusUnprocessableEntity},
		{name: "image", item: tppclient.Item{ContentType: clipboard.TypeImagePNG, Body: []byte(pngMagic)}, wantErr: http.StatusUnprocessableEntity},
		{name: "a page named as a video", item: tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "clip.mp4", Body: []byte("<html>")}, wantErr: http.StatusUnprocessableEntity},
		{name: "unknown file", item: tppclient.Item{ContentType: clipboard.TypeOctetStream, Filename: "film.mkv", Body: []byte("\x1a\x45\xdf\xa3")}, wantErr: http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			ctx := context.Background()
			item := tt.item
			item.CreatedAt = f.now()
			meta, err := f.relay.PutEntry(ctx, item)
			if err != nil {
				t.Fatalf("PutEntry() error = %v", err)
			}
			got, err := f.svc.EntryContent(ctx, meta.ID)
			if tt.wantErr != 0 {
				var se *localui.StatusError
				if !errors.As(err, &se) || se.Code != tt.wantErr {
					t.Fatalf("EntryContent() error = %v, want %d", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("EntryContent() error = %v", err)
			}
			if got.ContentType != tt.wantType || string(got.Body) != string(tt.item.Body) {
				t.Errorf("EntryContent() = %q, %d bytes, want %q and the entry's bytes", got.ContentType, len(got.Body), tt.wantType)
			}
			if f.clip.writes != 0 {
				t.Errorf("playing an entry wrote the clipboard %d times, want 0", f.clip.writes)
			}
		})
	}
}
