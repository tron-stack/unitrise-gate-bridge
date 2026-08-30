// The verification renderer — UnitRise's equivalent of the "storEDGE Test"
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

func init() { Register("unitrise_test", testFile{}) }

func (testFile) FileName() string { return "UNITRISE-GATE-TEST.txt" }

func (testFile) Render(st *api.State) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "UnitRise Gate Bridge — TEST OUTPUT (not a real vendor format)\n")
	fmt.Fprintf(&b, "Facility: %s\n", st.Facility.Name)
	fmt.Fprintf(&b, "Generated: %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "State hash: %s\n", st.StateHash)
	fmt.Fprintf(&b, "Codes: %d\n\n", len(st.Credentials))
	fmt.Fprintf(&b, "%-12s %-10s %-10s %-4s %s\n", "CODE", "UNIT", "STATUS", "TZ", "TENANT")
	for _, c := range st.Credentials {
		fmt.Fprintf(&b, "%-12s %-10s %-10s %-4d %s\n", c.Code, c.UnitLabel, c.Status, c.TimeZoneGroup, c.TenantName)
	}
	return b.Bytes(), nil
}
