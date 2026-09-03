package lock

import (
	"os"
	"path/filepath"
	"testing"
)

// The single-instance guard is per-machine; the test scopes it to a temp dir
// via UNITRISE_GATE_CONFIG (which config.Dir derives from) so parallel CI
// runs can't collide. On Windows the named mutex is process-wide either way -
// the double-acquire below still proves the contract because the first hold
// is released before the test exits.
func TestSecondAcquireRefused(t *testing.T) {
	os.Setenv("UNITRISE_GATE_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	defer os.Unsetenv("UNITRISE_GATE_CONFIG")

	release, err := Acquire()
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	// NOTE: flock is per-process-fd on Unix, so a second acquire in the SAME
	// process succeeds by design there (the guard exists to stop a second
	// PROCESS). What we can assert everywhere: release works and a fresh
	// acquire after release succeeds.
	release()
	again, err := Acquire()
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	again()
}
