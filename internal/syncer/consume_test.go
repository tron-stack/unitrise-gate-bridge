package syncer

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/config"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/logging"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/render"
)

// ED-6 (2026-11-13, part 6 audit, CRITICAL): the consume command and the gate
// file name come from the server and this program runs as the service
// account. These tests hold the one rule (consume.go) and drive the real
// consume and apply paths: a refused value writes and runs NOTHING, and the
// value the PTI preset uses still works, run as the file itself.

// A Syncer over a throwaway save folder and a backend that accepts every
// report (apply sends one after a write).
func testSyncer(t *testing.T, save string, local *config.Config) *Syncer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)
	cfg := &config.Config{AccessKey: "urgk_test-not-real", AccessSecret: "urgs_test-secret-not-real", FacilityID: "f", APIEndpoint: srv.URL, SavePath: save}
	if local != nil {
		cfg.GeneratedFileName = local.GeneratedFileName
		cfg.ConsumeCommand = local.ConsumeCommand
	}
	log, err := logging.New(filepath.Join(t.TempDir(), "agent.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.Close)
	render.RosterPath = filepath.Join(t.TempDir(), "roster.json")
	return &Syncer{cfg: cfg, client: api.New(cfg), log: log, force: make(chan struct{}, 1)}
}

func testState(fileName, consume string) *api.State {
	st := &api.State{StateHash: "h", Provider: "unitrise_test"}
	st.Facility.Name = "Test Yard"
	st.Settings.GeneratedFileName = fileName
	st.Settings.ConsumeCommand = consume
	return st
}

// writeScript puts a consume file in dir that prints how many arguments it
// was given and leaves a flag file behind, so "it ran" and "it did not run"
// are facts about the folder and not only about what the agent reported.
func writeScript(t *testing.T, dir, name string) {
	t.Helper()
	body := "#!/bin/sh\necho CONSUMED-ARGS-$#\ntouch ran.flag\n"
	if runtime.GOOS == "windows" {
		body = "@echo CONSUMED-ARGS-0\r\n@type nul > ran.flag\r\n"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestThePresetNamesPass(t *testing.T) {
	// Every consume command and file name the server's presets carry
	// (truckpark-backend util/gateFilePresets.js), plus ordinary variants.
	for _, n := range []string{"Ptisend.bat", "PTISEND.BAT", "send update.cmd", "import_v2.exe", "Sync-Now.Bat"} {
		if p := consumeNameProblem(n); p != "" {
			t.Errorf("consume %q refused: %s", n, p)
		}
	}
	for _, n := range []string{"UPDATE.DAT", "pti.dat", "update.txt", "toread.fil", "LINK.fil", "unitrise-doorking.csv", "gatecodes.csv", "UPDATE", "UNITRISE-GATE-TEST.txt", "GATECODES.TXT"} {
		if p := fileNameProblem(n); p != "" {
			t.Errorf("file %q refused: %s", n, p)
		}
	}
}

func TestConsumeNamesThatAreRefused(t *testing.T) {
	cases := map[string]string{
		"Ptisend.bat & powershell -c calc":  "characters",
		"Ptisend.bat; touch pwned":          "characters",
		"Ptisend.bat /q":                    "folder",
		`..\..\Windows\System32\cmd.exe`:    "folder",
		"../outside.bat":                    "folder",
		`C:\PTI\Ptisend.bat`:                "folder",
		"/bin/sh":                           "folder",
		"Ptisend..bat":                      `".."`,
		"Ptisend.ps1":                       ".bat, .cmd or .exe",
		"Ptisend":                           ".bat, .cmd or .exe",
		"Ptisend.bat.":                      "ends with a dot",
		"Ptisend.bat ":                      "ends with a dot",
		"CON.bat":                           "reserves",
		"nul.exe":                           "reserves",
		"com1.cmd":                          "reserves",
		".bat":                              "characters",
		" Ptisend.bat":                      "characters",
		"a%PATH%.bat":                       "characters",
		"x|y.bat":                           "characters",
		"x>y.bat":                           "characters",
		`"x".bat`:                           "characters",
		"x^y.bat":                           "characters",
		"x:y.bat":                           "characters",
		"PTISEN~1.BAT":                      "characters",
		strings.Repeat("a", 97) + ".bat":    "longer than 100",
		"Ptisend.bat\r\npowershell -c calc": "characters",
	}
	for name, want := range cases {
		p := consumeNameProblem(name)
		if p == "" || !strings.Contains(p, want) {
			t.Errorf("consume %q: got %q, want a refusal mentioning %q", name, p, want)
		}
	}
}

func TestGateFileNamesThatAreRefused(t *testing.T) {
	cases := map[string]string{
		`..\..\Windows\System32\drivers\etc\hosts`: "folder",
		"../escape.dat":     "folder",
		"x/y.dat":           "folder",
		`C:\PTI\UPDATE.DAT`: "folder",
		"evil.bat":          "program or script",
		"evil.EXE":          "program or script",
		"evil.ps1":          "program or script",
		"evil.vbs":          "program or script",
		"send.lnk":          "program or script",
		"pti.dll":           "program or script",
		"UPDATE.DAT.":       "ends with a dot",
		"UPDATE.DAT ":       "ends with a dot",
		"CON":               "reserves",
		"con.txt":           "reserves",
		"Aux.dat":           "reserves",
		"a:b.dat":           "characters",
		"UPDATE.DAT:hide":   "characters",
		"..":                `".."`,
	}
	for name, want := range cases {
		p := fileNameProblem(name)
		if p == "" || !strings.Contains(p, want) {
			t.Errorf("file %q: got %q, want a refusal mentioning %q", name, p, want)
		}
	}
}

func TestResolveConsumeOnlyAFileInsideTheSaveFolder(t *testing.T) {
	save := t.TempDir()
	writeScript(t, save, "Ptisend.bat")
	full, err := resolveConsume(save, "Ptisend.bat", false)
	if err != nil || full != filepath.Join(save, "Ptisend.bat") {
		t.Fatalf("CONTROL: the preset's consume file resolves inside the folder: %q %v", full, err)
	}
	if _, err := resolveConsume(save, "Missing.bat", false); err == nil || !strings.Contains(err.Error(), "isn't in the save folder") {
		t.Errorf("a consume file that is not there is refused: %v", err)
	}
	if err := os.Mkdir(filepath.Join(save, "Folder.bat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveConsume(save, "Folder.bat", false); err == nil || !strings.Contains(err.Error(), "isn't a file") {
		t.Errorf("a folder named like a batch file is refused: %v", err)
	}
	// A link in the folder pointing at a file outside it is not run.
	outside := t.TempDir()
	writeScript(t, outside, "Elsewhere.bat")
	if runtime.GOOS == "windows" {
		t.Log("the link case needs the right to create symlinks, which a Windows runner may not grant; it is held on the other platforms")
	} else {
		if err := os.Symlink(filepath.Join(outside, "Elsewhere.bat"), filepath.Join(save, "Linked.bat")); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveConsume(save, "Linked.bat", false); err == nil || !strings.Contains(err.Error(), "is a link") {
			t.Errorf("a link to a file outside the folder is refused: %v", err)
		}
	}
	if _, err := resolveConsume(save, `C:\PTI\Ptisend.bat`, true); err == nil || !strings.Contains(err.Error(), "this PC's config.json") {
		t.Errorf("a local override is held to the same rule and says where it came from: %v", err)
	}
}

func TestConsumeRunsTheFileItselfWithNoShell(t *testing.T) {
	save := t.TempDir()
	// A space in the name: through `sh -c` it would split into "send" and
	// "update.bat" and fail; run as the file itself it works, with no
	// arguments (the script prints how many it was given).
	writeScript(t, save, "send update.bat")
	s := testSyncer(t, save, nil)
	exit, out := s.consume(testState("UPDATE.DAT", "send update.bat"))
	if exit != 0 || !strings.Contains(out, "CONSUMED-ARGS-0") {
		t.Fatalf("the consume file runs directly with no arguments: exit %d, output %q", exit, out)
	}
	if !exists(filepath.Join(save, "ran.flag")) {
		t.Fatal("...and it really ran, in the save folder")
	}
	// The PTI preset's own name.
	writeScript(t, save, "Ptisend.bat")
	os.Remove(filepath.Join(save, "ran.flag"))
	if exit, out := s.consume(testState("UPDATE.DAT", "Ptisend.bat")); exit != 0 || !strings.Contains(out, "CONSUMED-ARGS-0") || !exists(filepath.Join(save, "ran.flag")) {
		t.Fatalf("CONTROL: Ptisend.bat runs: exit %d, output %q", exit, out)
	}
}

func TestARefusedConsumeRunsNothing(t *testing.T) {
	parent := t.TempDir()
	save := filepath.Join(parent, "site")
	if err := os.Mkdir(save, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, save, "Ptisend.bat")
	writeScript(t, parent, "outside.bat")
	s := testSyncer(t, save, nil)
	for _, cmd := range []string{
		"Ptisend.bat; touch pwned",
		"Ptisend.bat & type nul > pwned",
		"Ptisend.bat && touch pwned",
		"../outside.bat",
		filepath.Join(parent, "outside.bat"),
	} {
		exit, out := s.consume(testState("UPDATE.DAT", cmd))
		if exit != -2 || !strings.Contains(out, "was refused") {
			t.Errorf("%q: exit %d, output %q - want a refusal", cmd, exit, out)
		}
		for _, f := range []string{filepath.Join(save, "pwned"), filepath.Join(parent, "pwned"), filepath.Join(save, "ran.flag"), filepath.Join(parent, "ran.flag")} {
			if exists(f) {
				t.Fatalf("%q ran something: %s exists", cmd, f)
			}
		}
	}
	// No consume command configured is still "nothing to run" (-1), not a refusal.
	if exit, out := s.consume(testState("UPDATE.DAT", "")); exit != -1 || out != "" {
		t.Errorf("CONTROL: a blank consume command is -1 with no output: %d %q", exit, out)
	}
}

func TestApplyWritesOnlyInsideTheSaveFolder(t *testing.T) {
	parent := t.TempDir()
	save := filepath.Join(parent, "site")
	if err := os.Mkdir(save, 0o755); err != nil {
		t.Fatal(err)
	}
	s := testSyncer(t, save, nil)
	for _, name := range []string{"../escape.dat", "evil.bat", "CON", "UPDATE.DAT."} {
		err := s.apply(testState(name, ""))
		if err == nil || !strings.Contains(err.Error(), "nothing was written") {
			t.Errorf("%q: %v - want a refusal that wrote nothing", name, err)
		}
	}
	if entries, _ := os.ReadDir(save); len(entries) != 0 {
		t.Fatalf("a refused name wrote into the save folder: %v", entries)
	}
	if exists(filepath.Join(parent, "escape.dat")) {
		t.Fatal("a refused name wrote outside the save folder")
	}
	// CONTROL: the server's UPDATE.DAT lands in the folder.
	if err := s.apply(testState("UPDATE.DAT", "")); err != nil || !exists(filepath.Join(save, "UPDATE.DAT")) {
		t.Fatalf("CONTROL: UPDATE.DAT is written in the save folder: %v", err)
	}
	// This PC's own override is held to the same rule.
	local := testSyncer(t, save, &config.Config{GeneratedFileName: "..\\up.dat"})
	if err := local.apply(testState("UPDATE.DAT", "")); err == nil || !strings.Contains(err.Error(), "this PC's config.json") {
		t.Errorf("a local file name override is held to the rule: %v", err)
	}
}

// The second guard behind the plain-name rule: whatever reaches the join, the
// result is a file directly in the save folder or it is refused.
func TestInsideSavePathRefusesAnEscape(t *testing.T) {
	save := t.TempDir()
	for _, name := range []string{"../x.dat", "sub/x.dat", "..", "."} {
		if full, err := insideSavePath(save, name); err == nil {
			t.Errorf("%q resolved to %q - want a refusal", name, full)
		}
	}
	if full, err := insideSavePath(save, "UPDATE.DAT"); err != nil || full != filepath.Join(save, "UPDATE.DAT") {
		t.Errorf("CONTROL: UPDATE.DAT is in the folder: %q %v", full, err)
	}
}

func TestTheSessionLineIsBuiltFromCheckedPathsOnly(t *testing.T) {
	line, err := sessionConsumeLine(`C:\Windows\System32\cmd.exe`, `C:\Program Files\PTI\Ptisend.bat`, `C:\Program Files\PTI\unitrise-consume-0a1b.tmp`)
	want := `"C:\Windows\System32\cmd.exe" /D /S /C ""C:\Program Files\PTI\Ptisend.bat" > "C:\Program Files\PTI\unitrise-consume-0a1b.tmp" 2>&1"`
	if err != nil || line != want {
		t.Fatalf("session line:\n got %q (%v)\nwant %q", line, err, want)
	}
	for _, bad := range [][3]string{
		{`C:\Windows\System32\cmd.exe`, `C:\PTI\x" & calc & ".bat`, `C:\PTI\o.tmp`},
		{`C:\Windows\System32\cmd.exe`, `C:\PTI\x.bat`, "C:\\PTI\\o.tmp\r\ncalc"},
		{"", `C:\PTI\x.bat`, `C:\PTI\o.tmp`},
	} {
		if _, err := sessionConsumeLine(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("a path that can't be quoted safely is refused: %q", bad)
		}
	}
}
