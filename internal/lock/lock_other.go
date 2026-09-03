//go:build !windows

package lock

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/config"
)

// flock on a file beside the config: advisory, kernel-released on process
// exit, so a crashed agent never leaves a stale lock behind.
func acquire() (func(), error) {
	dir := config.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "agent.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if err == unix.EWOULDBLOCK {
			return nil, AlreadyRunningError{}
		}
		return nil, err
	}
	return func() {
		unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
	}, nil
}
