//go:build !windows

package syncer

import (
	"context"
	"os/exec"
	"time"
)

// Windows-only mechanism (services live in session 0 there); everywhere else
// the normal exec path is already the user's own session.
func runConsumeInSession(full, dir string, timeout time.Duration) (int, string, bool) {
	return 0, "", false
}

// directCommand starts the consume file itself (exec, no shell, no
// arguments): `full` has passed resolveConsume (2026-11-13, part 6 audit
// ED-6 - it used to be free text handed to `/bin/sh -c`).
func directCommand(ctx context.Context, full string) *exec.Cmd {
	return exec.CommandContext(ctx, full)
}

// The permission check is Windows-only: there the service runs the import
// file as LocalSystem in a folder that usually lets every user change it.
func platformConsumeACLRefusal(string) string { return "" }
