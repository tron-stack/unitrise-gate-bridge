package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/acl"
)

const (
	tSystem = acl.SIDSystem
	tAdmins = acl.SIDAdministrators
	tUsers  = "S-1-5-32-545"
	tDana   = "S-1-5-21-1004336348-1177238915-682003330-1001"
)

func trustedObj(path string) acl.Object {
	return acl.Object{Path: path, Owner: tAdmins, ACEs: []acl.ACE{
		{SID: tSystem, Allow: true, Mask: 0x1F01FF},
		{SID: tAdmins, Allow: true, Mask: 0x1F01FF},
	}}
}

// withHooks points the config at a temp folder and stands in for Windows.
func withHooks(t *testing.T, problem string) (dir string, secured *[]string) {
	t.Helper()
	parent := t.TempDir()
	dir = filepath.Join(parent, "UnitRiseGateBridge")
	t.Setenv("UNITRISE_GATE_CONFIG", filepath.Join(dir, "config.json"))
	oldCheck, oldSecure := checkTrust, secure
	t.Cleanup(func() { checkTrust, secure = oldCheck, oldSecure })
	checkTrust = func() string { return problem }
	var calls []string
	secure = func(p string) error {
		// What is still in the folder when it gets limited.
		entries, _ := os.ReadDir(p)
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		calls = append(calls, p+":"+strings.Join(names, ","))
		return nil
	}
	return dir, &calls
}

func put(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func there(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestTheTrustDecisionChecksTheFolderAndEveryFile(t *testing.T) {
	dir := `C:\ProgramData\UnitRiseGateBridge`
	good := []acl.Object{trustedObj(dir + `\config.json`), trustedObj(dir + `\own-files.json`)}
	if p := trustDecision(trustedObj(dir), good); p != "" {
		t.Fatalf("CONTROL: a trusted folder and files pass: %q", p)
	}
	folder := trustedObj(dir)
	folder.ACEs = append(folder.ACEs, acl.ACE{SID: tUsers, Allow: true, Mask: 0x6})
	if p := trustDecision(folder, good); !strings.Contains(p, "The config folder") {
		t.Fatalf("a folder Users can add files to: %q", p)
	}
	planted := trustedObj(dir + `\own-files.json`)
	planted.Owner = tDana
	if p := trustDecision(trustedObj(dir), []acl.Object{good[0], planted}); !strings.Contains(p, "The file own-files.json") {
		t.Fatalf("a state file somebody else owns: %q", p)
	}
}

// The service never trusts a config.json it can't vouch for: Load refuses it
// in words, and the agent runs unpaired.
func TestLoadRefusesAnUntrustedConfig(t *testing.T) {
	dir, _ := withHooks(t, `The config folder (C:\ProgramData\UnitRiseGateBridge) is owned by PC\dana, not by SYSTEM or Administrators`)
	put(t, dir, "config.json", `{"accessKey":"k","accessSecret":"s","facilityId":"f","savePath":"C:\\x","apiEndpoint":"https://evil.example"}`)
	c, err := Load()
	if err == nil || c != nil || !errors.Is(err, ErrUntrusted) {
		t.Fatalf("Load = %+v, %v - want an untrusted refusal", c, err)
	}
	if !strings.Contains(err.Error(), "owned by PC\\dana") || !strings.Contains(err.Error(), "won't trust the pairing") {
		t.Fatalf("the refusal says why: %v", err)
	}
	checkTrust = func() string { return "" }
	if c, err := Load(); err != nil || c.APIEndpoint != "https://evil.example" {
		t.Fatalf("CONTROL: a trusted config loads: %v", err)
	}
}

// Untrusted: the folder is limited FIRST, then config.json and the state
// files are deleted; nothing else in it is touched.
func TestPrepareQuarantinesAnUntrustedFolder(t *testing.T) {
	dir, secured := withHooks(t, "The config folder (x) is owned by someone")
	put(t, dir, "config.json", "{}")
	put(t, dir, "own-files.json", `{"files":{"C:\\Windows\\win.ini":"abc"}}`)
	put(t, dir, "last-roster.json", "{}")
	put(t, dir, "notes.txt", "an Administrator's own file")
	res, err := Prepare()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"config.json", "own-files.json", "last-roster.json"} {
		if there(filepath.Join(dir, f)) {
			t.Errorf("%s survived the quarantine", f)
		}
	}
	if !there(filepath.Join(dir, "notes.txt")) {
		t.Error("a file the agent doesn't read was deleted")
	}
	if len(*secured) != 1 || !strings.Contains((*secured)[0], "config.json") {
		t.Fatalf("the folder must be limited BEFORE it is emptied: %v", *secured)
	}
	if s := res.Summary(); !strings.Contains(s, "didn't trust it") || !strings.Contains(s, "deleted config.json") || !strings.Contains(s, "Pair this machine again") {
		t.Fatalf("summary: %q", s)
	}
	// After the quarantine nothing loads.
	if _, err := Load(); !os.IsNotExist(err) {
		t.Fatalf("the planted pairing is gone: %v", err)
	}
}

// A trusted folder keeps its pairing (a reinstall is the update path), and is
// limited again.
func TestPrepareKeepsATrustedPairing(t *testing.T) {
	dir, secured := withHooks(t, "")
	put(t, dir, "config.json", "{}")
	res, err := Prepare()
	if err != nil || !res.KeptPairing || res.Problem != "" {
		t.Fatalf("Prepare = %+v, %v", res, err)
	}
	if !there(filepath.Join(dir, "config.json")) {
		t.Fatal("a trusted pairing was deleted")
	}
	if len(*secured) != 1 {
		t.Fatalf("the folder is limited again: %v", *secured)
	}
}

// A fresh machine: the folder exists, limited, before anything else does.
func TestPrepareCreatesTheFolderLimited(t *testing.T) {
	dir, secured := withHooks(t, "")
	res, err := Prepare()
	if err != nil || !res.Created {
		t.Fatalf("Prepare = %+v, %v", res, err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatal("the folder was not created")
	}
	if len(*secured) != 1 || !strings.HasPrefix((*secured)[0], dir+":") {
		t.Fatalf("the new folder is limited: %v", *secured)
	}
}

// A link where the folder should be is removed, never followed: what it
// points at is left exactly as it was.
func TestALinkInPlaceOfTheFolderIsRemovedNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("making the link needs a privilege on Windows")
	}
	dir, _ := withHooks(t, "The config folder (x) is a link to another place")
	target := t.TempDir()
	put(t, target, "config.json", "{}")
	put(t, target, "important.dat", "keep")
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		t.Fatal("the link must be replaced by a real folder")
	}
	if !there(filepath.Join(target, "config.json")) || !there(filepath.Join(target, "important.dat")) {
		t.Fatal("the quarantine followed the link and deleted from what it pointed at")
	}
}

func TestSaveFailsWhenTheFolderCantBeLimited(t *testing.T) {
	_, _ = withHooks(t, "")
	secure = func(string) error { return errors.New("access denied") }
	if err := base().Save(); err == nil || !strings.Contains(err.Error(), "wasn't saved") {
		t.Fatalf("a pairing saved into a folder others can write would be refused at the next start; Save must fail now: %v", err)
	}
	if there(Path()) {
		t.Fatal("config.json was written anyway")
	}
}
