// Package update implements `unitrise-gate update`: a PERSON-RUN binary swap,
// never a silent background replacement (a gate PC that changes its own
// software mid-shift is how sites lose trust in vendors). The flow:
//
//  1. ask the server what the latest published version is (signed update-check)
//  2. if newer: check that every link it gave is a UnitRise Gate Bridge
//     release on GitHub (ReleaseBase), for exactly that version
//  3. download SHA256SUMS and SHA256SUMS.sig, and check the signature with the
//     release public key COMPILED INTO this agent
//  4. download this platform's binary and check it against the signed sums
//  5. atomically swap: current exe renamed to .old, new binary moved in place
//     (rename works on Windows even while the old file is executing)
//  6. tell the operator how to restart (service restart / re-run)
//
// ⚠ THE SERVER IS NOT TRUSTED TO NAME A BINARY (2026-11-20). The agent runs as
// LocalSystem, and the update used to install whatever binary the API server
// named, checked against a checksum from that same server: anyone who could
// point the agent at another server (an unauthenticated re-pair from the local
// dashboard did exactly that, see cmd/unitrise-gate pairing.go) or who held
// the server could hand every gate PC their own program. Now the links must
// sit under the compiled-in ReleaseBase, and the sums must carry an Ed25519
// signature from the key whose public half is built into the agent
// (`-X .../internal/update.releasePublicKey=<hex>`, from the committed
// release-signing.pub, see the Makefile). The signature covers the version
// too, so an older signed release can't be passed off as a newer one. An
// agent built without a key refuses every self-update.
//
// The run loop separately calls CheckOnly once a day and logs when an update
// is waiting - the console shows the same thing from heartbeat versions.
package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/status"
)

// ReleaseBase is the one place a self-update may come from: the Gate Bridge's
// GitHub releases, one folder per tag (".../download/v0.6.0/<asset>").
const ReleaseBase = "https://github.com/tron-stack/unitrise-gate-bridge/releases/download/"

// releasePublicKey is the hex Ed25519 public key releases are signed with,
// set at build time from release-signing.pub (the Makefile passes
// -X github.com/mytruckyards/unitrise-gate-bridge/internal/update.releasePublicKey=<hex>).
// Empty = this build can't verify an update, so it refuses to install one.
var releasePublicKey string

// HasSigningKey reports whether this build carries a release public key
// (`unitrise-gate version` says so).
func HasSigningKey() bool { return strings.TrimSpace(releasePublicKey) != "" }

// The domain-separation line the signature starts with (tools/sign-release
// signs exactly SignedMessage's bytes).
const signedPrefix = "unitrise-gate-bridge release v1\n"

// SignedMessage is what a release signature covers: a fixed first line, the
// version, then the SHA256SUMS body byte for byte. Binding the version means
// an old release's signature can't vouch for a "newer" version.
func SignedMessage(version string, sums []byte) []byte {
	msg := []byte(signedPrefix + "version " + strings.TrimPrefix(strings.TrimSpace(version), "v") + "\n")
	return append(msg, sums...)
}

// ParsePublicKey reads a hex Ed25519 public key (64 hex characters).
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("the release public key isn't 64 hex characters")
	}
	return ed25519.PublicKey(b), nil
}

// EncodeSignature / decodeSignature: SHA256SUMS.sig is one line of base64.
func EncodeSignature(sig []byte) string { return base64.StdEncoding.EncodeToString(sig) + "\n" }

func decodeSignature(b []byte) ([]byte, error) {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, errors.New("SHA256SUMS.sig isn't an Ed25519 signature")
	}
	return sig, nil
}

// VerifySums checks a release signature.
func VerifySums(pub ed25519.PublicKey, version string, sums, sigFile []byte) error {
	sig, err := decodeSignature(sigFile)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, SignedMessage(version, sums), sig) {
		return fmt.Errorf("the signature on SHA256SUMS doesn't match UnitRise's release key for version %s", strings.TrimPrefix(version, "v"))
	}
	return nil
}

// AssetName is this platform's binary in a release (matches the backend's
// DOWNLOAD_PLATFORMS map and make release).
func AssetName() string {
	n := "unitrise-gate-" + PlatformKey()
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return n
}

var plainVersion = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)

// releaseURLs checks the links the server sent and answers where the
// signature lives. Every link must be the release folder for EXACTLY the
// version offered, under ReleaseBase: nothing else is downloaded.
func releaseURLs(info *api.UpdateInfo) (sigURL string, err error) {
	version := strings.TrimPrefix(strings.TrimSpace(info.LatestVersion), "v")
	if !plainVersion.MatchString(version) {
		return "", fmt.Errorf("the server offered a version that isn't a plain release number (%q), so nothing was downloaded", info.LatestVersion)
	}
	dir := ReleaseBase + "v" + version + "/"
	if info.DownloadURL != dir+AssetName() {
		return "", fmt.Errorf("the update link isn't the UnitRise Gate Bridge %s release (expected %s, got %s), so nothing was downloaded", version, dir+AssetName(), info.DownloadURL)
	}
	if info.Sha256URL != dir+"SHA256SUMS" {
		return "", fmt.Errorf("the checksum link isn't the UnitRise Gate Bridge %s release (expected %s, got %s), so nothing was downloaded", version, dir+"SHA256SUMS", info.Sha256URL)
	}
	return info.Sha256URL + ".sig", nil
}

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

// Watch logs when a newer agent is published. Context-tied (stops with the
// service), retries hourly until the first successful check so a boot-time
// network blip doesn't leave a 24-hour blind window, then settles to daily.
// LOG-ONLY by design - the binary swap is always a person running
// `unitrise-gate update`.
func Watch(ctx context.Context, client *api.Client, logf func(string, ...any)) {
	interval := time.Hour
	timer := time.NewTimer(30 * time.Second) // first check shortly after boot
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		info, newer, err := CheckOnly(client)
		if err == nil {
			interval = 24 * time.Hour
			if newer {
				logf("agent %s is available (running %s) - install it from the dashboard or tray, or run `unitrise-gate update`", info.LatestVersion, api.AgentVersion)
			}
			// Surface (or clear) the one-click update in the dashboard + tray.
			v := ""
			if newer {
				v = info.LatestVersion
			}
			status.Update(func(s *status.Snapshot) { s.UpdateAvailable = v })
		}
		timer.Reset(interval)
	}
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

// verifyRelease downloads and checks a release, and returns the verified
// binary. fetch is the downloader (client.Download; a map in tests). Nothing
// is fetched when this build has no key or a link is off the release base.
func verifyRelease(info *api.UpdateInfo, fetch func(string) ([]byte, error)) ([]byte, error) {
	if !HasSigningKey() {
		return nil, errors.New("this build of the Gate Bridge has no release signing key, so it can't check an update and won't install one. Download the new version from the Gate card in UnitRise and run it")
	}
	pub, err := ParsePublicKey(releasePublicKey)
	if err != nil {
		return nil, fmt.Errorf("%v, so no update can be checked", err)
	}
	if info.DownloadURL == "" {
		return nil, fmt.Errorf("version %s is available but binaries aren't published for self-serve download yet - your UnitRise contact will send it", info.LatestVersion)
	}
	sigURL, err := releaseURLs(info)
	if err != nil {
		return nil, err
	}
	sums, err := fetch(info.Sha256URL)
	if err != nil {
		return nil, fmt.Errorf("SHA256SUMS couldn't be downloaded (%v) - nothing was installed; retry in a minute", err)
	}
	sig, err := fetch(sigURL)
	if err != nil {
		return nil, fmt.Errorf("SHA256SUMS.sig couldn't be downloaded (%v) - nothing was installed; a release without a signature is never installed", err)
	}
	if err := VerifySums(pub, info.LatestVersion, sums, sig); err != nil {
		return nil, fmt.Errorf("%v - nothing was installed", err)
	}
	want := expectedSum(sums, AssetName())
	if want == "" {
		return nil, fmt.Errorf("the signed SHA256SUMS has no entry for %s - nothing was installed", AssetName())
	}
	bin, err := fetch(info.DownloadURL)
	if err != nil {
		return nil, err
	}
	if len(bin) < 1<<20 {
		return nil, fmt.Errorf("downloaded file is suspiciously small (%d bytes) - nothing was installed", len(bin))
	}
	got := sha256.Sum256(bin)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("checksum mismatch for %s - nothing was installed (corrupt download or tampering)", AssetName())
	}
	return bin, nil
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
	fmt.Printf("downloading %s (%s)...\n", info.LatestVersion, info.DownloadURL)
	bin, err := verifyRelease(info, client.Download)
	if err != nil {
		return "", err
	}
	fmt.Println("signature and checksum verified.")

	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	dir := filepath.Dir(exe)

	// Write beside the target so the final rename never crosses volumes,
	// flushed to disk before the swap.
	tmp := filepath.Join(dir, ".unitrise-gate.new")
	if err := writeSynced(tmp, bin, 0o755); err != nil {
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

func writeSynced(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
