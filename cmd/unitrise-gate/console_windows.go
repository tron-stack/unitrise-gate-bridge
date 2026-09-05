//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// The Windows exe is built -H=windowsgui (one binary is installer + agent +
// tray, and a GUI-subsystem image never flashes a console at login or on
// double-click). The cost: a GUI image gets no console even when launched
// FROM one - so CLI commands would print nothing. attachParentConsole glues
// the process back onto the parent terminal's console and reopens the std
// handles into it.
//
// Returns whether a parent console existed - which is also the cleanest
// launched-by-double-click signal: Explorer gives a GUI process NO console
// at all, a terminal gives it one to attach to, and the service/tray paths
// never reach the no-args branch that consults this.
func attachParentConsole() bool {
	const attachParent = ^uint32(0) // ATTACH_PARENT_PROCESS
	k32 := windows.NewLazySystemDLL("kernel32.dll")
	r, _, _ := k32.NewProc("AttachConsole").Call(uintptr(attachParent))
	if r == 0 {
		return false
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = f
		os.Stderr = f
	}
	if f, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = f
	}
	return true
}
