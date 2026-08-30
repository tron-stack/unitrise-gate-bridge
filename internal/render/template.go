// The template renderer — the universal vendor-format engine.
//
// Vendor gate-file layouts are per-site artifacts (specs are partner-only
// paper that drifts between versions), so the layout lives on the SERVER as a
// template the console can edit; this renderer just interpolates it. A format
// fix is a console edit picked up on the next poll — the customer's PC is
// never touched.
//
// Placeholders (contract §settings.format — lockstep with the server):
//
//	per-credential: {code} {unit} {tenant} {tz} {status}
//	anywhere:       {facility} {date} {time} {count}
//	modifiers:      {code:pad10}  zero-pad left to 10
//	                {tenant:width20}  space-pad right + truncate to 20
//	                {unit:rwidth6}    space-pad left + truncate to 6
//
// Modes:
//
//	full  — the file is the complete roster every write. Suspended codes use
//	        suspendedLine, or are simply OMITTED when it's empty (removal is
//	        the lockout for formats that can't express suspension).
//	delta — op-coded lines against the last roster this agent applied
//	        (addedLine/changedLine/removedLine, e.g. WinSen's A/E-prefixed
//	        update.txt). The roster persists in the agent's data dir; a
//	        bumped server forceNonce — or the local Force button — clears it
//	        so everything re-emits as adds.
package render

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/api"
)

// RosterPath is where the last-applied roster lives (set from main to
// <config dir>/last-roster.json). Empty = delta mode has no memory and
// treats every render as a first run.
var RosterPath string

var (
	tmplMu    sync.Mutex
	forceNext bool // local Force button: next render re-emits everything
	pending   *rosterFile
)

// ForceNextFull makes the next template render treat the roster as empty —
// the local-dashboard sibling of the server's forceNonce.
func ForceNextFull() {
	tmplMu.Lock()
	forceNext = true
	tmplMu.Unlock()
}

// CommitApplied persists the roster computed by the last Render, and is only
// called by the syncer AFTER the vendor file landed on disk — a failed write
// must not advance the gate's remembered state.
func CommitApplied() error {
	tmplMu.Lock()
	p := pending
	pending = nil
	tmplMu.Unlock()
	if p == nil || RosterPath == "" {
		return nil
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := RosterPath + ".tmp"
	if err := os.MkdirAll(filepath.Dir(RosterPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, RosterPath)
}

type rosterEntry struct {
	Unit   string `json:"unit"`
	Tenant string `json:"tenant"`
	Status string `json:"status"`
	TZ     int    `json:"tz"`
}

type rosterFile struct {
	ForceNonce int                    `json:"forceNonce"`
	Codes      map[string]rosterEntry `json:"codes"`
}

func loadRoster() *rosterFile {
	r := &rosterFile{Codes: map[string]rosterEntry{}}
	if RosterPath == "" {
		return r
	}
	b, err := os.ReadFile(RosterPath)
	if err != nil {
		return r
	}
	_ = json.Unmarshal(b, r)
	if r.Codes == nil {
		r.Codes = map[string]rosterEntry{}
	}
	return r
}

type templateRenderer struct{}

func init() { Register("template", templateRenderer{}) }

func (templateRenderer) FileName() string { return "GATECODES.TXT" }

func (templateRenderer) Render(st *api.State) ([]byte, error) {
	spec := st.Settings.Format
	if spec == nil {
		return nil, fmt.Errorf("server sent the template renderer without a format — update the gate settings in the console")
	}

	creds := append([]api.Credential(nil), st.Credentials...)
	sortCreds(creds, spec.SortBy)

	// The "effective" desired set is what the GATE should know. When the
	// format can't express suspension (no suspendedLine), a suspended code's
	// expression is absence — so it must also be absent from the roster, or a
	// later restore would never re-add it.
	canSuspend := spec.SuspendedLine != ""
	effective := make([]api.Credential, 0, len(creds))
	for _, c := range creds {
		if c.Status == "suspended" && !canSuspend {
			continue
		}
		effective = append(effective, c)
	}

	var lines []string
	switch spec.Mode {
	case "delta":
		lines = renderDelta(spec, st, effective)
	default:
		lines = renderFull(spec, st, effective)
	}

	count := len(lines)
	var out []string
	if spec.Header != "" {
		out = append(out, expand(spec.Header, nil, st, count))
	}
	out = append(out, lines...)
	if spec.Footer != "" {
		out = append(out, expand(spec.Footer, nil, st, count))
	}

	// Vendors' DOS-lineage importers want a line terminator after EVERY line
	// (the Falcon manual is explicit about the trailing carriage return).
	body := strings.Join(out, "\n") + "\n"
	if spec.LineEnding != "lf" {
		body = strings.ReplaceAll(body, "\n", "\r\n")
	}

	// Remember what this render told the gate; the syncer commits it only
	// after the file actually lands.
	next := &rosterFile{ForceNonce: st.ForceNonce, Codes: map[string]rosterEntry{}}
	for _, c := range effective {
		next.Codes[c.Code] = rosterEntry{Unit: c.UnitLabel, Tenant: c.TenantName, Status: c.Status, TZ: c.TimeZoneGroup}
	}
	tmplMu.Lock()
	pending = next
	tmplMu.Unlock()

	return []byte(body), nil
}

func renderFull(spec *api.FormatSpec, st *api.State, effective []api.Credential) []string {
	var lines []string
	for i := range effective {
		c := &effective[i]
		tpl := spec.Line
		if c.Status == "suspended" {
			tpl = spec.SuspendedLine
		}
		if tpl == "" {
			continue
		}
		lines = append(lines, expand(tpl, c, st, 0))
	}
	return lines
}

func renderDelta(spec *api.FormatSpec, st *api.State, effective []api.Credential) []string {
	prior := loadRoster()
	tmplMu.Lock()
	fresh := forceNext
	forceNext = false
	tmplMu.Unlock()
	if fresh || prior.ForceNonce != st.ForceNonce {
		prior = &rosterFile{ForceNonce: st.ForceNonce, Codes: map[string]rosterEntry{}}
	}

	pick := func(c *api.Credential, changed bool) string {
		if changed && spec.ChangedLine != "" {
			return spec.ChangedLine
		}
		if c.Status == "suspended" && spec.SuspendedLine != "" {
			return spec.SuspendedLine
		}
		if spec.AddedLine != "" {
			return spec.AddedLine
		}
		return spec.Line
	}

	var lines []string
	seen := map[string]bool{}
	for i := range effective {
		c := &effective[i]
		seen[c.Code] = true
		old, existed := prior.Codes[c.Code]
		switch {
		case !existed:
			if tpl := pick(c, false); tpl != "" {
				lines = append(lines, expand(tpl, c, st, 0))
			}
		case old.Unit != c.UnitLabel || old.Tenant != c.TenantName || old.Status != c.Status || old.TZ != c.TimeZoneGroup:
			if tpl := pick(c, true); tpl != "" {
				lines = append(lines, expand(tpl, c, st, 0))
			}
		}
	}
	if spec.RemovedLine != "" {
		// Stable removal order: sort the departed codes.
		var gone []string
		for code := range prior.Codes {
			if !seen[code] {
				gone = append(gone, code)
			}
		}
		sort.Strings(gone)
		for _, code := range gone {
			old := prior.Codes[code]
			c := api.Credential{Code: code, UnitLabel: old.Unit, TenantName: old.Tenant, Status: old.Status, TimeZoneGroup: old.TZ}
			lines = append(lines, expand(spec.RemovedLine, &c, st, 0))
		}
	}
	return lines
}

func sortCreds(creds []api.Credential, key string) {
	switch key {
	case "none":
	case "unit":
		sort.SliceStable(creds, func(i, j int) bool { return creds[i].UnitLabel < creds[j].UnitLabel })
	case "name":
		sort.SliceStable(creds, func(i, j int) bool { return creds[i].TenantName < creds[j].TenantName })
	default: // code
		sort.SliceStable(creds, func(i, j int) bool { return creds[i].Code < creds[j].Code })
	}
}

var placeholderRe = regexp.MustCompile(`\{(code|unit|tenant|tz|status|facility|date|time|count)(?::(pad|width|rwidth)(\d{1,3}))?\}`)

func expand(tpl string, c *api.Credential, st *api.State, count int) string {
	now := time.Now()
	return placeholderRe.ReplaceAllStringFunc(tpl, func(m string) string {
		parts := placeholderRe.FindStringSubmatch(m)
		var v string
		switch parts[1] {
		case "code":
			if c != nil {
				v = c.Code
			}
		case "unit":
			if c != nil {
				v = c.UnitLabel
			}
		case "tenant":
			if c != nil {
				v = c.TenantName
			}
		case "tz":
			if c != nil {
				v = strconv.Itoa(c.TimeZoneGroup)
			}
		case "status":
			if c != nil {
				v = c.Status
			}
		case "facility":
			v = st.Facility.Name
		case "date":
			v = now.Format("01/02/2006")
		case "time":
			v = now.Format("15:04:05")
		case "count":
			v = strconv.Itoa(count)
		}
		if parts[2] != "" {
			n, _ := strconv.Atoi(parts[3])
			switch parts[2] {
			case "pad":
				for len(v) < n {
					v = "0" + v
				}
			case "width":
				if len(v) > n {
					v = v[:n]
				}
				v += strings.Repeat(" ", n-len(v))
			case "rwidth":
				if len(v) > n {
					v = v[:n]
				}
				v = strings.Repeat(" ", n-len(v)) + v
			}
		}
		return v
	})
}
