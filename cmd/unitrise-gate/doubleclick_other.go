//go:build !windows

package main

// Only Windows Explorer produces the flash-and-vanish console double-click
// this exists for; everywhere else the answer is simply no.
func launchedByDoubleClick() bool { return false }
