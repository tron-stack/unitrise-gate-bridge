//go:build windows

package syncer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// shellCommand builds the consume invocation for cmd.exe.
//
// The consume value is free text from the console - usually a bare batch-file
// name ("Ptisend.bat"), sometimes a full command line with arguments. When the
// resolved value is an actual file whose path contains spaces, it must be
// quoted; `cmd /S /C " … "` is the documented reliable form (outer quotes
// stripped exactly once by /S). Go's default Windows arg escaping through
// exec.Command("cmd", "/C", full) mangles exactly this case, so the raw
// command line is set explicitly.
func shellCommand(ctx context.Context, full string) *exec.Cmd {
	line := quoteIfNeeded(full)
	cmd := exec.CommandContext(ctx, "cmd")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd /S /C "` + line + `"`}
	return cmd
}

func quoteIfNeeded(full string) string {
	if strings.Contains(full, " ") && !strings.Contains(full, `"`) {
		if _, err := os.Stat(full); err == nil {
			return `"` + full + `"`
		}
	}
	return full
}

var (
	k32                        = windows.NewLazySystemDLL("kernel32.dll")
	wtsapi32                   = windows.NewLazySystemDLL("wtsapi32.dll")
	procActiveConsoleSessionId = k32.NewProc("WTSGetActiveConsoleSessionId")
	procPidToSessionId         = k32.NewProc("ProcessIdToSessionId")
	procQueryUserToken         = wtsapi32.NewProc("WTSQueryUserToken")
)

// runConsumeInSession runs the consume command in the SIGNED-IN user's
// desktop session instead of the service's session 0.
//
// Why this exists (Falcon site, 2026-09-06): the consume exited 0 from the
// service yet the Falcon never sent codes to the keypads. PTI's management
// interface (Easy-Link / PTI-MI) is a DESKTOP application living in the
// logged-in session - a sender launched in session 0 can't reach it and may
// no-op "successfully". storEDGE's Gate runs as a desktop app, which is
// exactly why its Ptisend works. So: when we're a service (session 0) and a
// user is signed in at the console, run the command AS that user, in their
// session - the same world the gate software lives in.
//
// ran=false means "not applicable here" (already interactive, nobody signed
// in, or the session launch failed) - the caller falls back to the plain
// session-0 exec, so this is strictly additive.
func runConsumeInSession(full, dir string, timeout time.Duration) (exit int, out string, ran bool) {
	// Already in an interactive session (foreground `run`)? Normal path.
	var mySession uint32
	pid := uint32(os.Getpid())
	if r, _, _ := procPidToSessionId.Call(uintptr(pid), uintptr(unsafe.Pointer(&mySession))); r == 0 || mySession != 0 {
		return 0, "", false
	}
	sid, _, _ := procActiveConsoleSessionId.Call()
	if uint32(sid) == 0xFFFFFFFF || uint32(sid) == 0 {
		return 0, "", false // nobody signed in at the console
	}
	var token windows.Token
	if r, _, _ := procQueryUserToken.Call(sid, uintptr(unsafe.Pointer(&token))); r == 0 {
		return 0, "", false // needs SE_TCB (SYSTEM has it); anything else falls back
	}
	defer token.Close()

	// Output rides a temp file in the save folder (the user can write there -
	// it's the gate software's own directory); handle plumbing across
	// CreateProcessAsUser isn't worth the ceremony.
	outPath := filepath.Join(dir, "unitrise-consume-output.tmp")
	cmdLine := `cmd /S /C "` + quoteIfNeeded(full) + ` > "` + outPath + `" 2>&1"`
	cmdPtr, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		return 0, "", false
	}
	dirPtr, _ := windows.UTF16PtrFromString(dir)
	desktop, _ := windows.UTF16PtrFromString(`winsta0\default`)

	// The user's own environment block, best-effort (a bat referencing
	// %USERPROFILE% etc. should see the user's values, not SYSTEM's).
	var env *uint16
	if e := windows.CreateEnvironmentBlock(&env, token, false); e == nil && env != nil {
		defer windows.DestroyEnvironmentBlock(env) //nolint:errcheck
	} else {
		env = nil
	}

	si := &windows.StartupInfo{Desktop: desktop}
	si.Cb = uint32(unsafe.Sizeof(*si))
	pi := &windows.ProcessInformation{}
	const createNoWindow = 0x08000000
	const createUnicodeEnv = 0x00000400
	if err := windows.CreateProcessAsUser(token, nil, cmdPtr, nil, nil, false,
		createNoWindow|createUnicodeEnv, env, dirPtr, si, pi); err != nil {
		return 0, "", false
	}
	defer windows.CloseHandle(pi.Thread)  //nolint:errcheck
	defer windows.CloseHandle(pi.Process) //nolint:errcheck

	wait, _ := windows.WaitForSingleObject(pi.Process, uint32(timeout.Milliseconds()))
	if wait != windows.WAIT_OBJECT_0 {
		windows.TerminateProcess(pi.Process, 1) //nolint:errcheck
		return -2, fmt.Sprintf("timed out after %s in the user session", timeout), true
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return -2, "couldn't read the exit code: " + err.Error(), true
	}
	b, _ := os.ReadFile(outPath)
	os.Remove(outPath) //nolint:errcheck
	return int(code), strings.TrimSpace(string(b)), true
}
