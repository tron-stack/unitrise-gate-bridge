// Package brand holds the UnitRise identity assets the agent ships with.
//
// falcon-mark.png is the alpha-matted low-poly peregrine cut out of the
// UnitRise logo (copied from the platform's brand kit, tp-front-vite
// src/assets/falcon-mark.png - regenerate THERE, then re-copy; never edit
// the pixels here). It is the source for the tray/app icons
// (tools/gen-trayicons) and is served by the local dashboard and the
// desktop window so every surface shows the same bird.
//
// The palette constants mirror the platform brand kit v2
// (tp-front-vite/src/brand/unitrise.js, measured from the logo itself).
// Two-gold rule: Gold is a FILL (the mark, CTAs with navy text on top),
// Bronze is gold used as TEXT or under white text. Keep in sync by hand -
// this repo stays isolated from platform code on purpose.
package brand

import _ "embed"

//go:embed falcon-mark.png
var FalconMark []byte

const (
	Paper    = "#FAF8F5" // page canvas - warm, deliberately not white
	Card     = "#FFFFFF"
	Line     = "#E9E5DC" // hairline borders
	Ink      = "#131F35" // headings - the wordmark navy
	Body     = "#3E4A60"
	Muted    = "#6B7085"
	Navy900  = "#071321" // deepest facet - dark bands
	Navy800  = "#0E1A2B"
	Gold     = "#C38B4E" // MEASURED metallic core: fills only, never text
	Bronze   = "#8E6235" // MEASURED wordmark gold: gold as TEXT
	GoldDeep = "#714B27" // small text/icons on gold washes
	GoldWash = "#F6E9D6"
	Success  = "#15803D"
	Danger   = "#B91C1C"
)
