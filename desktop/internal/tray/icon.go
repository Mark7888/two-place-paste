//go:build windows || darwin

package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"sync"
)

// The icon is drawn rather than shipped.
//
// A tray glyph at this size is a rectangle and a fold — twenty lines of
// image/draw against committed blobs nobody can review in a diff, and it is
// deliberately not the app icon (assets/icon), which does not survive being
// drawn this small in one colour. Each ink renders once, on first use.
const iconSize = 32

// The two inks. macOS takes the black one as a template and recolours it
// itself; Windows does not, so it gets whichever one contrasts with the
// taskbar (icon_windows.go).
var (
	inkDark  = color.NRGBA{A: 255}
	inkLight = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
)

type iconSet struct {
	png, ico []byte
}

var (
	iconMu    sync.Mutex
	iconCache = map[color.NRGBA]iconSet{}
)

// icons returns the glyph in one ink, as a PNG and as an .ico.
func icons(ink color.NRGBA) iconSet {
	iconMu.Lock()
	defer iconMu.Unlock()
	if set, ok := iconCache[ink]; ok {
		return set
	}
	pngBytes := drawIcon(ink)
	set := iconSet{png: pngBytes, ico: wrapICO(pngBytes)}
	iconCache[ink] = set
	return set
}

// drawIcon renders a clipboard outline in ink with a transparent ground.
func drawIcon(ink color.NRGBA) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, iconSize, iconSize))

	const (
		left, right = 6, 26
		top, bottom = 4, 28
		stroke      = 2
	)
	fill := func(x0, y0, x1, y1 int) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				img.SetNRGBA(x, y, ink)
			}
		}
	}
	// Body outline.
	fill(left, top, right, top+stroke)
	fill(left, bottom-stroke, right, bottom)
	fill(left, top, left+stroke, bottom)
	fill(right-stroke, top, right, bottom)
	// The clip at the head.
	fill(left+5, top-2, right-5, top+4)
	// Two lines of "content".
	fill(left+4, top+10, right-4, top+12)
	fill(left+4, top+16, right-6, top+18)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		// Encoding a in-memory NRGBA cannot fail; an icon is not worth a
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
