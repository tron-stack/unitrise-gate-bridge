package config

// ⚠ THE CONFIG FOLDER IS TRUSTED ONLY WHEN ONLY SYSTEM AND ADMINISTRATORS
// COULD HAVE WRITTEN IT (2026-11-20). %ProgramData% lets every user create a
// folder, and the agent used to create its own only at the first pairing (or
// when the logger first opened agent.log). So on a fresh machine any local
// user could create %ProgramData%\UnitRiseGateBridge first and plant a
// config.json pointing at their own server, and the service, running as
// SYSTEM, loaded it as a real pairing. A planted own-files.json is worse: it
// tells the agent which files to delete.
//
// So the agent (and both installers, before the service exists) checks the
// folder and every file it reads from it against an allow-list
// (internal/acl PrivateProblem): a real folder, not a link, owned by SYSTEM or
// Administrators, changeable by nobody else; and each file the same. Anything
// else is not trusted:
//
//   - Load refuses it (UntrustedError) and the agent runs UNPAIRED;
//   - Prepare (the service's start, the installers) QUARANTINES the folder:
//     a link is removed (never what it points at), the folder is re-owned by
//     Administrators and limited to SYSTEM and Administrators with
//     inheritance off, and config.json and the agent's state files in it are
//     deleted, because nothing in them can be trusted.
//
// A folder that already passes is kept as it is, pairing included, and its
// owner and permissions are set again: only an Administrator or SYSTEM can
// produce a file owned by Administrators or SYSTEM, so a trusted config.json
// was written by one of them. The check reads Windows permissions
// (trust_windows.go); elsewhere the folder is root's 0700 and nothing is read.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/acl"
)

// StateFiles are the files the agent reads from the config folder besides
// config.json (the delta rosters, the own-files record, the log). They are
// trusted, or deleted, together with it.
var StateFiles = []string{"last-roster.json", "last-roster.pending.json", "own-files.json", "agent.log", "agent.log.old"}

// ErrUntrusted marks a config Load refused because of who could have written it.
var ErrUntrusted = errors.New("untrusted config folder")

// UntrustedError carries the reason in words.
type UntrustedError struct{ Problem string }

func (e *UntrustedError) Error() string {
	return e.Problem + ", so the Bridge won't trust the pairing in it"
}

func (e *UntrustedError) Is(target error) bool { return target == ErrUntrusted }

// trustDecision judges the config folder and the files read from it. ""
// means trusted.
func trustDecision(folder acl.Object, files []acl.Object) string {
	if p := acl.PrivateProblem(folder, "The config folder"); p != "" {
		return p
	}
	for _, f := range files {
		name := f.Path
		if i := strings.LastIndexAny(name, `\/`); i >= 0 {
			name = name[i+1:]
		}
		if p := acl.PrivateProblem(f, "The file "+name); p != "" {
			return p
		}
	}
	return ""
}

// Hooks (tests stand in for Windows).
var (
	checkTrust = TrustProblem
	secure     = secureDir
)

// PrepareResult says what Prepare found and did.
type PrepareResult struct {
	Dir         string
	Created     bool     // the folder didn't exist
	KeptPairing bool     // an existing, trusted config.json was kept
	Problem     string   // why the folder couldn't be trusted ("" = it could)
	Removed     []string // what was deleted from it
}

// Summary is one line for the installer's output or the agent's log.
func (r PrepareResult) Summary() string {
	switch {
	case r.Problem != "":
		removed := "nothing else was in it"
		if len(r.Removed) > 0 {
			removed = "deleted " + strings.Join(r.Removed, ", ")
		}
		return fmt.Sprintf("%s, so the Bridge didn't trust it: it %s, and limited %s to SYSTEM and Administrators. Pair this machine again.", r.Problem, removed, r.Dir)
	case r.Created:
		return fmt.Sprintf("created %s, limited to SYSTEM and Administrators", r.Dir)
	case r.KeptPairing:
		return fmt.Sprintf("kept the existing pairing in %s (only SYSTEM and Administrators could have written it) and limited the folder to them again", r.Dir)
	}
	return fmt.Sprintf("%s is limited to SYSTEM and Administrators", r.Dir)
}

// Prepare makes the config folder trustworthy BEFORE anything reads it: the
// installers call it before the service exists, and the service calls it at
// start. Needs Administrator rights (or SYSTEM) on Windows.
func Prepare() (PrepareResult, error) {
	dir := Dir()
	res := PrepareResult{Dir: dir}
	if p := checkTrust(); p != "" {
		res.Problem = p
		removed, err := quarantine(dir)
		res.Removed = removed
		return res, err
	}
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		res.Created = true
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return res, err
	}
	if err := secure(dir); err != nil {
		return res, fmt.Errorf("couldn't limit %s to SYSTEM and Administrators: %w", dir, err)
	}
	if _, err := os.Stat(Path()); err == nil {
		res.KeptPairing = true
	}
	return res, nil
}

// quarantine: remove a link at the folder's place (never what it points at),
// make a real folder, own and limit it, and delete config.json and the state
// files in it.
func quarantine(dir string) ([]string, error) {
	var removed []string
	if fi, err := os.Lstat(dir); err == nil && (isLink(fi) || !fi.IsDir()) {
		if err := os.Remove(dir); err != nil {
			return removed, fmt.Errorf("couldn't remove %s, which isn't a real folder: %w - an Administrator must delete it", dir, err)
		}
		removed = append(removed, dir+" (not a real folder)")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return removed, err
	}
	// Limit the folder FIRST, so nothing new can be put in it while it is
	// being emptied.
	if err := secure(dir); err != nil {
		return removed, fmt.Errorf("couldn't limit %s to SYSTEM and Administrators: %w - an Administrator must delete the folder", dir, err)
	}
	for _, name := range append([]string{filepath.Base(Path())}, StateFiles...) {
		p := filepath.Join(dir, name)
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		if err := os.Remove(p); err != nil {
			return removed, fmt.Errorf("couldn't delete %s: %w - an Administrator must delete it", p, err)
		}
		removed = append(removed, name)
	}
	return removed, nil
}
