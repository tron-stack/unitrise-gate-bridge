//go:build !windows

package syncer

import (
	"context"
	"os/exec"
)

func shellCommand(ctx context.Context, full string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", full)
}
