//go:build !windows

package main

import "fmt"

// The tray is Windows-only for now, ON PURPOSE: a macOS menu-bar item wants
// Cocoa (cgo) and really wants a proper .app bundle - both land with the Mac
// release pass. Keeping it out of the non-Windows builds also keeps the
// darwin/linux agents pure Go, so the ubuntu release runner can cross-compile
// them.
func trayCmd() error {
	return fmt.Errorf("the tray is Windows-only for now - use `unitrise-gate ui` for the dashboard")
}
