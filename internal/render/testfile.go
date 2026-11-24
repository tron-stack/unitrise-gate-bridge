// The verification renderer - UnitRise's equivalent of the "storEDGE Test"
// provider. It writes a human-readable file so an installer can prove the
// whole loop (cloud → agent → folder → consume command) end to end BEFORE any
// real gate hardware is in the picture.
package render

import (
	"bytes"
	"fmt"
	"time"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
)

type testFile struct{}

// TestFileName is the verification renderer's file, and TestFileHeader its
// first line, written by nothing but this agent (the syncer uses both to
// remove the agent's own test file once a real format takes over).
const (
	TestFileName   = "UNITRISE-GATE-TEST.txt"
	TestFileHeader = "UnitRise Gate Bridge - TEST OUTPUT (not a real vendor format)"
)

func init() { Register("unitrise_test", testFile{}) }

func (testFile) FileName() string { return TestFileName }

func (testFile) Render(st *api.State) ([]byte, error) {
	// This file carries no roster: drop one a template render left behind
	// (a write that failed), so it is never recorded against this file.
	tmplMu.Lock()
	pending = nil
	tmplMu.Unlock()
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s\n", TestFileHeader)
	fmt.Fprintf(&b, "Facility: %s\n", st.Facility.Name)
	fmt.Fprintf(&b, "Generated: %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "State hash: %s\n", st.StateHash)
	fmt.Fprintf(&b, "Codes: %d\n\n", len(st.Credentials))
	fmt.Fprintf(&b, "%-12s %-10s %-10s %-4s %s\n", "CODE", "UNIT", "STATUS", "TZ", "TENANT")
	for _, c := range st.Credentials {
		fmt.Fprintf(&b, "%-12s %-10s %-10s %-4s %s\n", c.Code, c.UnitLabel, c.Status, string(c.TimeZoneGroup), c.TenantName)
	}
	return b.Bytes(), nil
}
