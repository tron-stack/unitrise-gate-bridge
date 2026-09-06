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
	_ "embed"
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

// guiEntry is the double-click path (GUI subsystem, no console, no args):
// the native setup dialog (options + staged progress), never a terminal.
func guiEntry() {
	exe, _ := os.Executable()
	if strings.EqualFold(exe, installedExe()) || service.Installed() {
		// Already installed: double-click opens the control window.
		windowCmd() //nolint:errcheck - falls back to the browser internally
		return
	}
	proceed, opts := setupDialog()
	if !proceed {
		return
	}
	if !isElevated() {
		// The elevated leg re-enters through `install --gui` carrying the
		// choices; UAC is the only prompt between the dialog and progress.
		if err := relaunchElevated(installArgs(opts)); err != nil {
			report("Couldn't request administrator rights: "+err.Error(), true)
		}
		return
	}
	runGuiInstall(opts)
}

func installArgs(opts installOpts) string {
	args := "install --gui"
	if opts.DesktopShortcut {
		args += " --shortcut"
	}
	if opts.OpenGuide {
		args += " --guide"
	}
	return args
}

// runGuiInstall: staged progress window around the real work, then the
// after-party (guide, dashboard, confirmation).
func runGuiInstall(opts installOpts) {
	err := progressDialog(func(rep func(int, string)) error {
		return installSelf(opts, rep)
	})
	if err != nil {
		return // progressDialog already showed the error box
	}
	if opts.OpenGuide {
		exec.Command("notepad", filepath.Join(installDir(), "README.txt")).Start() //nolint:errcheck
	}
	msgBox("UnitRise Gate Bridge is installed and running.\n\nLook for the UnitRise icon by the clock - the setup page that just opened walks you through connecting (or shows live status if this machine was already paired).", mbOK|mbIconInfo)
}

// installCmd installs (or updates) in place. Self-elevates when needed;
// flags (--gui --shortcut --guide) carry the dialog's choices across the
// UAC relaunch.
func installCmd() error {
	opts := installOpts{}
	gui := false
	for _, a := range os.Args[2:] {
		switch a {
		case "--gui":
			gui = true
		case "--shortcut":
			opts.DesktopShortcut = true
		case "--guide":
			opts.OpenGuide = true
		}
	}
	if !isElevated() {
		if !hasConsole {
			return relaunchElevated(installArgs(opts))
		}
		return fmt.Errorf("install needs an Administrator terminal (or just double-click the exe)")
	}
	if gui && !hasConsole {
		runGuiInstall(opts)
		return nil
	}
	if err := installSelf(opts, func(pct int, msg string) { fmt.Printf("  [%3d%%] %s\n", pct, msg) }); err != nil {
		report("Install failed: "+err.Error(), true)
		return errQuiet
	}
	report("UnitRise Gate Bridge is installed and running.\n\nLook for the UnitRise icon by the clock - the setup page that just opened walks you through connecting (or shows the live status if this machine was already paired).", false)
	return nil
}

// errQuiet: the outcome was already reported in the right medium - main()
// must not double-print it.
var errQuiet = fmt.Errorf("")

//go:embed readme_operator.txt
var operatorReadme []byte

func desktopShortcutPath() string {
	pub := os.Getenv("PUBLIC")
	if pub == "" {
		pub = `C:\Users\Public`
	}
	return filepath.Join(pub, "Desktop", "UnitRise Gate Bridge.lnk")
}

func startMenuShortcutPath() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, `Microsoft\Windows\Start Menu\Programs`, "UnitRise Gate Bridge.lnk")
}

// createShortcut writes a .lnk via WScript.Shell - the one COM dance not
// worth hand-rolling ole32 syscalls for. PowerShell is on every supported
// Windows; failure is a warning, never a failed install.
func createShortcut(lnkPath, target, desc string) error {
	script := fmt.Sprintf(
		"$ws = New-Object -ComObject WScript.Shell; $s = $ws.CreateShortcut('%s'); $s.TargetPath = '%s'; $s.WorkingDirectory = '%s'; $s.Description = '%s'; $s.Save()",
		lnkPath, target, filepath.Dir(target), desc)
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func installSelf(opts installOpts, report func(pct int, msg string)) error {
	if report == nil {
		report = func(int, string) {}
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	dest := installedExe()

	// An update needs the exe unlocked: stop the service, close any trays.
	report(8, "Preparing (stopping any running copy)…")
	wasInstalled := service.Installed()
	if wasInstalled {
		_ = service.Stop()
	}
	killOtherInstances()

	report(30, "Copying program files…")
	if err := os.MkdirAll(installDir(), 0o755); err != nil {
		return err
	}
	if !strings.EqualFold(self, dest) {
		if err := copyFileRetry(self, dest); err != nil {
			return fmt.Errorf("copying into %s: %w", installDir(), err)
		}
	}
	// The operator quick-start, and the target of "Open the guide when done".
	os.WriteFile(filepath.Join(installDir(), "README.txt"), operatorReadme, 0o644) //nolint:errcheck
	if err := addMachinePath(installDir()); err != nil {
		// PATH is a convenience - never fail the install over it.
		fmt.Fprintln(os.Stderr, "warning: PATH:", err)
	}

	report(52, "Registering the background service…")
	if !wasInstalled {
		// Register via the INSTALLED copy - the service must point at
		// Program Files, not at wherever the installer was downloaded.
		if out, err := exec.Command(dest, "service", "install").CombinedOutput(); err != nil {
			return fmt.Errorf("service install: %v (%s)", err, strings.TrimSpace(string(out)))
		}
	}

	report(70, "Starting the service and status icon…")
	if err := service.Start(); err != nil {
		return fmt.Errorf("service start: %w", err)
	}
	// Tray at login, for every user of this shared site PC.
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, runKeyPath, registry.SET_VALUE); err == nil {
		k.SetStringValue(trayRunValue, `"`+dest+`" tray`) //nolint:errcheck
		k.Close()
	}
	exec.Command(dest, "tray").Start() //nolint:errcheck - best-effort; login launches it anyway

	report(86, "Creating shortcuts…")
	// Start Menu always - it's how Windows software says "I'm installed".
	if err := createShortcut(startMenuShortcutPath(), dest, "UnitRise Gate Bridge - gate code sync status"); err != nil {
		fmt.Fprintln(os.Stderr, "warning: start-menu shortcut:", err)
	}
	if opts.DesktopShortcut {
		if err := createShortcut(desktopShortcutPath(), dest, "UnitRise Gate Bridge - gate code sync status"); err != nil {
			fmt.Fprintln(os.Stderr, "warning: desktop shortcut:", err)
		}
	}

	report(94, "Finishing…")
	// Add/Remove Programs entry, so the machine's software inventory is
	// honest and removal is a normal Windows act.
	if k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, arpKeyPath, registry.SET_VALUE); err == nil {
		k.SetStringValue("DisplayName", "UnitRise Gate Bridge")     //nolint:errcheck
		k.SetStringValue("DisplayVersion", api.AgentVersion)        //nolint:errcheck
		k.SetStringValue("Publisher", "UnitRise")                   //nolint:errcheck
		k.SetStringValue("DisplayIcon", dest)                       //nolint:errcheck
		k.SetStringValue("InstallLocation", installDir())           //nolint:errcheck
		k.SetStringValue("UninstallString", `"`+dest+`" uninstall`) //nolint:errcheck
		k.SetDWordValue("NoModify", 1)                              //nolint:errcheck
		k.SetDWordValue("NoRepair", 1)                              //nolint:errcheck
		k.Close()
	}
	// Setup continues in the control window (pairing form when unpaired).
	exec.Command(dest, "window").Start() //nolint:errcheck
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
	os.Remove(desktopShortcutPath())   //nolint:errcheck
	os.Remove(startMenuShortcutPath()) //nolint:errcheck

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
