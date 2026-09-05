// Package trayicon embeds the generated tray/menu-bar icons (the UnitRise
// hexagon mark in three states). Regenerate with `go run ./tools/gen-trayicons`
// when the mark or palette changes - never hand-edit the assets.
package trayicon

import (
	_ "embed"
	"runtime"
)

var (
	//go:embed assets/ok.ico
	okICO []byte
	//go:embed assets/warn.ico
	warnICO []byte
	//go:embed assets/off.ico
	offICO []byte
	//go:embed assets/ok.png
	okPNG []byte
	//go:embed assets/warn.png
	warnPNG []byte
	//go:embed assets/off.png
	offPNG []byte
)

func pick(icoBytes, pngBytes []byte) []byte {
	if runtime.GOOS == "windows" {
		return icoBytes
	}
	return pngBytes
}

// OK: agent reachable and healthy (amber core).
func OK() []byte { return pick(okICO, okPNG) }

// Warn: agent reachable but reporting a problem (red core).
func Warn() []byte { return pick(warnICO, warnPNG) }

// Off: agent not reachable - service stopped or not installed (gray core).
func Off() []byte { return pick(offICO, offPNG) }
