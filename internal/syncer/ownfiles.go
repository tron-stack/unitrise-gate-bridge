package syncer

// ⚠ THE AGENT'S OWN FILES DON'T OUTLIVE THEIR USE (2026-11-20). Before a gate
// format is saved the agent writes UNITRISE-GATE-TEST.txt, and with a refused
// file name it writes GATECODES.TXT; both carry every code and tenant name,
// in a folder ordinary users can usually read. Once the output moves to the
// gate software's own file, the old one used to stay there for ever.
//
// So the agent records, beside config.json, each file it wrote under one of
// its own default names with a hash of the bytes, and once the output has
// moved on it removes that file - only when it is still a plain file holding
// exactly what the agent wrote (anything else means somebody else has touched
// it, and it is left alone), and never the file it has just written. A test
// file left by an agent from before this record is recognised by its first
// line, which only this agent writes.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/fsutil"
	"github.com/mytruckyards/unitrise-gate-bridge/internal/render"
)

type ownFilesRecord struct {
	// Files maps a path the agent wrote to the hex sha256 of what it wrote.
	Files map[string]string `json:"files"`
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// isOwnDefaultName: one of the agent's own renderers' default file names.
func isOwnDefaultName(name string) bool {
	for _, n := range render.DefaultFileNames() {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

func loadOwnFiles(path string) ownFilesRecord {
	rec := ownFilesRecord{Files: map[string]string{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &rec)
	}
	if rec.Files == nil {
		rec.Files = map[string]string{}
	}
	return rec
}

// cleanOwnFiles runs after every successful write of target.
func (s *Syncer) cleanOwnFiles(target string, data []byte) {
	if s.ownFilesPath == "" {
		return
	}
	target = filepath.Clean(target)
	targetInfo, _ := os.Stat(target)
	rec := loadOwnFiles(s.ownFilesPath)
	changed := false
	for path, sum := range rec.Files {
		if path == target {
			continue // rewritten below
		}
		if removeIfOurs(path, targetInfo, func(b []byte) bool { return hashHex(b) == sum }) {
			s.log.Infof("removed the agent's own old output %s - the gate file is %s now", path, target)
		}
		delete(rec.Files, path)
		changed = true
	}
	// A test file from an agent that predates the record.
	legacy := filepath.Clean(filepath.Join(s.cfg.SavePath, render.TestFileName))
	if _, tracked := rec.Files[legacy]; !tracked && legacy != target {
		if removeIfOurs(legacy, targetInfo, isTestFileContent) {
			s.log.Infof("removed the agent's own old test file %s - the gate file is %s now", legacy, target)
		}
	}
	if isOwnDefaultName(filepath.Base(target)) {
		if sum := hashHex(data); rec.Files[target] != sum {
			rec.Files[target] = sum
			changed = true
		}
	} else if _, ok := rec.Files[target]; ok {
		delete(rec.Files, target)
		changed = true
	}
	if !changed {
		return
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}
	if err := fsutil.WriteFileAtomic(s.ownFilesPath, b, 0o600); err != nil {
		s.log.Errorf("own-file record: %v", err)
	}
}

// removeIfOurs removes path when it is a plain file (not a link, not a
// folder), is NOT the file just written (the same file under another case or
// spelling), and its content passes ours. Reports whether it removed it.
func removeIfOurs(path string, target os.FileInfo, ours func([]byte) bool) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if target != nil && os.SameFile(fi, target) {
		return false
	}
	b, err := os.ReadFile(path)
	if err != nil || !ours(b) {
		return false
	}
	return os.Remove(path) == nil
}

// isTestFileContent: the verification renderer's own first line.
func isTestFileContent(b []byte) bool {
	line, err := bufio.NewReader(strings.NewReader(string(b))).ReadString('\n')
	if err != nil && err != io.EOF {
		return false
	}
	return strings.TrimRight(line, "\r\n") == render.TestFileHeader
}
