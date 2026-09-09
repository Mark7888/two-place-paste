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
// A committed .png and .ico would be two binary files in a repository that
// otherwise contains none, and a menu-bar glyph at this size is a rectangle
// and a fold — twenty lines of image/draw against two blobs nobody can review
// in a diff. It renders once, on first use.
const iconSize = 32

var (
	iconOnce sync.Once
	iconPNG  []byte
	iconICO  []byte
)

func icons() ([]byte, []byte) {
	iconOnce.Do(func() {
		iconPNG = drawIcon()
		iconICO = wrapICO(iconPNG)
	})
	return iconPNG, iconICO
}

// drawIcon renders a clipboard outline in black with a transparent ground,
// which is what macOS wants from a template image and what Windows renders
// acceptably against either theme.
func drawIcon() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, iconSize, iconSize))
	ink := color.NRGBA{A: 255}

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
