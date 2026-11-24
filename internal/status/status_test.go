package status

import (
	"encoding/json"
	"strings"
	"testing"
)

// The local dashboard answers any account on the gate PC, so the roster it
// serves never carries a full code.
func TestTheRosterNeverHoldsAFullCode(t *testing.T) {
	SetRoster([]RosterRow{
		{Unit: "A14", Tenant: "Maria Torres", Code: "482913", Status: "active", TZ: "011A"},
		{Unit: "B02", Code: "", Status: "vacant"},
	})
	rows := Roster()
	if rows[0].Code != "••••13" {
		t.Fatalf("roster code = %q, want only the last two digits", rows[0].Code)
	}
	b, _ := json.Marshal(map[string]any{"rows": rows})
	if strings.Contains(string(b), "482913") {
		t.Fatalf("the served roster carries the full code: %s", b)
	}
	if rows[1].Code != "" {
		t.Fatalf("a vacant row has no code to mask: %q", rows[1].Code)
	}
}
