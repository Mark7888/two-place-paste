// Package preview decides which history entries the desktop UI can draw, and
// how (SPEC §7.2).
//
// It is one table. Each row is a format: the extensions a file of it is named
// with, the media type it is served as, how the UI renders it, and a check of
// its first bytes. Adding a format is adding a row — and, when the UI has no
// renderer for it yet, a component on the other side.
//
// A file arrives as application/octet-stream with a name, so its type comes
// from the name; and a name is only a claim, so the bytes are checked before
// anything is drawn. A ".png" that is really an HTML page is refused rather
// than handed to a browser that might be talked into reading it as one. The
// media type the UI is given always comes from this table, never from the
// sender.
package preview

import (
	"bytes"
	"mime"
	"path"
	"strings"
	"unicode/utf8"
)

// Render is how the UI draws a format. The UI holds one component per value.
type Render string

// The renderers the UI knows.
const (
	RenderText  Render = "text"
	RenderImage Render = "image"
	RenderAudio Render = "audio"
	RenderVideo Render = "video"
)

// Streamed reports whether the UI fetches the decrypted bytes on their own,
// rather than receiving them inside the preview response. Audio and video are
// too large to inline and are played from a blob the page builds.
func (r Render) Streamed() bool { return r == RenderAudio || r == RenderVideo }

// Format is one previewable format.
type Format struct {
	// Name is what the UI calls it: "PNG image", "MP3 audio".
	Name string
	// MIME is the media type the bytes are rendered as.
	MIME string
	// Render is the UI component that draws it.
	Render Render

	exts  []string
	types []string
	sniff func([]byte) bool
}

// Verify reports whether body looks like what the format claims to be.
func (f Format) Verify(body []byte) bool {
	if f.sniff == nil {
		return true
	}
	return f.sniff(body)
}

// sniffLen is how much of a body the checks look at. Every signature here is
// in the first few bytes; the text check reads further, because a binary file
// can open with a printable header.
const sniffLen = 8 << 10

// formats is the table. Order matters only for content-type lookups, where
// the first row naming a type wins.
var formats = []Format{
	// Images. The UI draws these with <img>, which never runs a script, and
	// that is what makes SVG safe to include.
	{Name: "PNG image", MIME: "image/png", Render: RenderImage, exts: []string{".png", ".apng"}, types: []string{"image/png", "image/apng"}, sniff: prefix("\x89PNG\r\n\x1a\n")},
	{Name: "JPEG image", MIME: "image/jpeg", Render: RenderImage, exts: []string{".jpg", ".jpeg", ".jpe", ".jfif"}, types: []string{"image/jpeg"}, sniff: prefix("\xff\xd8\xff")},
	{Name: "GIF image", MIME: "image/gif", Render: RenderImage, exts: []string{".gif"}, types: []string{"image/gif"}, sniff: anyPrefix("GIF87a", "GIF89a")},
	{Name: "WebP image", MIME: "image/webp", Render: RenderImage, exts: []string{".webp"}, types: []string{"image/webp"}, sniff: riff("WEBP")},
	{Name: "BMP image", MIME: "image/bmp", Render: RenderImage, exts: []string{".bmp"}, types: []string{"image/bmp"}, sniff: prefix("BM")},
	{Name: "Icon", MIME: "image/x-icon", Render: RenderImage, exts: []string{".ico"}, types: []string{"image/x-icon", "image/vnd.microsoft.icon"}, sniff: prefix("\x00\x00\x01\x00")},
	{Name: "AVIF image", MIME: "image/avif", Render: RenderImage, exts: []string{".avif"}, types: []string{"image/avif"}, sniff: ftypBrand("avif", "avis")},
	{Name: "SVG image", MIME: "image/svg+xml", Render: RenderImage, exts: []string{".svg"}, types: []string{"image/svg+xml"}, sniff: isSVG},

	// Audio. The browser decodes it; support for a codec is the browser's,
	// and the UI says so when it cannot play one.
	{Name: "MP3 audio", MIME: "audio/mpeg", Render: RenderAudio, exts: []string{".mp3"}, types: []string{"audio/mpeg", "audio/mp3"}, sniff: isMP3},
	{Name: "WAV audio", MIME: "audio/wav", Render: RenderAudio, exts: []string{".wav"}, types: []string{"audio/wav", "audio/x-wav", "audio/wave"}, sniff: riff("WAVE")},
	{Name: "Ogg audio", MIME: "audio/ogg", Render: RenderAudio, exts: []string{".ogg", ".oga", ".opus"}, types: []string{"audio/ogg", "audio/opus"}, sniff: prefix("OggS")},
	{Name: "FLAC audio", MIME: "audio/flac", Render: RenderAudio, exts: []string{".flac"}, types: []string{"audio/flac", "audio/x-flac"}, sniff: anyPrefix("fLaC", "ID3")},
	{Name: "AAC audio", MIME: "audio/mp4", Render: RenderAudio, exts: []string{".m4a"}, types: []string{"audio/mp4", "audio/x-m4a"}, sniff: isISOBMFF},
	{Name: "AAC audio", MIME: "audio/aac", Render: RenderAudio, exts: []string{".aac"}, types: []string{"audio/aac"}, sniff: isADTS},

	// Video.
	{Name: "MP4 video", MIME: "video/mp4", Render: RenderVideo, exts: []string{".mp4", ".m4v"}, types: []string{"video/mp4"}, sniff: isISOBMFF},
	{Name: "WebM video", MIME: "video/webm", Render: RenderVideo, exts: []string{".webm"}, types: []string{"video/webm"}, sniff: isWebM},
	{Name: "QuickTime video", MIME: "video/quicktime", Render: RenderVideo, exts: []string{".mov"}, types: []string{"video/quicktime"}, sniff: isQuickTime},

	// Text. Shown as text, never rendered: an .html file is its source, not a
	// page. The check is that it reads as UTF-8 and holds no NUL.
	textFormat("Text", ".txt", ".text", ".log", ".csv", ".tsv", ".srt", ".vtt", ".diff", ".patch"),
	textFormat("Markdown", ".md", ".markdown"),
	textFormat("Data", ".json", ".jsonl", ".ndjson", ".xml", ".yaml", ".yml", ".toml"),
	textFormat("Config", ".ini", ".cfg", ".conf", ".env", ".properties"),
	textFormat("Web source", ".html", ".htm", ".css", ".scss", ".less"),
	textFormat("Source code",
		".go", ".py", ".rb", ".rs", ".java", ".kt", ".kts", ".scala", ".groovy", ".gradle",
		".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx", ".c", ".h", ".cc", ".cpp", ".hpp",
		".cs", ".swift", ".m", ".php", ".pl", ".lua", ".r", ".dart", ".sql", ".proto",
		".sh", ".bash", ".zsh", ".fish", ".ps1", ".bat", ".cmd", ".tex"),
}

func textFormat(name string, exts ...string) Format {
	return Format{Name: name, MIME: "text/plain; charset=utf-8", Render: RenderText, exts: exts, sniff: isText}
}

// clipboardText is any text/* entry without a name — what a copy of text
// from any application becomes.
var clipboardText = Format{Name: "Text", MIME: "text/plain; charset=utf-8", Render: RenderText, sniff: isText}

var byExt, byType = func() (map[string]Format, map[string]Format) {
	exts, types := map[string]Format{}, map[string]Format{}
	for _, f := range formats {
		for _, e := range f.exts {
			if _, dup := exts[e]; dup {
				panic("preview: extension " + e + " is listed twice")
			}
			exts[e] = f
		}
		for _, t := range f.types {
			if _, dup := types[t]; !dup {
				types[t] = f
			}
		}
	}
	return exts, types
}()

// ForEntry finds the format of an entry from its header alone, so the history
// list can say what is previewable without downloading anything. A named
// entry is a file and is known by its extension; an unnamed one is a
// clipboard copy and is known by its media type.
func ForEntry(filename, contentType string) (Format, bool) {
	if filename != "" {
		f, ok := byExt[strings.ToLower(path.Ext(filename))]
		return f, ok
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return Format{}, false
	}
	if f, ok := byType[mt]; ok {
		return f, true
	}
	if strings.HasPrefix(mt, "text/") {
		return clipboardText, true
	}
	return Format{}, false
}

func head(b []byte) []byte {
	if len(b) > sniffLen {
		return b[:sniffLen]
	}
	return b
}

func prefix(sig string) func([]byte) bool {
	return func(b []byte) bool { return bytes.HasPrefix(b, []byte(sig)) }
}

func anyPrefix(sigs ...string) func([]byte) bool {
	return func(b []byte) bool {
		for _, s := range sigs {
			if bytes.HasPrefix(b, []byte(s)) {
				return true
			}
		}
		return false
	}
}

// riff matches a RIFF container of the given form: WAVE, WEBP.
func riff(form string) func([]byte) bool {
	return func(b []byte) bool {
		return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == form
	}
}

// isISOBMFF matches the MP4 family: a first box of type ftyp.
func isISOBMFF(b []byte) bool {
	return len(b) >= 12 && string(b[4:8]) == "ftyp"
}

// ftypBrand matches an ISO BMFF file whose major or compatible brands include
// one of brands — which is how AVIF tells itself apart from MP4.
func ftypBrand(brands ...string) func([]byte) bool {
	return func(b []byte) bool {
		if !isISOBMFF(b) {
			return false
		}
		size := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
		if size < 16 || size > len(b) {
			size = min(len(b), 64)
		}
		for i := 8; i+4 <= size; i += 4 {
			if i == 12 {
				continue // the minor version, not a brand
			}
			for _, want := range brands {
				if string(b[i:i+4]) == want {
					return true
				}
			}
		}
		return false
	}
}

// isQuickTime matches a .mov: an ftyp box, or one of the top-level atoms
// older QuickTime files open with instead.
func isQuickTime(b []byte) bool {
	if isISOBMFF(b) {
		return true
	}
	if len(b) < 8 {
		return false
	}
	switch string(b[4:8]) {
	case "moov", "mdat", "wide", "free", "skip", "pnot":
		return true
	}
	return false
}

// isWebM matches an EBML document whose DocType is webm. Matroska shares the
// header, and an .mkv is not something every browser plays.
func isWebM(b []byte) bool {
	h := head(b)
	return bytes.HasPrefix(h, []byte("\x1a\x45\xdf\xa3")) && bytes.Contains(h[:min(len(h), 64)], []byte("webm"))
}

// isMP3 matches an ID3 tag or a bare MPEG audio frame header: eleven sync
// bits and a layer that is not the reserved zero (which is ADTS, not MP3).
func isMP3(b []byte) bool {
	if bytes.HasPrefix(b, []byte("ID3")) {
		return true
	}
	return len(b) >= 2 && b[0] == 0xff && b[1]&0xe0 == 0xe0 && (b[1]>>1)&0x3 != 0
}

// isADTS matches a raw AAC stream: twelve sync bits and layer zero, possibly
// behind an ID3 tag.
func isADTS(b []byte) bool {
	if bytes.HasPrefix(b, []byte("ID3")) {
		return true
	}
	return len(b) >= 2 && b[0] == 0xff && b[1]&0xf6 == 0xf0
}

// isText reports whether the start of b reads as UTF-8 with no NUL. A rune
// cut by the sniff window is not held against it.
func isText(b []byte) bool {
	h := head(b)
	if bytes.IndexByte(h, 0) >= 0 {
		return false
	}
	if len(b) > sniffLen {
		// Trim a rune the window cut in half.
		for i := 0; i < utf8.UTFMax && len(h) > 0 && !utf8.Valid(h); i++ {
			h = h[:len(h)-1]
		}
	}
	return utf8.Valid(h)
}

// isSVG matches text whose first element is an svg root, after any XML
// declaration, doctype or comment.
func isSVG(b []byte) bool {
	if !isText(b) {
		return false
	}
	h := bytes.ToLower(head(b))
	i := bytes.Index(h, []byte("<svg"))
	if i < 0 {
		return false
	}
	// Everything before the root has to be prologue: a page that merely
	// mentions an svg somewhere is not one.
	before := bytes.TrimSpace(bytes.TrimPrefix(h[:i], []byte("\xef\xbb\xbf")))
	for len(before) > 0 {
		var end []byte
		switch {
		case bytes.HasPrefix(before, []byte("<?")):
			end = []byte("?>")
		case bytes.HasPrefix(before, []byte("<!--")):
			end = []byte("-->")
		case bytes.HasPrefix(before, []byte("<!doctype")):
			end = []byte(">")
		default:
			return false
		}
		j := bytes.Index(before, end)
		if j < 0 {
			return false
		}
		before = bytes.TrimSpace(before[j+len(end):])
	}
	return true
}
