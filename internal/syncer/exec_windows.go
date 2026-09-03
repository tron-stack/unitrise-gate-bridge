//go:build windows

package syncer

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
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
	line := full
	if strings.Contains(full, " ") && !strings.Contains(full, `"`) {
		if _, err := os.Stat(full); err == nil {
			line = `"` + full + `"`
		}
	}
	cmd := exec.CommandContext(ctx, "cmd")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd /S /C "` + line + `"`}
	return cmd
}
