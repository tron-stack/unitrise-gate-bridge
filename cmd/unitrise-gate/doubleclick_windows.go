//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
)

// launchedByDoubleClick reports whether this process was started from
// Explorer (double-click) rather than a terminal. Explorer gives the process
// a brand-new console it is the SOLE owner of; launched from
// PowerShell/cmd, the shell is in the console's process list too, and a
// service has no console at all (the call returns 0, which reads as false).
func launchedByDoubleClick() bool {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}
