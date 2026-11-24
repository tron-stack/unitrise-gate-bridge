//go:build !windows

package main

import (
	"fmt"
	"os"
)

// The built-in installer is a Windows thing (that's where the double-click
// lives). macOS/Linux install via install scripts / `service install`; a
// proper Mac .app + pkg lands with the Mac release pass.
func guiEntry() {}

var errQuiet = fmt.Errorf("")

// Non-Windows builds always have a console - reporting is plain printing.
func report(text string, errKind bool) {
	if errKind {
		fmt.Fprintln(os.Stderr, "error:", text)
	} else {
		fmt.Println(text)
	}
}

// isElevated: running as root (what can read and write the root-owned config
// folder).
func isElevated() bool { return os.Geteuid() == 0 }

func installCmd() error {
	return fmt.Errorf("the built-in installer is Windows-only - use `unitrise-gate pair` + `unitrise-gate service install`")
}

func uninstallCmd() error {
	return fmt.Errorf("the built-in uninstaller is Windows-only - use `unitrise-gate service uninstall`")
}
