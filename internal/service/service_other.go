//go:build !windows

// Non-Windows: no SCM. macOS installs use launchd - `service install` writes
// a LaunchDaemon plist (matching storEDGE's new Mac-compatible installer).
package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

const Name = "com.unitrise.gatebridge"

func IsWindowsService() bool { return false }

func RunAsService(run func(ctx context.Context)) error {
	return fmt.Errorf("not a Windows service context - use `run`")
}

const plistPath = "/Library/LaunchDaemons/" + Name + ".plist"

func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict></plist>
`, Name, exe)
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write %s (need sudo): %w", plistPath, err)
	}
	return exec.Command("launchctl", "load", "-w", plistPath).Run()
}

func Uninstall() error {
	exec.Command("launchctl", "unload", "-w", plistPath).Run()
	return os.Remove(plistPath)
}

func Start() error { return exec.Command("launchctl", "start", Name).Run() }
func Stop() error  { return exec.Command("launchctl", "stop", Name).Run() }

// Installed is only meaningful on Windows (the built-in installer).
func Installed() bool { return false }
