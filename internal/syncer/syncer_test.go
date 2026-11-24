package syncer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/config"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/logging"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/render"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/status"
)

// fakeServer answers the state with whatever *state holds and records every
// report the agent sends.
type fakeServer struct {
	mu         sync.Mutex
	state      *api.State
	heartbeats atomic.Int32
	applied    []string // raw bodies
	beats      []string
}

func newFakeServer(t *testing.T, st *api.State) (*fakeServer, *httptest.Server) {
	t.Helper()
	f := &fakeServer{state: st}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/state"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(f.state) //nolint:errcheck
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			f.heartbeats.Add(1)
			f.beats = append(f.beats, string(b))
		case strings.HasSuffix(r.URL.Path, "/applied"):
			f.applied = append(f.applied, string(b))
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func syncerFor(t *testing.T, endpoint, save string) *Syncer {
	t.Helper()
	cfg := &config.Config{AccessKey: "urgk_test-not-real", AccessSecret: "urgs_test-secret-not-real", FacilityID: "f", APIEndpoint: endpoint, SavePath: save}
	log, err := logging.New(filepath.Join(t.TempDir(), "agent.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.Close)
	state := t.TempDir()
	render.RosterPath = filepath.Join(state, "last-roster.json")
	return &Syncer{cfg: cfg, client: api.New(cfg), log: log, force: make(chan struct{}, 1), ownFilesPath: filepath.Join(state, "own-files.json")}
}

func writeScriptBody(t *testing.T, dir, name, sh, bat string) {
	t.Helper()
	body := "#!/bin/sh\n" + sh + "\n"
	if runtime.GOOS == "windows" {
		body = bat
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func deltaState(creds ...api.Credential) *api.State {
	st := &api.State{StateHash: "h", Provider: "template", ForceNonce: 1, Credentials: creds}
	st.Facility.Name = "Test Yard"
	st.Settings.GeneratedFileName = "update.txt"
	st.Settings.ConsumeCommand = "import.bat"
	st.Settings.Format = &api.FormatSpec{Mode: "delta", AddedLine: "A,{code},{unit}", RemovedLine: "D,{code},{unit}", SortBy: "code", LineEnding: "lf"}
	return st
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The delta defect end to end (2026-11-20): an import that FAILED must not
// advance what the agent thinks the gate has, so the next file still carries
// the lines the gate never imported.
func TestAFailedImportIsResentAsASuperset(t *testing.T) {
	save := t.TempDir()
	_, srv := newFakeServer(t, nil)
	s := syncerFor(t, srv.URL, save)
	a := api.Credential{Code: "1111", UnitLabel: "1", Status: "active"}
	b := api.Credential{Code: "2222", UnitLabel: "2", Status: "active"}
	c := api.Credential{Code: "3333", UnitLabel: "3", Status: "active"}
	file := filepath.Join(save, "update.txt")

	writeScriptBody(t, save, "import.bat", "exit 0", "@exit /b 0\r\n")
	if err := s.apply(deltaState(a)); err != nil {
		t.Fatal(err)
	}
	// The import fails from here on.
	writeScriptBody(t, save, "import.bat", "exit 3", "@exit /b 3\r\n")
	if err := s.apply(deltaState(a, b)); err == nil {
		t.Fatal("a failed import is a failed sync")
	}
	if got := readFile(t, file); got != "A,2222,2\n" {
		t.Fatalf("second file: %q", got)
	}
	if err := s.apply(deltaState(a, b, c)); err == nil {
		t.Fatal("a failed import is a failed sync")
	}
	if got := readFile(t, file); got != "A,2222,2\nA,3333,3\n" {
		t.Fatalf("the file replacing an unimported one must still carry B: %q", got)
	}
	// The import works again: the next state's diff is against that file.
	writeScriptBody(t, save, "import.bat", "exit 0", "@exit /b 0\r\n")
	if err := s.apply(deltaState(a, b, c)); err != nil {
		t.Fatal(err)
	}
	if err := s.apply(deltaState(b, c)); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, file); got != "D,1111,1\n" {
		t.Fatalf("CONTROL: after a good import only the new change is sent: %q", got)
	}
}

// With no consume command, a file the gate software hasn't taken yet is
// replaced by a superset; once it has gone, the diff starts from it.
func TestNoConsumeWaitsForTheVendorToTakeTheFile(t *testing.T) {
	save := t.TempDir()
	_, srv := newFakeServer(t, nil)
	s := syncerFor(t, srv.URL, save)
	a := api.Credential{Code: "1111", UnitLabel: "1", Status: "active"}
	b := api.Credential{Code: "2222", UnitLabel: "2", Status: "active"}
	file := filepath.Join(save, "update.txt")
	noConsume := func(st *api.State) *api.State { st.Settings.ConsumeCommand = ""; return st }

	if err := s.apply(noConsume(deltaState(a))); err != nil {
		t.Fatal(err)
	}
	if err := s.apply(noConsume(deltaState(a, b))); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, file); got != "A,1111,1\nA,2222,2\n" {
		t.Fatalf("the vendor never took the first file, so A is resent: %q", got)
	}
	os.Remove(file) // the gate software imports and deletes it
	if err := s.apply(noConsume(deltaState(b))); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, file); got != "D,1111,1\n" {
		t.Fatalf("once taken, the diff starts from it: %q", got)
	}
}

// A heartbeat on its own clock: with a one-hour poll the server used to see
// nothing for an hour and call a healthy agent offline after 10 minutes.
func TestHeartbeatsKeepComingBetweenLongPolls(t *testing.T) {
	save := t.TempDir()
	st := testState("", "")
	f, srv := newFakeServer(t, st)
	s := syncerFor(t, srv.URL, save)
	s.cfg.PollSeconds = 3600
	s.heartbeatEvery = func() time.Duration { return 20 * time.Millisecond }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	time.Sleep(1500 * time.Millisecond) // the first cycle starts after 1 s
	cancel()
	<-done
	if n := f.heartbeats.Load(); n < 5 {
		t.Fatalf("%d heartbeat(s) in 1.5 s with a one-hour poll - want them on their own clock", n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.applied) != 1 {
		t.Fatalf("CONTROL: one poll, one apply (got %d)", len(f.applied))
	}
	for _, b := range f.beats {
		if !strings.Contains(b, `"lastApplyAt"`) {
			t.Fatalf("a heartbeat went out before the first sync finished: %s", b)
		}
	}
}

func TestTheHeartbeatClockIsWellInsideTheOnlineWindow(t *testing.T) {
	for i := 0; i < 200; i++ {
		d := heartbeatInterval()
		if d < 60*time.Second || d > 90*time.Second {
			t.Fatalf("heartbeat interval %s, want 60-90 s", d)
		}
	}
}

// A vendor tool can echo the lines it imported; those are gate codes. The
// output is masked before the log, the dashboard status or the server sees
// it.
func TestConsumeOutputIsMaskedEverywhere(t *testing.T) {
	save := t.TempDir()
	st := testState("UPDATE.DAT", "send.bat")
	f, srv := newFakeServer(t, st)
	s := syncerFor(t, srv.URL, save)
	writeScriptBody(t, save, "send.bat", "echo imported 482913 A14 011A result 0:258", "@echo imported 482913 A14 011A result 0:258\r\n")
	logging.Hook = status.AppendLog
	t.Cleanup(func() { logging.Hook = nil })
	if err := s.apply(st); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	applied := strings.Join(f.applied, "\n")
	f.mu.Unlock()
	snap := status.Get()
	for where, text := range map[string]string{
		"the applied report": applied,
		"the dashboard note": snap.ConsumeNote,
		"the log":            strings.Join(snap.Log, "\n"),
	} {
		if strings.Contains(text, "482913") {
			t.Errorf("%s carries the code: %s", where, text)
		}
	}
	if !strings.Contains(applied, "imported ****** A14 ***A result 0:***") {
		t.Fatalf("CONTROL: the masked output is still sent: %s", applied)
	}
}

// The permission check sits between the session attempt and the run as this
// process: when it refuses, nothing runs and the reason is the status line.
func TestARefusedImportPermissionRunsNothing(t *testing.T) {
	save := t.TempDir()
	st := testState("UPDATE.DAT", "Ptisend.bat")
	_, srv := newFakeServer(t, st)
	s := syncerFor(t, srv.URL, save)
	writeScript(t, save, "Ptisend.bat")
	const why = "The import file's folder (C:\\PTI) can be changed by any user on this PC (Authenticated Users has write access), so the Bridge won't run Ptisend.bat as the system account. Limit the folder to Administrators."
	orig := consumeACLRefusal
	t.Cleanup(func() { consumeACLRefusal = orig })
	consumeACLRefusal = func(string) string { return why }
	err := s.apply(st)
	if err == nil || !strings.Contains(err.Error(), why) {
		t.Fatalf("apply error = %v, want the permission refusal", err)
	}
	if exists(filepath.Join(save, "ran.flag")) {
		t.Fatal("the import file ran although its folder failed the permission check")
	}
	if !exists(filepath.Join(save, "UPDATE.DAT")) {
		t.Fatal("CONTROL: the gate file itself is still written")
	}
	// CONTROL: with the check passing, it runs.
	consumeACLRefusal = func(string) string { return "" }
	if err := s.apply(st); err != nil || !exists(filepath.Join(save, "ran.flag")) {
		t.Fatalf("CONTROL: a passing check runs the file: %v", err)
	}
}

// Before the first apply the heartbeat carries no apply time at all.
func TestTheHeartbeatSendsNoApplyTimeBeforeTheFirstApply(t *testing.T) {
	_, srv := newFakeServer(t, nil)
	s := syncerFor(t, srv.URL, t.TempDir())
	if hb := s.heartbeat(); hb.LastApplyAt != nil {
		t.Fatalf("heartbeat before any apply carries lastApplyAt %v", hb.LastApplyAt)
	}
	s.mu.Lock()
	s.lastApplyAt = time.Now()
	s.mu.Unlock()
	if hb := s.heartbeat(); hb.LastApplyAt == nil {
		t.Fatal("CONTROL: after an apply the time is sent")
	}
}
