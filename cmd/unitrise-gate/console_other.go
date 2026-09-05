//go:build !windows

package main

// Non-Windows builds are ordinary console binaries - there is always a
// terminal (or a service supervisor's pipes) behind stdout.
func attachParentConsole() bool { return true }
