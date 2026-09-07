package render

import (
	"encoding/json"
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
		{Code: "222222", UnitLabel: "Truck 42", TenantName: "M. Torres", Status: "active", TimeZoneGroup: "1"},
		{Code: "111111", UnitLabel: "Truck 7", TenantName: "A. Chen", Status: "active", TimeZoneGroup: "1"},
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
	active := api.Credential{Code: "5555", UnitLabel: "Truck 42", Status: "active", TimeZoneGroup: "1"}

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

// Pinned against a real Falcon 2000 site's storEDGE update.old, measured
// on site 2026-09-07: FIXED-WIDTH columns, not space-separated - unit at
// column 1, code at column 11, tenant at column 21, zone at column 51
// (lines capped at 63 chars; long values truncate in place, never shifting
// the next field). The importer reads by POSITION: the site's first
// single-space attempt came back result.dat "1:1" (rejected at record 1)
// while storEDGE's fixed columns returned "0:258". The zone/access code is
// ALPHANUMERIC - "011A" for zone 1.
func TestFalconLayoutFixedColumns(t *testing.T) {
	st := &api.State{
		Credentials: []api.Credential{
			{Code: "482913", UnitLabel: "A14", TenantName: "Maria Torres", Status: "active", TimeZoneGroup: "011A"},
			{Code: "", UnitLabel: "B02", TenantName: "", Status: "vacant", TimeZoneGroup: "011A"},
			{Code: "9876543210", UnitLabel: "LONGUNIT99", TenantName: "A Tenant Name So Long It Overruns The Column", Status: "active", TimeZoneGroup: "011A"},
		},
	}
	falconLine := "{unit:width10}{code:width10}{tenant:width30}{tz}"
	st.Settings.Format = &api.FormatSpec{Mode: "full", Line: falconLine, VacantLine: falconLine, LineEnding: "crlf", SortBy: "unit"}
	b, err := (templateRenderer{}).Render(st)
	if err != nil {
		t.Fatal(err)
	}
	want := "A14       482913    Maria Torres                  011A\r\n" +
		"B02                                               011A\r\n" +
		"LONGUNIT999876543210A Tenant Name So Long It Overr011A\r\n"
	if got := string(b); got != want {
		t.Fatalf("Falcon fixed columns =\n%q\nwant\n%q", got, want)
	}
	for _, line := range strings.Split(strings.TrimRight(string(b), "\r\n"), "\r\n") {
		if len(line) > 63 {
			t.Fatalf("line exceeds the Falcon's 63-char cap: %q", line)
		}
	}
}

func TestFlexStringDecodesNumberAndString(t *testing.T) {
	var c api.Credential
	if err := json.Unmarshal([]byte(`{"code":"1","timeZoneGroup":1}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.TimeZoneGroup != "1" {
		t.Fatalf("numeric tz decoded to %q, want \"1\" (older backends send a number)", c.TimeZoneGroup)
	}
	if err := json.Unmarshal([]byte(`{"code":"1","timeZoneGroup":"011A"}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.TimeZoneGroup != "011A" {
		t.Fatalf("string tz decoded to %q, want \"011A\"", c.TimeZoneGroup)
	}
}

// Vacant units (real Falcon 2000 lesson, 2026-09-06): a unit absent from a
// full send KEEPS its old code in the controller, so stray pre-bridge codes
// on vacant units never die. With a vacantLine, every unit gets a row; the
// roster (keyed by code) never records the codeless rows, and delta formats
// skip them entirely.
func TestVacantLineRendersEveryUnit(t *testing.T) {
	st := &api.State{
		Credentials: []api.Credential{
			{Code: "111111", UnitLabel: "A1", TenantName: "Ann Chen", Status: "active", TimeZoneGroup: "011A"},
			{Code: "", UnitLabel: "A2", Status: "vacant", TimeZoneGroup: "011A"},
		},
	}
	st.Settings.Format = &api.FormatSpec{Mode: "full", Line: "{unit} {code} {tenant} {tz}", VacantLine: "{unit} 0 VACANT {tz}", LineEnding: "lf", SortBy: "unit"}
	b, err := (templateRenderer{}).Render(st)
	if err != nil {
		t.Fatal(err)
	}
	want := "A1 111111 Ann Chen 011A\nA2 0 VACANT 011A\n"
	if string(b) != want {
		t.Fatalf("vacant render = %q, want %q", string(b), want)
	}
	tmplMu.Lock()
	p := pending
	tmplMu.Unlock()
	if _, ok := p.Codes[""]; ok {
		t.Fatal("a vacant (codeless) row must never enter the code-keyed roster")
	}

	// Blank vacantLine = vacant units are left out, exactly as before.
	st.Settings.Format.VacantLine = ""
	b, err = (templateRenderer{}).Render(st)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "A1 111111 Ann Chen 011A\n" {
		t.Fatalf("blank vacantLine must omit vacant units, got %q", string(b))
	}

	// Delta mode never emits vacant rows, vacantLine or not.
	st.Settings.Format = &api.FormatSpec{Mode: "delta", AddedLine: "A,{code},{unit}", VacantLine: "{unit} 0", LineEnding: "lf"}
	b, err = (templateRenderer{}).Render(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "A2") {
		t.Fatalf("delta output must not carry vacant units, got %q", string(b))
	}
}
