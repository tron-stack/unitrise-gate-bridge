package syncer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/render"
)

func templateState(fileName string) *api.State {
	st := &api.State{StateHash: "h2", Provider: "template", Credentials: []api.Credential{{Code: "482913", UnitLabel: "A14", Status: "active"}}}
	st.Facility.Name = "Test Yard"
	st.Settings.GeneratedFileName = fileName
	st.Settings.Format = &api.FormatSpec{Mode: "full", Line: "{code},{unit}", LineEnding: "lf"}
	return st
}

// The test file carries every code; once the gate format takes over, the
// agent's own copy goes.
func TestTheTestFileIsRemovedWhenTheFormatMovesOn(t *testing.T) {
	save := t.TempDir()
	_, srv := newFakeServer(t, nil)
	s := syncerFor(t, srv.URL, save)
	testFile := filepath.Join(save, render.TestFileName)
	if err := s.apply(testState("", "")); err != nil {
		t.Fatal(err)
	}
	if !exists(testFile) {
		t.Fatal("CONTROL: test mode writes the test file")
	}
	if err := s.apply(templateState("UPDATE.DAT")); err != nil {
		t.Fatal(err)
	}
	if exists(testFile) {
		t.Fatal("the agent's own test file is still in the save folder after the format moved on")
	}
	if !exists(filepath.Join(save, "UPDATE.DAT")) {
		t.Fatal("CONTROL: the gate file is written")
	}
}

// GATECODES.TXT (the fallback for a refused name) goes too once a real name
// arrives.
func TestTheFallbackFileIsRemovedWhenANameArrives(t *testing.T) {
	save := t.TempDir()
	_, srv := newFakeServer(t, nil)
	s := syncerFor(t, srv.URL, save)
	if err := s.apply(templateState("")); err != nil {
		t.Fatal(err)
	}
	fallback := filepath.Join(save, "GATECODES.TXT")
	if !exists(fallback) {
		t.Fatal("CONTROL: no name writes GATECODES.TXT")
	}
	if err := s.apply(templateState("UPDATE.DAT")); err != nil {
		t.Fatal(err)
	}
	if exists(fallback) {
		t.Fatal("GATECODES.TXT is still there after the real file name arrived")
	}
}

// Only a file the agent wrote, unchanged: one somebody edited stays.
func TestAnEditedOwnFileIsLeftAlone(t *testing.T) {
	save := t.TempDir()
	_, srv := newFakeServer(t, nil)
	s := syncerFor(t, srv.URL, save)
	testFile := filepath.Join(save, render.TestFileName)
	if err := s.apply(testState("", "")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testFile, []byte("my own notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.apply(templateState("UPDATE.DAT")); err != nil {
		t.Fatal(err)
	}
	if !exists(testFile) {
		t.Fatal("a file somebody changed after the agent wrote it must be left alone")
	}
}

// A file of that name the agent never wrote (no record, not the test
// renderer's first line) stays.
func TestAPlantedFileOfThatNameIsLeftAlone(t *testing.T) {
	save := t.TempDir()
	_, srv := newFakeServer(t, nil)
	s := syncerFor(t, srv.URL, save)
	planted := filepath.Join(save, render.TestFileName)
	if err := os.WriteFile(planted, []byte("not the agent's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.apply(templateState("UPDATE.DAT")); err != nil {
		t.Fatal(err)
	}
	if !exists(planted) {
		t.Fatal("a file the agent never wrote must not be removed")
	}
	// An older agent's test file (no record) is recognised by its first line.
	if err := os.WriteFile(planted, []byte(render.TestFileHeader+"\nFacility: X\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.apply(templateState("UPDATE.DAT")); err != nil {
		t.Fatal(err)
	}
	if exists(planted) {
		t.Fatal("an older agent's own test file is removed once the format moved on")
	}
}

// Never the file just written, under another spelling of its name (on a
// case-insensitive disk GATECODES.TXT and gatecodes.txt are one file).
func TestTheFileJustWrittenIsNeverRemoved(t *testing.T) {
	save := t.TempDir()
	_, srv := newFakeServer(t, nil)
	s := syncerFor(t, srv.URL, save)
	if err := s.apply(templateState("")); err != nil { // GATECODES.TXT
		t.Fatal(err)
	}
	if err := s.apply(templateState("gatecodes.txt")); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(save, "gatecodes.txt")) {
		t.Fatal("the file just written was removed as an old one")
	}
}
