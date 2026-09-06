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

func shellCommand(ctx context.Context, full string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", full)
}
