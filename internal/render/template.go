// The template renderer - the universal vendor-format engine.
//
// Vendor gate-file layouts are per-site artifacts (specs are partner-only
// paper that drifts between versions), so the layout lives on the SERVER as a
// template the console can edit; this renderer just interpolates it. A format
// fix is a console edit picked up on the next poll - the customer's PC is
// never touched.
//
// Placeholders (contract §settings.format - lockstep with the server):
//
//	per-credential: {code} {unit} {tenant} {tz} {status}
//	anywhere:       {facility} {date} {time} {count}
//	modifiers:      {code:pad10}  zero-pad left to 10
//	                {tenant:width20}  space-pad right + truncate to 20
//	                {unit:rwidth6}    space-pad left + truncate to 6
//
// Modes:
//
//	full  - the file is the complete roster every write. Suspended codes use
//	        suspendedLine, or are simply OMITTED when it's empty (removal is
//	        the lockout for formats that can't express suspension).
//	delta - op-coded lines against the roster the gate software has
//	        CONFIRMED taking (addedLine/changedLine/removedLine, e.g. WinSen's
//	        A/E-prefixed update.txt). A bumped server forceNonce - or the
//	        local Force button - treats it as empty so everything re-emits as
//	        adds. Removals are written before changes and additions, so an
//	        importer keyed by unit never deletes the new tenant it just added.
//
// ⚠ THE DIFF IS AGAINST WHAT THE VENDOR TOOK, NOT WHAT WAS WRITTEN
// (2026-11-20). The roster used to advance the moment the file landed, before
// the vendor's import ran: a failed consume command still advanced it, and
// the next cycle REPLACED the unimported file with a diff that no longer had
// the earlier lines, so those adds and removals never reached the gate. Two
// rosters now live beside config.json:
//
//	last-roster.json          CONFIRMED - what the gate software has taken
//	last-roster.pending.json  PENDING   - what the file on disk now tells it
//
// Every render diffs against the confirmed roster. The pending one is
// promoted (ConfirmWritten) only when the consume command exits 0, or, with
// no consume command, when the vendor has removed or renamed the file by the
// next cycle (ConfirmIfTaken). A file still sitting there unimported is
// therefore replaced by a SUPERSET diff (every change since the confirmed
// roster), so nothing is lost; at worst a change is sent twice.
package render

import (
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/mytruckyards/unitrise-gate-bridge/internal/fsutil"
)

// RosterPath is where the CONFIRMED roster lives (set from the syncer to
// <config dir>/last-roster.json). Empty = delta mode has no memory and
// treats every render as a first run. The pending roster sits beside it
// (PendingPath).
var RosterPath string

var (
	tmplMu    sync.Mutex
	forceNext bool // local Force button: next render re-emits everything
	pending   *rosterFile
)

// PendingPath is the pending roster's file, beside the confirmed one.
func PendingPath() string {
	if RosterPath == "" {
		return ""
	}
	return strings.TrimSuffix(RosterPath, ".json") + ".pending.json"
}

// ForceNextFull makes the next template render treat the roster as empty -
// the local-dashboard sibling of the server's forceNonce.
func ForceNextFull() {
	tmplMu.Lock()
	forceNext = true
	tmplMu.Unlock()
}

// RecordWritten stores the roster the last Render computed as PENDING, with
// the file it was written to and a hash of the bytes, and is only called by
// the syncer AFTER the vendor file landed on disk - a failed write must not
// even become pending. It is NOT confirmed: the next render still diffs
// against the confirmed roster until ConfirmWritten or ConfirmIfTaken runs.
func RecordWritten(file string, data []byte) error {
	tmplMu.Lock()
	p := pending
	pending = nil
	tmplMu.Unlock()
	if RosterPath == "" {
		return nil
	}
	if p == nil {
		// This file carries no roster (the test renderer): an older pending
		// one no longer describes what is on disk, so it must never be
		// confirmed by this file's import.
		if err := os.Remove(PendingPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	sum := sha256.Sum256(data)
	p.File = file
	p.Sha256 = hex.EncodeToString(sum[:])
	return saveRoster(PendingPath(), p)
}

// ConfirmWritten promotes the pending roster to confirmed: the gate software
// has taken the file (its consume command exited 0).
func ConfirmWritten() error {
	path := PendingPath()
	if path == "" {
		return nil
	}
	p, err := readRoster(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing pending
		}
		return err
	}
	confirmed := &rosterFile{ForceNonce: p.ForceNonce, Codes: p.Codes}
	if err := saveRoster(RosterPath, confirmed); err != nil {
		return err
	}
	return os.Remove(path)
}

// ConfirmIfTaken is the confirmation for a site with NO consume command: the
// gate software watches the folder and takes the file itself, so the pending
// roster is confirmed once the file it was written to is gone from its path
// (the vendor removed or renamed it). A file still there, whatever it holds,
// is not taken: the next render replaces it with a superset diff. Returns
// whether it confirmed.
func ConfirmIfTaken() (bool, error) {
	path := PendingPath()
	if path == "" {
		return false, nil
	}
	p, err := readRoster(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if p.File == "" {
		return false, nil
	}
	if _, err := os.Lstat(p.File); !os.IsNotExist(err) {
		return false, nil // still there (or can't tell): not taken
	}
	return true, ConfirmWritten()
}

func saveRoster(path string, r *rosterFile) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, b, 0o600)
}

func readRoster(path string) (*rosterFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r := &rosterFile{}
	if err := json.Unmarshal(b, r); err != nil {
		return nil, err
	}
	if r.Codes == nil {
		r.Codes = map[string]rosterEntry{}
	}
	return r, nil
}

type rosterEntry struct {
	Unit   string `json:"unit"`
	Tenant string `json:"tenant"`
	Status string `json:"status"`
	// FlexString: rosters persisted before 2026-09-06 stored an int here.
	TZ api.FlexString `json:"tz"`
}

type rosterFile struct {
	ForceNonce int                    `json:"forceNonce"`
	Codes      map[string]rosterEntry `json:"codes"`
	// Pending only: this write re-emitted everything (a force), the file it
	// went to, and a hash of what was written.
	Full   bool   `json:"full,omitempty"`
	File   string `json:"file,omitempty"`
	Sha256 string `json:"sha256,omitempty"`
}

// loadRoster reads the CONFIRMED roster (an agent before 2026-11-20 wrote this
// file right after the write; it is the best record there is, so it is kept
// as confirmed).
func loadRoster() *rosterFile {
	r := &rosterFile{Codes: map[string]rosterEntry{}}
	if RosterPath == "" {
		return r
	}
	if got, err := readRoster(RosterPath); err == nil {
		return got
	}
	return r
}

// pendingIsFull: the unconfirmed write on disk was a full re-emit (a force),
// so the next render must be one too or the force is lost with it.
func pendingIsFull() bool {
	path := PendingPath()
	if path == "" {
		return false
	}
	p, err := readRoster(path)
	return err == nil && p.Full
}

type templateRenderer struct{}

func init() { Register("template", templateRenderer{}) }

func (templateRenderer) FileName() string { return "GATECODES.TXT" }

func (templateRenderer) Render(st *api.State) ([]byte, error) {
	spec := st.Settings.Format
	if spec == nil {
		return nil, fmt.Errorf("server sent the template renderer without a format - update the gate settings in the console")
	}

	creds := append([]api.Credential(nil), st.Credentials...)
	sortCreds(creds, spec.SortBy)

	// The "effective" desired set is what the GATE should know. When the
	// format can't express suspension (no suspendedLine), a suspended code's
	// expression is absence - so it must also be absent from the roster, or a
	// later restore would never re-add it. VACANT units (no code, sent so a
	// full write can overwrite the controller's per-unit record and clear a
	// stray pre-bridge code) render only in FULL mode through vacantLine -
	// delta formats speak in code changes and have nothing to say about a
	// codeless unit.
	canSuspend := spec.SuspendedLine != ""
	renderVacant := spec.Mode != "delta" && spec.VacantLine != ""
	effective := make([]api.Credential, 0, len(creds))
	for _, c := range creds {
		if c.Status == "suspended" && !canSuspend {
			continue
		}
		if c.Status == "vacant" && !renderVacant {
			continue
		}
		effective = append(effective, c)
	}

	var lines []string
	full := false
	switch spec.Mode {
	case "delta":
		lines, full = renderDelta(spec, st, effective)
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

	// Remember what this render told the gate; the syncer records it as
	// pending only after the file actually lands, and it is confirmed only
	// once the gate software takes the file.
	next := &rosterFile{ForceNonce: st.ForceNonce, Codes: map[string]rosterEntry{}, Full: full}
	for _, c := range effective {
		if c.Status == "vacant" {
			continue // no code to remember - the roster is keyed by code
		}
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
		switch c.Status {
		case "suspended":
			tpl = spec.SuspendedLine
		case "vacant":
			tpl = spec.VacantLine
		}
		if tpl == "" {
			continue
		}
		lines = append(lines, expand(tpl, c, st, 0))
	}
	return lines
}

func renderDelta(spec *api.FormatSpec, st *api.State, effective []api.Credential) (lines []string, full bool) {
	prior := loadRoster()
	tmplMu.Lock()
	fresh := forceNext
	forceNext = false
	tmplMu.Unlock()
	if !fresh && pendingIsFull() {
		fresh = true // the forced re-emit was never taken: send it again
	}
	if fresh || prior.ForceNonce != st.ForceNonce {
		prior = &rosterFile{ForceNonce: st.ForceNonce, Codes: map[string]rosterEntry{}}
		full = true
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

	// ORDER: removals, then changes, then additions. An importer keyed by
	// unit that read "A,new,12" and then "D,old,12" would delete unit 12's
	// NEW tenant; removing first can never undo a line written after it.
	var removed, changed, added []string
	seen := map[string]bool{}
	for i := range effective {
		c := &effective[i]
		seen[c.Code] = true
		old, existed := prior.Codes[c.Code]
		switch {
		case !existed:
			if tpl := pick(c, false); tpl != "" {
				added = append(added, expand(tpl, c, st, 0))
			}
		case old.Unit != c.UnitLabel || old.Tenant != c.TenantName || old.Status != c.Status || old.TZ != c.TimeZoneGroup:
			if tpl := pick(c, true); tpl != "" {
				changed = append(changed, expand(tpl, c, st, 0))
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
			removed = append(removed, expand(spec.RemovedLine, &c, st, 0))
		}
	}
	lines = append(lines, removed...)
	lines = append(lines, changed...)
	lines = append(lines, added...)
	return lines, full
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
				v = string(c.TimeZoneGroup)
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
