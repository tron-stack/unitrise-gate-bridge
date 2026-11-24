package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/update"
)

// A throwaway key pair, made inside the test (never a real release key).
func keyFiles(t *testing.T, dir string) (keyPath, pubPath string, pub ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath = filepath.Join(dir, "test.key")
	pubPath = filepath.Join(dir, "test.pub")
	os.WriteFile(keyPath, []byte(hex.EncodeToString(priv.Seed())+"\n"), 0o600) //nolint:errcheck
	os.WriteFile(pubPath, []byte(hex.EncodeToString(pub)+"\n"), 0o644)         //nolint:errcheck
	return keyPath, pubPath, pub
}

// What the tool signs, the agent accepts - for that version only.
func TestASignedSumsFileVerifiesInTheAgent(t *testing.T) {
	dir := t.TempDir()
	keyPath, pubPath, pub := keyFiles(t, dir)
	sumsPath := filepath.Join(dir, "SHA256SUMS")
	sums := []byte("aa  unitrise-gate-windows-amd64.exe\n")
	os.WriteFile(sumsPath, sums, 0o644) //nolint:errcheck
	if err := run([]string{"-key", keyPath, "-version", "0.6.0", "-pub", pubPath, sumsPath}); err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(sumsPath + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	if err := update.VerifySums(pub, "0.6.0", sums, sig); err != nil {
		t.Fatalf("the agent refuses what the tool signed: %v", err)
	}
	if err := update.VerifySums(pub, "0.6.1", sums, sig); err == nil {
		t.Fatal("a signature for 0.6.0 vouched for 0.6.1")
	}
	if err := run([]string{"verify", "-version", "0.6.0", "-pub", pubPath, sumsPath}); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// Signing with a key that isn't the committed public key would ship a
// release every agent refuses: the tool refuses first.
func TestSigningWithTheWrongKeyIsRefused(t *testing.T) {
	dir := t.TempDir()
	keyPath, _, _ := keyFiles(t, dir)
	_, otherPub, _ := keyFiles(t, t.TempDir())
	sumsPath := filepath.Join(dir, "SHA256SUMS")
	os.WriteFile(sumsPath, []byte("aa  x\n"), 0o644) //nolint:errcheck
	err := run([]string{"-key", keyPath, "-version", "0.6.0", "-pub", otherPub, sumsPath})
	if err == nil || !strings.Contains(err.Error(), "the private key doesn't match") {
		t.Fatalf("want a mismatch refusal, got %v", err)
	}
	if _, err := os.Stat(sumsPath + ".sig"); err == nil {
		t.Fatal("a signature was written with the wrong key")
	}
	err = run([]string{"-key", keyPath, "-version", "0.6.0", "-pub", filepath.Join(dir, "missing.pub"), sumsPath})
	if err == nil || !strings.Contains(err.Error(), "commit the public key first") {
		t.Fatalf("want a refusal when there is no committed public key, got %v", err)
	}
}

func TestAnOpenSSLKeyIsRead(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parsePrivateKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil || !got.Equal(priv) {
		t.Fatalf("PKCS#8 PEM key: %v", err)
	}
}

func TestKeygenNeverReplacesAKey(t *testing.T) {
	dir := t.TempDir()
	if err := run([]string{"keygen", "-out", dir}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "release-signing.key"))
	if err := run([]string{"keygen", "-out", dir}); err == nil {
		t.Fatal("a second keygen into the same folder must refuse")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "release-signing.key"))
	if string(before) != string(after) {
		t.Fatal("keygen replaced an existing key")
	}
}
