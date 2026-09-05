// gen-trayicons draws the UnitRise hexagon mark (the dashboard header's
// logo: dark outer hexagon ring, filled inner hexagon) into the tray icon
// assets embedded by internal/trayicon. Three states, colored by the inner
// fill: ok (amber), warn (red), off (gray).
//
// Run from the repo root when the mark or palette changes:
//   go run ./tools/gen-trayicons
// Outputs internal/trayicon/assets/*.ico (Windows, PNG-compressed entries at
// 16/24/32) and *.png (22px, macOS menu bar / Linux).
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// Rise palette (mirrors internal/ui/index.html).
var (
	ink   = color.NRGBA{0x11, 0x13, 0x22, 0xFF}
	amber = color.NRGBA{0xF5, 0x9E, 0x0B, 0xFF}
	red   = color.NRGBA{0xEF, 0x44, 0x44, 0xFF}
	gray  = color.NRGBA{0x8A, 0x8D, 0xA3, 0xFF}
)

// pointInHex reports whether (x, y) lies inside a regular flat-top hexagon
// centered at (cx, cy) with circumradius r (vertices at top and bottom,
// matching the dashboard SVG's point layout).
func pointInHex(x, y, cx, cy, r float64) bool {
	// Vertices every 60 degrees starting from straight up.
	var vx, vy [6]float64
	for i := 0; i < 6; i++ {
		a := math.Pi/2 + float64(i)*math.Pi/3
		vx[i] = cx + r*math.Cos(a)
		vy[i] = cy - r*math.Sin(a)
	}
	// Point-in-convex-polygon: consistent cross-product sign for all edges.
	sign := 0.0
	for i := 0; i < 6; i++ {
		j := (i + 1) % 6
		cross := (vx[j]-vx[i])*(y-vy[i]) - (vy[j]-vy[i])*(x-vx[i])
		if cross != 0 {
			if sign == 0 {
				sign = cross
			} else if (cross > 0) != (sign > 0) {
				return false
			}
		}
	}
	return true
}

// render draws the mark at size px with 8x supersampling for smooth edges.
func render(size int, fill color.NRGBA) *image.NRGBA {
	const ss = 8
	big := size * ss
	cx, cy := float64(big)/2, float64(big)/2
	outer := float64(big) * 0.48
	ringInner := float64(big) * 0.38 // outer ring thickness
	inner := float64(big) * 0.28     // the state-colored core

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var rs, gs, bs, as, n float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := float64(px*ss+sx) + 0.5
					y := float64(py*ss+sy) + 0.5
					var c color.NRGBA
					switch {
					case pointInHex(x, y, cx, cy, inner):
						c = fill
					case pointInHex(x, y, cx, cy, ringInner):
						c = color.NRGBA{} // gap between ring and core
					case pointInHex(x, y, cx, cy, outer):
						c = ink
					default:
						c = color.NRGBA{}
					}
					rs += float64(c.R) * float64(c.A)
					gs += float64(c.G) * float64(c.A)
					bs += float64(c.B) * float64(c.A)
					as += float64(c.A)
					n++
				}
			}
			if as > 0 {
				img.SetNRGBA(px, py, color.NRGBA{
					R: uint8(rs / as), G: uint8(gs / as), B: uint8(bs / as), A: uint8(as / n),
				})
			}
		}
	}
	return img
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
		b.WriteByte(w) // width
		b.WriteByte(w) // height
		b.WriteByte(0) // palette colors
		b.WriteByte(0) // reserved
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
	states := map[string]color.NRGBA{"ok": amber, "warn": red, "off": gray}
	for name, fill := range states {
		// Windows: multi-size ico.
		sizes := map[int][]byte{}
		for _, s := range []int{16, 24, 32} {
			sizes[s] = pngBytes(render(s, fill))
		}
		if err := os.WriteFile(filepath.Join(outDir, name+".ico"), ico(sizes, []int{16, 24, 32}), 0o644); err != nil {
			panic(err)
		}
		// macOS menu bar / Linux: single 22px png.
		if err := os.WriteFile(filepath.Join(outDir, name+".png"), pngBytes(render(22, fill)), 0o644); err != nil {
			panic(err)
		}
		fmt.Println("wrote", name+".ico", "+", name+".png")
	}
}
