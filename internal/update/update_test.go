package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.2.0", "0.3.0", true},
		{"0.3.0", "0.3.0", false},
		{"0.3.1", "0.3.0", false},
		{"0.3.0", "1.0.0", true},
		{"1.0.0", "0.9.9", false},
		{"v0.2.0", "v0.10.0", true}, // numeric, not lexicographic
		{"dev", "0.3.0", false},     // unparseable never nags
		{"0.3.0", "", false},
		{"", "0.3.0", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.current, c.latest); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestExpectedSum(t *testing.T) {
	sums := []byte("abc123  unitrise-gate-windows-amd64.exe\ndef456  unitrise-gate-linux-amd64\n")
	if got := expectedSum(sums, "unitrise-gate-windows-amd64.exe"); got != "abc123" {
		t.Errorf("windows sum = %q", got)
	}
	if got := expectedSum(sums, "unitrise-gate-darwin-arm64"); got != "" {
		t.Errorf("missing entry should be empty, got %q", got)
	}
	// sha256sum's binary-mode marker (*name) still matches.
	if got := expectedSum([]byte("aa11 *unitrise-gate-linux-amd64\n"), "unitrise-gate-linux-amd64"); got != "aa11" {
		t.Errorf("starred name = %q", got)
	}
}

// ── signed releases (2026-11-20) ─────────────────────────────────────────────

// A test release: a throwaway key pair made in the test, never a real one.
type release struct {
	pub, priv []byte
	files     map[string][]byte
	fetched   []string
}

func newRelease(t *testing.T, version string, signWith ed25519.PrivateKey) (*release, *api.UpdateInfo) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if signWith == nil {
		signWith = priv
	}
	bin := bytes.Repeat([]byte("B"), 2<<20)
	sum := sha256.Sum256(bin)
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + AssetName() + "\nffff  install.ps1\n")
	dir := ReleaseBase + "v" + version + "/"
	r := &release{pub: pub, priv: priv, files: map[string][]byte{
		dir + AssetName():      bin,
		dir + "SHA256SUMS":     sums,
		dir + "SHA256SUMS.sig": []byte(EncodeSignature(ed25519.Sign(signWith, SignedMessage(version, sums)))),
	}}
	withKey(t, hex.EncodeToString(pub))
	return r, &api.UpdateInfo{LatestVersion: version, DownloadURL: dir + AssetName(), Sha256URL: dir + "SHA256SUMS"}
}

func (r *release) fetch(url string) ([]byte, error) {
	r.fetched = append(r.fetched, url)
	if b, ok := r.files[url]; ok {
		return b, nil
	}
	return nil, errors.New("404")
}

func withKey(t *testing.T, hexKey string) {
	t.Helper()
	old := releasePublicKey
	releasePublicKey = hexKey
	t.Cleanup(func() { releasePublicKey = old })
}

func TestASignedReleaseVerifies(t *testing.T) {
	r, info := newRelease(t, "0.6.1", nil)
	bin, err := verifyRelease(info, r.fetch)
	if err != nil {
		t.Fatalf("CONTROL: a properly signed release installs: %v", err)
	}
	if len(bin) != 2<<20 {
		t.Fatalf("got %d bytes", len(bin))
	}
}

// No compiled-in key: nothing is fetched, nothing is installed.
func TestABuildWithoutAKeyRefusesEveryUpdate(t *testing.T) {
	r, info := newRelease(t, "0.6.1", nil)
	withKey(t, "")
	if _, err := verifyRelease(info, r.fetch); err == nil || !strings.Contains(err.Error(), "no release signing key") {
		t.Fatalf("want a refusal naming the missing key, got %v", err)
	}
	if len(r.fetched) != 0 {
		t.Fatalf("fetched %v with no key to check them", r.fetched)
	}
}

// The server names the links; only the release folder for the offered version
// is accepted, and nothing is fetched otherwise.
func TestLinksOffTheReleaseBaseAreRefused(t *testing.T) {
	r, good := newRelease(t, "0.6.1", nil)
	cases := map[string]api.UpdateInfo{
		"another host":             {LatestVersion: "0.6.1", DownloadURL: "https://evil.example/unitrise-gate.exe", Sha256URL: "https://evil.example/SHA256SUMS"},
		"latest, not versioned":    {LatestVersion: "0.6.1", DownloadURL: "https://github.com/tron-stack/unitrise-gate-bridge/releases/latest/download/" + AssetName(), Sha256URL: "https://github.com/tron-stack/unitrise-gate-bridge/releases/latest/download/SHA256SUMS"},
		"another version's folder": {LatestVersion: "0.6.1", DownloadURL: ReleaseBase + "v0.5.9/" + AssetName(), Sha256URL: ReleaseBase + "v0.5.9/SHA256SUMS"},
		"another repository":       {LatestVersion: "0.6.1", DownloadURL: "https://github.com/someone/fork/releases/download/v0.6.1/" + AssetName(), Sha256URL: "https://github.com/someone/fork/releases/download/v0.6.1/SHA256SUMS"},
		"another asset":            {LatestVersion: "0.6.1", DownloadURL: ReleaseBase + "v0.6.1/install.ps1", Sha256URL: good.Sha256URL},
		"sums elsewhere":           {LatestVersion: "0.6.1", DownloadURL: good.DownloadURL, Sha256URL: "https://evil.example/SHA256SUMS"},
		"no sums at all":           {LatestVersion: "0.6.1", DownloadURL: good.DownloadURL},
		"a path in the version":    {LatestVersion: "0.6.1/../../x", DownloadURL: ReleaseBase + "v0.6.1/../../x/" + AssetName(), Sha256URL: ReleaseBase + "v0.6.1/../../x/SHA256SUMS"},
	}
	for name, info := range cases {
		info := info
		r.fetched = nil
		if _, err := verifyRelease(&info, r.fetch); err == nil {
			t.Errorf("%s: installed", name)
		}
		if len(r.fetched) != 0 {
			t.Errorf("%s: fetched %v before the links were checked", name, r.fetched)
		}
	}
}

func TestASumsFileSignedByAnotherKeyIsRefused(t *testing.T) {
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	r, info := newRelease(t, "0.6.1", other)
	if _, err := verifyRelease(info, r.fetch); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("want a signature refusal, got %v", err)
	}
	for _, u := range r.fetched {
		if u == info.DownloadURL {
			t.Fatal("the binary was downloaded although the sums failed their signature")
		}
	}
}

// Rollback: a real, signed OLD release offered as the "new" version fails,
// because the signature names its own version.
func TestAnOldSignedReleaseCantPassAsANewOne(t *testing.T) {
	r, _ := newRelease(t, "0.6.0", nil)
	old := ReleaseBase + "v0.6.0/"
	newDir := ReleaseBase + "v0.7.0/"
	for _, f := range []string{AssetName(), "SHA256SUMS", "SHA256SUMS.sig"} {
		r.files[newDir+f] = r.files[old+f] // the old files re-hosted under the new tag
	}
	info := &api.UpdateInfo{LatestVersion: "0.7.0", DownloadURL: newDir + AssetName(), Sha256URL: newDir + "SHA256SUMS"}
	if _, err := verifyRelease(info, r.fetch); err == nil {
		t.Fatal("an old release's signature vouched for a newer version")
	}
}

func TestAMissingSignatureIsRefused(t *testing.T) {
	r, info := newRelease(t, "0.6.1", nil)
	delete(r.files, info.Sha256URL+".sig")
	if _, err := verifyRelease(info, r.fetch); err == nil || !strings.Contains(err.Error(), "SHA256SUMS.sig couldn't be downloaded") {
		t.Fatalf("want a refusal naming the missing signature, got %v", err)
	}
}

func TestABinaryThatDoesntMatchTheSignedSumIsRefused(t *testing.T) {
	r, info := newRelease(t, "0.6.1", nil)
	r.files[info.DownloadURL] = bytes.Repeat([]byte("X"), 2<<20)
	if _, err := verifyRelease(info, r.fetch); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want a checksum refusal, got %v", err)
	}
}
