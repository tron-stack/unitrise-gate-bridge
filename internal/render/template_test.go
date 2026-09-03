package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
)

// The template engine writes the bytes a gate controller ingests - exact-byte
// behavior IS the contract, so these tests assert exact bytes. They mirror the
// backend's end-to-end smoke (scripts/smoke-gate-bridge.js) so `make test`
// stands on its own without a Node environment.

func tState(creds []api.Credential, spec *api.FormatSpec, nonce int) *api.State {
	st := &api.State{Provider: "template", ForceNonce: nonce, Credentials: creds}
	st.Facility.Name = "Test Yard"
	st.Settings.Format = spec
	st.StateHash = "t"
	return st
}

func render(t *testing.T, st *api.State) string {
	t.Helper()
	r, err := Get("template")
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Render(st)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFullModeExactBytes(t *testing.T) {
	RosterPath = filepath.Join(t.TempDir(), "roster.json")
	spec := &api.FormatSpec{
		Mode:   "full",
		Line:   "{code},{unit:width6},{tenant},{tz}",
		SortBy: "code",
		// LineEnding unset → CRLF is the DEFAULT (DOS-lineage importers).
	}
	got := render(t, tState([]api.Credential{
		{Code: "222222", UnitLabel: "Truck 42", TenantName: "M. Torres", Status: "active", TimeZoneGroup: 1},
		{Code: "111111", UnitLabel: "Truck 7", TenantName: "A. Chen", Status: "active", TimeZoneGroup: 1},
	}, spec, 0))
	// Sorted by code, width6 truncates "Truck 42" → "Truck ", CRLF after EVERY
	// line including the last.
	want := "111111,Truck ,A. Chen,1\r\n222222,Truck ,M. Torres,1\r\n"
	if got != want {
		t.Fatalf("full mode bytes:\n got %q\nwant %q", got, want)
	}
}

func TestFullModeSuspensionIsAbsence(t *testing.T) {
	RosterPath = filepath.Join(t.TempDir(), "roster.json")
	spec := &api.FormatSpec{Mode: "full", Line: "{code}", SortBy: "code", LineEnding: "lf"}
	got := render(t, tState([]api.Credential{
		{Code: "1111", Status: "active"},
		{Code: "2222", Status: "suspended"}, // no suspendedLine → lockout = absence
	}, spec, 0))
	if got != "1111\n" {
		t.Fatalf("suspended code must be ABSENT when the format can't express suspension: %q", got)
	}
}

func TestModifiersHeaderFooter(t *testing.T) {
	RosterPath = filepath.Join(t.TempDir(), "roster.json")
	spec := &api.FormatSpec{
		Mode:       "full",
		Header:     "BEGIN {count}",
		Line:       "{code:pad10}|{unit:rwidth6}",
		Footer:     "END",
		SortBy:     "code",
		LineEnding: "lf",
	}
	got := render(t, tState([]api.Credential{
		{Code: "42", UnitLabel: "B12", Status: "active"},
	}, spec, 0))
	want := "BEGIN 1\n0000000042|   B12\nEND\n"
	if got != want {
		t.Fatalf("modifiers:\n got %q\nwant %q", got, want)
	}
}

func TestDeltaLifecycle(t *testing.T) {
	RosterPath = filepath.Join(t.TempDir(), "roster.json")
	spec := &api.FormatSpec{
		Mode:        "delta",
		AddedLine:   "A,{code},{unit},{tz}",
		RemovedLine: "D,{code}",
		SortBy:      "code",
		LineEnding:  "lf",
	}
	active := api.Credential{Code: "5555", UnitLabel: "Truck 42", Status: "active", TimeZoneGroup: 1}

	// First render: everything is an add.
	got := render(t, tState([]api.Credential{active}, spec, 1))
	if got != "A,5555,Truck 42,1\n" {
		t.Fatalf("first delta render: %q", got)
	}
	if err := CommitApplied(); err != nil {
		t.Fatal(err)
	}

	// Unchanged state: an empty diff, never a spurious re-add.
	got = render(t, tState([]api.Credential{active}, spec, 1))
	if got != "\n" && got != "" {
		t.Fatalf("unchanged delta must emit no ops: %q", got)
	}
	if err := CommitApplied(); err != nil {
		t.Fatal(err)
	}

	// Suspension with no suspendedLine = removal op.
	suspended := active
	suspended.Status = "suspended"
	got = render(t, tState([]api.Credential{suspended}, spec, 1))
	if got != "D,5555\n" {
		t.Fatalf("suspension must emit the remove op: %q", got)
	}
	if err := CommitApplied(); err != nil {
		t.Fatal(err)
	}

	// Restore = add op again.
	got = render(t, tState([]api.Credential{active}, spec, 1))
	if got != "A,5555,Truck 42,1\n" {
		t.Fatalf("restore must emit the add op: %q", got)
	}
	if err := CommitApplied(); err != nil {
		t.Fatal(err)
	}

	// Force nonce change: the whole roster re-emits as adds.
	got = render(t, tState([]api.Credential{active}, spec, 2))
	if got != "A,5555,Truck 42,1\n" {
		t.Fatalf("force nonce must re-emit the roster: %q", got)
	}
}

func TestRosterCommittedOnlyOnApply(t *testing.T) {
	dir := t.TempDir()
	RosterPath = filepath.Join(dir, "roster.json")
	spec := &api.FormatSpec{Mode: "delta", AddedLine: "A,{code}", RemovedLine: "D,{code}", LineEnding: "lf"}
	_ = render(t, tState([]api.Credential{{Code: "9", Status: "active"}}, spec, 1))
	// No CommitApplied: the roster file must NOT exist yet - a render whose
	// file never landed must not poison the next diff.
	if _, err := os.Stat(RosterPath); err == nil {
		t.Fatal("roster must only be committed after the vendor file lands")
	}
}

func TestUnknownPlaceholderSurvivesLiterally(t *testing.T) {
	RosterPath = filepath.Join(t.TempDir(), "roster.json")
	spec := &api.FormatSpec{Mode: "full", Line: "{code},{bogus}", LineEnding: "lf"}
	got := render(t, tState([]api.Credential{{Code: "7", Status: "active"}}, spec, 0))
	if !strings.Contains(got, "{bogus}") {
		t.Fatalf("unknown placeholders must pass through visibly (never silently vanish): %q", got)
	}
}
