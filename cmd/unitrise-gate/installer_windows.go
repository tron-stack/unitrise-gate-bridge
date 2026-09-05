//go:build windows

package main

// The built-in installer: the ONE downloaded exe installs itself. Double-
// click → confirm → UAC → it copies itself into Program Files, registers the
// service and the login tray icon, adds the Add/Remove Programs entry, starts
// everything, and opens the dashboard (whose setup-mode pairing form takes it
// from there). Double-clicking an already-installed copy just opens the
// dashboard. `unitrise-gate install` / `uninstall` are the same flows from a
// terminal (self-elevating when interactive).
//
// Everything here is idempotent: re-running the installer over an existing
// install is the update path (stop service, replace exe, start service),
// and the pairing is never touched.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/service"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/ui"
)

const (
	installDirName   = "UnitRise Gate Bridge"
	installedExeName = "unitrise-gate.exe"
	trayRunValue     = "UnitRiseGateBridgeTray"
	runKeyPath       = `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`
	arpKeyPath       = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\UnitRiseGateBridge`
	envKeyPath       = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
)

func installDir() string {
	base := os.Getenv("ProgramFiles")
	if base == "" {
		base = `C:\Program Files`
	}
	return filepath.Join(base, installDirName)
}
func installedExe() string { return filepath.Join(installDir(), installedExeName) }

func isElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

const (
	mbOK           = 0x0
	mbYesNo        = 0x4
	mbIconError    = 0x10
	mbIconQuestion = 0x20
	mbIconInfo     = 0x40
	idYes          = 6
)

func msgBox(text string, flags uint32) int32 {
	u32 := windows.NewLazySystemDLL("user32.dll")
	t, _ := windows.UTF16PtrFromString("UnitRise Gate Bridge")
	x, _ := windows.UTF16PtrFromString(text)
	r, _, _ := u32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(x)), uintptr(unsafe.Pointer(t)), uintptr(flags))
	return int32(r)
}

// report speaks the right language for how we were launched: terminal =
// print, double-click/UAC = message box.
func report(text string, errKind bool) {
	if hasConsole {
		if errKind {
			fmt.Fprintln(os.Stderr, "error:", text)
		} else {
			fmt.Println(text)
		}
		return
	}
	flags := uint32(mbOK | mbIconInfo)
	if errKind {
		flags = mbOK | mbIconError
	}
	msgBox(text, flags)
}

func relaunchElevated(arg string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	args, _ := windows.UTF16PtrFromString(arg)
	return windows.ShellExecute(0, verb, file, args, nil, windows.SW_NORMAL)
}

// guiEntry is the double-click path (GUI subsystem, no console, no args).
func guiEntry() {
	exe, _ := os.Executable()
	if strings.EqualFold(exe, installedExe()) || service.Installed() {
		// Already installed: the program's face is the tray + dashboard.
		openBrowser(fmt.Sprintf("http://127.0.0.1:%d", ui.DefaultPort))
		return
	}
	if msgBox("Install UnitRise Gate Bridge on this computer?\n\nIt runs as a background service that keeps your gate software's code list in sync with UnitRise, with a status icon by the clock. You'll enter your pairing credentials on the setup page that opens after install.", mbYesNo|mbIconQuestion) != idYes {
		return
	}
	if !isElevated() {
		if err := relaunchElevated("install"); err != nil {
			report("Couldn't request administrator rights: "+err.Error(), true)
		}
		return
	}
	installCmd() //nolint:errcheck - installCmd reports its own outcome
}

// installCmd installs (or updates) in place. Self-elevates when needed.
func installCmd() error {
	if !isElevated() {
		if !hasConsole {
			return relaunchElevated("install")
		}
		return fmt.Errorf("install needs an Administrator terminal (or just double-click the exe)")
	}
	if err := installSelf(); err != nil {
		report("Install failed: "+err.Error(), true)
		return errQuiet
	}
	report("UnitRise Gate Bridge is installed and running.\n\nLook for the UnitRise icon by the clock - the setup page that just opened walks you through connecting (or shows the live status if this machine was already paired).", false)
	return nil
}

// errQuiet: the outcome was already reported in the right medium - main()
// must not double-print it.
var errQuiet = fmt.Errorf("")

func installSelf() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	dest := installedExe()

	// An update needs the exe unlocked: stop the service, close any trays.
	wasInstalled := service.Installed()
	if wasInstalled {
		_ = service.Stop()
	}
	killOtherInstances()

	if err := os.MkdirAll(installDir(), 0o755); err != nil {
		return err
	}
	if !strings.EqualFold(self, dest) {
		if err := copyFileRetry(self, dest); err != nil {
			return fmt.Errorf("copying into %s: %w", installDir(), err)
		}
	}

	if err := addMachinePath(installDir()); err != nil {
		// PATH is a convenience - never fail the install over it.
		fmt.Fprintln(os.Stderr, "warning: PATH:", err)
	}

	if !wasInstalled {
		// Register via the INSTALLED copy - the service must point at
		// Program Files, not at wherever the installer was downloaded.
		if out, err := exec.Command(dest, "service", "install").CombinedOutput(); err != nil {
			return fmt.Errorf("service install: %v (%s)", err, strings.TrimSpace(string(out)))
		}
	}
	if err := service.Start(); err != nil {
		return fmt.Errorf("service start: %w", err)
	}

	// Tray at login, for every user of this shared site PC.
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, runKeyPath, registry.SET_VALUE); err == nil {
		k.SetStringValue(trayRunValue, `"`+dest+`" tray`) //nolint:errcheck
		k.Close()
	}
	// Add/Remove Programs entry, so the machine's software inventory is
	// honest and removal is a normal Windows act.
	if k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, arpKeyPath, registry.SET_VALUE); err == nil {
		k.SetStringValue("DisplayName", "UnitRise Gate Bridge")   //nolint:errcheck
		k.SetStringValue("DisplayVersion", api.AgentVersion)      //nolint:errcheck
		k.SetStringValue("Publisher", "MyTruckYards LLC")         //nolint:errcheck
		k.SetStringValue("DisplayIcon", dest)                     //nolint:errcheck
		k.SetStringValue("InstallLocation", installDir())         //nolint:errcheck
		k.SetStringValue("UninstallString", `"`+dest+`" uninstall`) //nolint:errcheck
		k.SetDWordValue("NoModify", 1)                            //nolint:errcheck
		k.SetDWordValue("NoRepair", 1)                            //nolint:errcheck
		k.Close()
	}

	exec.Command(dest, "tray").Start() //nolint:errcheck - best-effort; login launches it anyway
	// The dashboard is where setup continues (pairing form when unpaired).
	openBrowser(fmt.Sprintf("http://127.0.0.1:%d", ui.DefaultPort))
	return nil
}

func uninstallCmd() error {
	if !isElevated() {
		if !hasConsole {
			return relaunchElevated("uninstall")
		}
		return fmt.Errorf("uninstall needs an Administrator terminal")
	}
	if !hasConsole && msgBox("Remove UnitRise Gate Bridge from this computer?\n\nThe pairing is kept (reinstalling picks up where it left off), and the last code file written for the gate software stays - the gate keeps admitting from its current list.", mbYesNo|mbIconQuestion) != idYes {
		return nil
	}
	_ = service.Stop()
	_ = service.Uninstall()
	killOtherInstances()
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, runKeyPath, registry.SET_VALUE); err == nil {
		k.DeleteValue(trayRunValue) //nolint:errcheck
		k.Close()
	}
	registry.DeleteKey(registry.LOCAL_MACHINE, arpKeyPath) //nolint:errcheck
	removeMachinePath(installDir())

	// The running exe can't delete itself - hand the directory removal to a
	// detached cmd that waits for this process to exit first.
	cmd := exec.Command("cmd", "/C", "ping -n 3 127.0.0.1 > nul & rd /s /q \""+installDir()+"\"")
	cmd.Dir = os.TempDir()
	cmd.Start() //nolint:errcheck
	report("UnitRise Gate Bridge was removed. The pairing was kept under ProgramData - delete that folder too if this machine is being retired.", false)
	return nil
}

// killOtherInstances closes every OTHER unitrise-gate.exe (tray icons, a
// stray foreground run) so the installed exe isn't file-locked. The service
// was already stopped through SCM; taskkill's PID filter spares ourselves.
func killOtherInstances() {
	exec.Command("taskkill", "/F",
		"/FI", "IMAGENAME eq "+installedExeName,
		"/FI", fmt.Sprintf("PID ne %d", os.Getpid()),
	).Run() //nolint:errcheck
	time.Sleep(300 * time.Millisecond)
}

func copyFileRetry(src, dst string) error {
	var last error
	for i := 0; i < 5; i++ {
		if last = copyFile(src, dst); last == nil {
			return nil
		}
		time.Sleep(400 * time.Millisecond) // service/tray still letting go
	}
	return last
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func addMachinePath(dir string) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, envKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	cur, _, err := k.GetStringValue("Path")
	if err != nil {
		return err
	}
	for _, p := range strings.Split(cur, ";") {
		if strings.EqualFold(strings.TrimSpace(p), dir) {
			return nil
		}
	}
	return k.SetStringValue("Path", strings.TrimRight(cur, ";")+";"+dir)
}

func removeMachinePath(dir string) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, envKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	cur, _, err := k.GetStringValue("Path")
	if err != nil {
		return
	}
	parts := strings.Split(cur, ";")
	kept := parts[:0]
	for _, p := range parts {
		if !strings.EqualFold(strings.TrimSpace(p), dir) {
			kept = append(kept, p)
		}
	}
	k.SetStringValue("Path", strings.Join(kept, ";")) //nolint:errcheck
}
