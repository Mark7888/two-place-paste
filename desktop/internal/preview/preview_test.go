package preview

import (
	"strings"
	"testing"
)

// ftyp builds the first box of an ISO BMFF file with the given brands.
func ftyp(major string, compatible ...string) []byte {
	box := []byte{0, 0, 0, byte(16 + 4*len(compatible))}
	box = append(box, "ftyp"+major+"\x00\x00\x00\x00"...)
	for _, c := range compatible {
		box = append(box, c...)
	}
	return append(box, "....mdat"...)
}

func TestForEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		filename    string
		contentType string
		wantOK      bool
		wantRender  Render
		wantMIME    string
	}{
		{name: "png file", filename: "shot.png", contentType: "application/octet-stream", wantOK: true, wantRender: RenderImage, wantMIME: "image/png"},
		{name: "upper-case extension", filename: "SHOT.JPG", contentType: "application/octet-stream", wantOK: true, wantRender: RenderImage, wantMIME: "image/jpeg"},
		{name: "svg file", filename: "logo.svg", wantOK: true, wantRender: RenderImage, wantMIME: "image/svg+xml"},
		{name: "mp3 file", filename: "song.mp3", wantOK: true, wantRender: RenderAudio, wantMIME: "audio/mpeg"},
		{name: "opus file", filename: "voice.opus", wantOK: true, wantRender: RenderAudio, wantMIME: "audio/ogg"},
		{name: "mov file", filename: "clip.mov", wantOK: true, wantRender: RenderVideo, wantMIME: "video/quicktime"},
		{name: "go source", filename: "main.go", wantOK: true, wantRender: RenderText},
		{name: "html is text", filename: "index.html", wantOK: true, wantRender: RenderText},
		{name: "a name wins over a type", filename: "notes.txt", contentType: "image/png", wantOK: true, wantRender: RenderText},
		{name: "pdf is not previewed", filename: "report.pdf"},
		{name: "mkv is not previewed", filename: "film.mkv"},
		{name: "no extension", filename: "Makefile"},
		{name: "clipboard png", contentType: "image/png", wantOK: true, wantRender: RenderImage, wantMIME: "image/png"},
		{name: "clipboard text with charset", contentType: "text/plain; charset=utf-8", wantOK: true, wantRender: RenderText},
		{name: "clipboard html", contentType: "text/html", wantOK: true, wantRender: RenderText},
		{name: "clipboard audio", contentType: "audio/mpeg", wantOK: true, wantRender: RenderAudio},
		{name: "clipboard tiff", contentType: "image/tiff"},
		{name: "clipboard json", contentType: "application/json"},
		{name: "garbage type", contentType: "not a type;;"},
		{name: "nothing", contentType: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, ok := ForEntry(tt.filename, tt.contentType)
			if ok != tt.wantOK {
				t.Fatalf("ForEntry(%q, %q) ok = %v, want %v", tt.filename, tt.contentType, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if f.Render != tt.wantRender {
				t.Errorf("ForEntry(%q, %q).Render = %q, want %q", tt.filename, tt.contentType, f.Render, tt.wantRender)
			}
			if tt.wantMIME != "" && f.MIME != tt.wantMIME {
				t.Errorf("ForEntry(%q, %q).MIME = %q, want %q", tt.filename, tt.contentType, f.MIME, tt.wantMIME)
			}
			if f.Name == "" {
				t.Errorf("ForEntry(%q, %q).Name is empty", tt.filename, tt.contentType)
			}
		})
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()

	html := []byte("<!doctype html><script>alert(1)</script>")
	tests := []struct {
		filename string
		body     []byte
		want     bool
	}{
		{"a.png", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), true},
		{"a.png", html, false},
		{"a.jpg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF"), true},
		{"a.jpg", []byte("\x89PNG\r\n\x1a\n"), false},
		{"a.gif", []byte("GIF89a\x01\x00"), true},
		{"a.webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), true},
		{"a.webp", []byte("RIFF\x00\x00\x00\x00WAVEfmt "), false},
		{"a.bmp", []byte("BM\x00\x00"), true},
		{"a.ico", []byte("\x00\x00\x01\x00\x01\x00"), true},
		{"a.avif", ftyp("avif", "mif1", "miaf"), true},
		{"a.avif", ftyp("mif1", "avif"), true},
		{"a.avif", ftyp("isom", "mp41"), false},
		{"a.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), true},
		{"a.svg", []byte("\xef\xbb\xbf<?xml version=\"1.0\"?>\n<!-- made by hand -->\n<!DOCTYPE svg>\n<svg/>"), true},
		{"a.svg", []byte("<html><body><svg/></body></html>"), false},
		{"a.svg", []byte("\x00<svg/>"), false},
		{"a.mp3", []byte("ID3\x04\x00"), true},
		{"a.mp3", []byte("\xff\xfb\x90\x64"), true},
		{"a.mp3", []byte("\xff\xf1\x50\x80"), false},
		{"a.aac", []byte("\xff\xf1\x50\x80"), true},
		{"a.aac", []byte("\xff\xfb\x90\x64"), false},
		{"a.wav", []byte("RIFF\x00\x00\x00\x00WAVEfmt "), true},
		{"a.ogg", []byte("OggS\x00\x02"), true},
		{"a.flac", []byte("fLaC\x00\x00\x00\x22"), true},
		{"a.m4a", ftyp("M4A ", "isom"), true},
		{"a.mp4", ftyp("isom", "mp41"), true},
		{"a.mp4", html, false},
		{"a.webm", []byte("\x1a\x45\xdf\xa3\x9f\x42\x86\x81\x01\x42\x82\x84webm"), true},
		{"a.webm", []byte("\x1a\x45\xdf\xa3\x9f\x42\x86\x81\x01\x42\x82\x88matroska"), false},
		{"a.mov", ftyp("qt  "), true},
		{"a.mov", []byte("\x00\x00\x00\x08wide\x00\x00\x00\x00mdat"), true},
		{"a.mov", []byte("not a movie"), false},
		{"a.txt", []byte("hello, wörld"), true},
		{"a.txt", []byte{}, true},
		{"a.txt", []byte("bin\x00ary"), false},
		{"a.txt", []byte("\xff\xfeh\x00i\x00"), false},
		{"a.txt", []byte(strings.Repeat("a", sniffLen-1) + "é and more"), true},
		{"a.html", html, true},
	}
	for _, tt := range tests {
		f, ok := ForEntry(tt.filename, "")
		if !ok {
			t.Fatalf("ForEntry(%q) found no format", tt.filename)
		}
		if got := f.Verify(tt.body); got != tt.want {
			t.Errorf("Verify(%q, %q) = %v, want %v", tt.filename, tt.body[:min(len(tt.body), 24)], got, tt.want)
		}
	}
}

// TestEveryFormatIsComplete keeps a new row from going in half-filled: the UI
// needs a name, a media type and a renderer it knows.
func TestEveryFormatIsComplete(t *testing.T) {
	t.Parallel()

	known := map[Render]bool{RenderText: true, RenderImage: true, RenderAudio: true, RenderVideo: true}
	for _, f := range formats {
		if f.Name == "" || f.MIME == "" || !known[f.Render] || len(f.exts) == 0 || f.sniff == nil {
			t.Errorf("format %+v is incomplete", f)
		}
		for _, e := range f.exts {
			if e != strings.ToLower(e) || !strings.HasPrefix(e, ".") {
				t.Errorf("format %q: extension %q must be lower case with a dot", f.Name, e)
			}
		}
	}
}

func TestStreamed(t *testing.T) {
	t.Parallel()

	for r, want := range map[Render]bool{RenderText: false, RenderImage: false, RenderAudio: true, RenderVideo: true} {
		if got := r.Streamed(); got != want {
			t.Errorf("Render(%q).Streamed() = %v, want %v", r, got, want)
		}
	}
}
