// Package update implements `unitrise-gate update`: a PERSON-RUN binary swap,
// never a silent background replacement (a gate PC that changes its own
// software mid-shift is how sites lose trust in vendors). The flow:
//
//  1. ask the server what the latest published version is (signed update-check)
//  2. if newer: download this platform's binary + the SHA256SUMS file
//  3. verify the checksum (hard requirement when the sums file is published)
//  4. atomically swap: current exe renamed to .old, new binary moved in place
//     (rename works on Windows even while the old file is executing)
//  5. tell the operator how to restart (service restart / re-run)
//
// The run loop separately calls CheckOnly once a day and logs when an update
// is waiting - the console shows the same thing from heartbeat versions.
package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
)

// PlatformKey matches the server's DOWNLOAD_PLATFORMS map.
func PlatformKey() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

// IsNewer reports whether latest > current, comparing lenient x.y.z tuples.
// Unparseable versions (dev builds) are never "outdated".
func IsNewer(current, latest string) bool {
	parse := func(v string) ([]int, bool) {
		parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
		if len(parts) < 3 {
			return nil, false
		}
		out := make([]int, 3)
		for i := 0; i < 3; i++ {
			n, err := strconv.Atoi(parts[i])
			if err != nil {
				return nil, false
			}
			out[i] = n
		}
		return out, true
	}
	a, okA := parse(current)
	b, okB := parse(latest)
	if !okA || !okB {
		return false
	}
	for i := 0; i < 3; i++ {
		if b[i] > a[i] {
			return true
		}
		if b[i] < a[i] {
			return false
		}
	}
	return false
}

// CheckOnly returns (info, newerAvailable). Used by the run loop's daily log.
func CheckOnly(client *api.Client) (*api.UpdateInfo, bool, error) {
	info, err := client.UpdateCheck(PlatformKey())
	if err != nil {
		return nil, false, err
	}
	return info, IsNewer(api.AgentVersion, info.LatestVersion), nil
}

// expectedSum finds the hex digest for fileName inside a SHA256SUMS body
// (`<hex>  <name>` per line, the shasum/sha256sum format).
func expectedSum(sums []byte, fileName string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && filepath.Base(strings.TrimPrefix(f[1], "*")) == fileName {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// SelfUpdate performs the interactive update. Returns a human summary line.
func SelfUpdate(client *api.Client) (string, error) {
	info, newer, err := CheckOnly(client)
	if err != nil {
		return "", err
	}
	if !newer {
		return fmt.Sprintf("up to date (running %s, latest %s)", api.AgentVersion, info.LatestVersion), nil
	}
	if info.DownloadURL == "" {
		return "", fmt.Errorf("version %s is available but binaries aren't published for self-serve download yet - your UnitRise contact will send it", info.LatestVersion)
	}

	fmt.Printf("downloading %s (%s)...\n", info.LatestVersion, info.DownloadURL)
	bin, err := client.Download(info.DownloadURL)
	if err != nil {
		return "", err
	}
	if len(bin) < 1<<20 {
		return "", fmt.Errorf("downloaded file is suspiciously small (%d bytes) - aborting", len(bin))
	}

	// Integrity: when SHA256SUMS is published (make release writes it), a
	// mismatch is a hard stop. If the sums file itself is missing we say so
	// and continue - the transport is TLS either way.
	if info.Sha256URL != "" {
		fileName := filepath.Base(info.DownloadURL)
		if sums, err := client.Download(info.Sha256URL); err == nil {
			want := expectedSum(sums, fileName)
			if want == "" {
				return "", fmt.Errorf("SHA256SUMS is published but has no entry for %s - aborting", fileName)
			}
			got := sha256.Sum256(bin)
			if hex.EncodeToString(got[:]) != want {
				return "", fmt.Errorf("checksum mismatch for %s - aborting (corrupt download or tampering)", fileName)
			}
			fmt.Println("checksum verified.")
		} else {
			fmt.Println("note: SHA256SUMS not reachable - proceeding on TLS alone.")
		}
	}

	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	dir := filepath.Dir(exe)

	// Write beside the target so the final rename never crosses volumes.
	tmp := filepath.Join(dir, ".unitrise-gate.new")
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return "", fmt.Errorf("cannot write next to the current binary (run from an elevated prompt?): %w", err)
	}
	old := exe + ".old"
	_ = os.Remove(old) // stale leftover from a previous update
	// Windows allows renaming a RUNNING executable (its file stays mapped
	// under the old name) - this two-step is the standard self-update swap.
	if err := os.Rename(exe, old); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("cannot move the current binary aside: %w", err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Rename(old, exe) // roll back so the install keeps working
		return "", fmt.Errorf("swap failed (rolled back): %w", err)
	}

	restart := "restart the agent to finish (re-run it, or restart its scheduled task)"
	if runtime.GOOS == "windows" {
		restart = `finish with: unitrise-gate service stop && unitrise-gate service start`
	}
	return fmt.Sprintf("updated %s -> %s. The previous binary is %s. Now %s.", api.AgentVersion, info.LatestVersion, filepath.Base(old), restart), nil
}
