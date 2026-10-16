package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"
)

// The icon is drawn rather than shipped.
//
// A tray glyph at this size is a rectangle and a fold — a few dozen lines of
// pixel filling against committed blobs nobody can review in a diff, and it is
// deliberately not the app icon (assets/icon), which does not survive being
// drawn this small in one colour. Each glyph renders once, on first use.
//
// The glyph is a clipboard, and each status draws it differently rather than
// adding a coloured badge: macOS recolours a template image as a whole, so
// colour cannot carry the status there, and shape reads on both.
//
//   - connected: the clipboard with two lines of content
//   - uploading: one up arrow in place of the lines
//   - downloading: one down arrow in place of the lines
//   - syncing (both at once): an up and a down arrow side by side
//   - skipped: connected, with an exclamation badge at the bottom right
//   - connecting: faded, with three dots in place of the lines
//   - unpaired: faded and empty
//   - disconnected and failed: the clipboard struck through
const iconSize = 32

// inkDark is the glyph's ink. macOS takes it as a template and recolours it
// itself; Windows does not, and swaps it for a light one on a dark taskbar
// (icon_windows.go).
var inkDark = color.NRGBA{A: 255}

// fadedAlpha is the opacity of a glyph for a service that is not connected
// yet: present, but visibly not doing anything.
const fadedAlpha = 110

type iconSet struct {
	png, ico []byte
}

type iconKey struct {
	ink    color.NRGBA
	status Status
}

var (
	iconMu    sync.Mutex
	iconCache = map[iconKey]iconSet{}
)

// icons returns the glyph for a status in one ink, as a PNG and as an .ico.
func icons(ink color.NRGBA, s Status) iconSet {
	iconMu.Lock()
	defer iconMu.Unlock()
	key := iconKey{ink, s}
	if set, ok := iconCache[key]; ok {
		return set
	}
	pngBytes := encodePNG(drawGlyph(ink, s))
	set := iconSet{png: pngBytes, ico: wrapICO(pngBytes)}
	iconCache[key] = set
	return set
}

// The clipboard's outline, in pixels of the 32×32 canvas.
const (
	boardLeft, boardRight = 6, 26
	boardTop, boardBottom = 4, 28
	stroke                = 2
)

// drawGlyph renders the clipboard for a status in ink, on a transparent
// ground.
func drawGlyph(ink color.NRGBA, s Status) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, iconSize, iconSize))
	if s == StatusConnecting || s == StatusUnpaired {
		ink.A = fadedAlpha
	}
	fill := func(x0, y0, x1, y1 int) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				img.SetNRGBA(x, y, ink)
			}
		}
	}

	// Body outline.
	fill(boardLeft, boardTop, boardRight, boardTop+stroke)
	fill(boardLeft, boardBottom-stroke, boardRight, boardBottom)
	fill(boardLeft, boardTop, boardLeft+stroke, boardBottom)
	fill(boardRight-stroke, boardTop, boardRight, boardBottom)
	// The clip at the head.
	fill(boardLeft+5, boardTop-2, boardRight-5, boardTop+4)

	switch s {
	case StatusUploading:
		drawArrow(fill, true)
	case StatusDownloading:
		drawArrow(fill, false)
	case StatusSyncing:
		drawArrows(fill)
	case StatusConnecting:
		// Three dots: something is on its way.
		for _, x := range []int{9, 14, 19} {
			fill(x, 16, x+3, 19)
		}
	case StatusUnpaired:
		// Nothing on the board: there is nothing to sync yet.
	default:
		// Two lines of "content".
		fill(boardLeft+4, boardTop+10, boardRight-4, boardTop+12)
		fill(boardLeft+4, boardTop+16, boardRight-6, boardTop+18)
	}

	switch s {
	case StatusDisconnected, StatusFailed:
		strike(img, ink)
	case StatusSkippedUpload, StatusSkippedDownload:
		badge(img, ink)
	}
	return img
}

// drawArrow draws one arrow down the middle of the board, pointing up for an
// upload and down for a download.
func drawArrow(fill func(x0, y0, x1, y1 int), up bool) {
	if up {
		fill(15, 13, 17, 25)
		for i := range 4 {
			fill(15-i, 9+i, 17+i, 10+i)
		}
		return
	}
	fill(15, 9, 17, 21)
	for i := range 4 {
		fill(15-i, 24-i, 17+i, 25-i)
	}
}

// drawArrows draws ⇅ inside the board: an up arrow on the left, a down arrow
// on the right.
func drawArrows(fill func(x0, y0, x1, y1 int)) {
	// Up: the shaft, then a head that widens a pixel each side per row.
	fill(11, 12, 13, 24)
	for i := range 3 {
		fill(11-i, 10+i, 13+i, 11+i)
	}
	// Down: the same, upside down.
	fill(19, 10, 21, 22)
	for i := range 3 {
		fill(19-i, 23-i, 21+i, 24-i)
	}
}

// strike draws a diagonal from the top left to the bottom right, with a
// transparent margin either side so it reads as a line across the board
// rather than a smudge on it. It is the one anti-aliased stroke in the glyph:
// a staircase at this size reads as noise.
func strike(img *image.NRGBA, ink color.NRGBA) {
	const (
		half   = 1.6 // half the stroke's width
		margin = 1.4 // the cleared gap on each side of it
		inset  = 2.5 // how far from the canvas edge it starts and ends
	)
	clamp := func(v float64) float64 { return math.Max(0, math.Min(1, v)) }
	for y := 0; y < iconSize; y++ {
		for x := 0; x < iconSize; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			// Distance to the segment along y = x, capped at each end.
			t := math.Max(inset, math.Min(iconSize-inset, (px+py)/2))
			d := math.Hypot(px-t, py-t)
			line := clamp(half + 0.5 - d)
			gap := clamp(half + margin + 0.5 - d)
			if gap == 0 {
				continue
			}
			c := img.NRGBAAt(x, y)
			// Clear what is under the gap, then lay the line over it.
			under := float64(c.A) / 255 * (1 - gap)
			alpha := line*float64(ink.A)/255 + under*(1-line)
			if alpha == 0 {
				img.SetNRGBA(x, y, color.NRGBA{})
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{R: ink.R, G: ink.G, B: ink.B, A: uint8(math.Round(alpha * 255))})
		}
	}
}

// badge draws a filled disc with an exclamation mark cut out of it over the
// board's bottom right corner, with a cleared ring round it so it reads as a
// badge and not as part of the board. Like strike, its edges are
// anti-aliased.
func badge(img *image.NRGBA, ink color.NRGBA) {
	const (
		cx, cy = 24.0, 24.0 // the disc's centre
		disc   = 6.2        // its radius
		ring   = 1.6        // the cleared gap round it
	)
	clamp := func(v float64) float64 { return math.Max(0, math.Min(1, v)) }
	// The exclamation mark, knocked out of the disc: a bar and a dot.
	mark := func(x, y int) bool {
		return x >= 23 && x < 25 && ((y >= 20 && y < 25) || (y >= 26 && y < 28))
	}
	for y := 0; y < iconSize; y++ {
		for x := 0; x < iconSize; x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			gap := clamp(disc + ring + 0.5 - d)
			if gap == 0 {
				continue
			}
			fill := clamp(disc + 0.5 - d)
			if mark(x, y) {
				fill = 0
			}
			c := img.NRGBAAt(x, y)
			under := float64(c.A) / 255 * (1 - gap)
			alpha := fill*float64(ink.A)/255 + under*(1-fill)
			img.SetNRGBA(x, y, color.NRGBA{R: ink.R, G: ink.G, B: ink.B, A: uint8(math.Round(alpha * 255))})
		}
	}
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		// Encoding an in-memory NRGBA cannot fail; an icon is not worth a
		// panic in a service that otherwise runs fine without one.
		return nil
	}
	return buf.Bytes()
}

// wrapICO puts a PNG inside a single-image .ico container, which is the format
// the Windows notification area wants. PNG-compressed icons have been
// supported since Windows Vista.
func wrapICO(pngBytes []byte) []byte {
	if len(pngBytes) == 0 {
		return nil
	}
	var buf bytes.Buffer
	write := func(v any) { _ = binary.Write(&buf, binary.LittleEndian, v) }

	write(uint16(0)) // reserved
	write(uint16(1)) // type: icon
	write(uint16(1)) // one image
	buf.WriteByte(iconSize)
	buf.WriteByte(iconSize)
	buf.WriteByte(0)  // palette colours: none
	buf.WriteByte(0)  // reserved
	write(uint16(1))  // colour planes
	write(uint16(32)) // bits per pixel
	write(uint32(len(pngBytes)))
	write(uint32(6 + 16)) // offset: file header plus one directory entry
	buf.Write(pngBytes)
	return buf.Bytes()
}
