// gen-trayicons renders the tray + application icons from the real UnitRise
// falcon mark (internal/brand/falcon-mark.png - the alpha-matted low-poly
// peregrine from the platform brand kit), replacing the placeholder hexagon.
//
// Two treatments, per the brand kit's own rule:
//   - tray states (16-32px): a SOLID SILHOUETTE via the mark's alpha channel,
//     colored by state - at tray size the full-color bird (mostly navy) turns
//     to mush and vanishes on dark taskbars; the silhouette is the kit's
//     answer for small/dark chrome. ok = the measured gold, warn = red,
//     off = gray.
//   - app icon (Explorer / shortcuts / Add-Remove Programs, up to 256px):
//     the FULL-COLOR cutout - large enough to read as the actual mark.
//
// Run from the repo root when the mark or palette changes:
//
//	go run ./tools/gen-trayicons
//
// Outputs internal/trayicon/assets/*.ico (Windows, PNG-compressed entries)
// and *.png (22px, macOS menu bar / Linux).
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	xdraw "golang.org/x/image/draw"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/brand"
)

// Tray state colors. ok is the brand's measured gold; warn/off are kept
// bright enough to read on BOTH light and dark taskbars (the kit's AA-on-
// white status colors go muddy on dark chrome at 16px).
var (
	gold = color.NRGBA{0xC3, 0x8B, 0x4E, 0xFF} // brand.Gold
	red  = color.NRGBA{0xEF, 0x44, 0x44, 0xFF}
	gray = color.NRGBA{0x98, 0xA2, 0xB3, 0xFF}
)

func loadMark() *image.NRGBA {
	src, err := png.Decode(bytes.NewReader(brand.FalconMark))
	if err != nil {
		panic(err)
	}
	b := src.Bounds()
	img := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	xdraw.Draw(img, img.Bounds(), src, b.Min, xdraw.Src)
	return img
}

// fit scales the mark to fill a size x size square (small inset so wingtips
// never touch the icon edge), preserving aspect, centered. CatmullRom keeps
// the facet edges clean at tiny sizes.
func fit(mark *image.NRGBA, size int) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	inset := size / 16
	box := size - 2*inset
	mw, mh := mark.Bounds().Dx(), mark.Bounds().Dy()
	w, h := box, box*mh/mw
	if h > box {
		h, w = box, box*mw/mh
	}
	x0 := (size - w) / 2
	y0 := (size - h) / 2
	xdraw.CatmullRom.Scale(out, image.Rect(x0, y0, x0+w, y0+h), mark, mark.Bounds(), xdraw.Over, nil)
	return out
}

// silhouette recolors every pixel to the state color, keeping the mark's
// alpha - the kit's "solid" variant for small or dark chrome.
func silhouette(img *image.NRGBA, c color.NRGBA) *image.NRGBA {
	out := image.NewNRGBA(img.Bounds())
	for i := 0; i < len(img.Pix); i += 4 {
		a := img.Pix[i+3]
		if a == 0 {
			continue
		}
		out.Pix[i+0] = c.R
		out.Pix[i+1] = c.G
		out.Pix[i+2] = c.B
		out.Pix[i+3] = a
	}
	return out
}

func pngBytes(img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// ico wraps PNG-compressed images into a single .ico (PNG entries are valid
// from Windows Vista on, which is everything the agent supports).
func ico(images map[int][]byte, order []int) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(&b, binary.LittleEndian, uint16(1)) // type: icon
	binary.Write(&b, binary.LittleEndian, uint16(len(order)))
	offset := 6 + 16*len(order)
	for _, size := range order {
		data := images[size]
		w := byte(size)
		if size >= 256 {
			w = 0
		}
		b.WriteByte(w)                                    // width
		b.WriteByte(w)                                    // height
		b.WriteByte(0)                                    // palette colors
		b.WriteByte(0)                                    // reserved
		binary.Write(&b, binary.LittleEndian, uint16(1))  // planes
		binary.Write(&b, binary.LittleEndian, uint16(32)) // bit depth
		binary.Write(&b, binary.LittleEndian, uint32(len(data)))
		binary.Write(&b, binary.LittleEndian, uint32(offset))
		offset += len(data)
	}
	for _, size := range order {
		b.Write(images[size])
	}
	return b.Bytes()
}

func main() {
	outDir := filepath.Join("internal", "trayicon", "assets")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		panic(err)
	}
	mark := loadMark()

	states := map[string]color.NRGBA{"ok": gold, "warn": red, "off": gray}
	for name, fill := range states {
		// Windows: multi-size ico of the state-colored silhouette.
		sizes := map[int][]byte{}
		for _, s := range []int{16, 24, 32} {
			sizes[s] = pngBytes(silhouette(fit(mark, s), fill))
		}
		if err := os.WriteFile(filepath.Join(outDir, name+".ico"), ico(sizes, []int{16, 24, 32}), 0o644); err != nil {
			panic(err)
		}
		// macOS menu bar / Linux: single 22px png.
		if err := os.WriteFile(filepath.Join(outDir, name+".png"), pngBytes(silhouette(fit(mark, 22), fill)), 0o644); err != nil {
			panic(err)
		}
		fmt.Println("wrote", name+".ico", "+", name+".png")
	}

	// The application icon - embedded into the Windows exe by the version
	// resource (Makefile winres -icon), so Explorer, the desktop shortcut,
	// and Add/Remove Programs show the falcon itself. Full color: these
	// sizes are large enough for the real mark, and 16/24 fall back to the
	// gold silhouette where the full bird would smear.
	appSizes := map[int][]byte{}
	order := []int{16, 24, 32, 48, 256}
	for _, s := range order {
		if s <= 24 {
			appSizes[s] = pngBytes(silhouette(fit(mark, s), gold))
		} else {
			appSizes[s] = pngBytes(fit(mark, s))
		}
	}
	if err := os.WriteFile(filepath.Join(outDir, "app.ico"), ico(appSizes, order), 0o644); err != nil {
		panic(err)
	}
	fmt.Println("wrote app.ico")
}
