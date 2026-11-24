package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The data is flushed to disk BEFORE the rename puts it in place: a power cut
// between the two must leave the old file, never an empty one.
func TestWriteFlushesBeforeRename(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "UPDATE.DAT")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	var events []string
	origFile, origDir, origRename := syncFile, syncDir, rename
	t.Cleanup(func() { syncFile, syncDir, rename = origFile, origDir, origRename })
	syncFile = func(f *os.File) error {
		// At flush time the target still holds the old bytes and the temp
		// file already holds every new byte.
		if b, _ := os.ReadFile(target); string(b) != "old" {
			t.Errorf("the target changed before the data was flushed: %q", b)
		}
		if b, _ := os.ReadFile(f.Name()); string(b) != "new contents" {
			t.Errorf("the temp file did not hold the data when flushed: %q", b)
		}
		events = append(events, "sync-file")
		return origFile(f)
	}
	rename = func(from, to string) error {
		events = append(events, "rename")
		return origRename(from, to)
	}
	syncDir = func(d string) error {
		events = append(events, "sync-dir")
		return origDir(d)
	}
	if err := WriteFileAtomic(target, []byte("new contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, ","); got != "sync-file,rename,sync-dir" {
		t.Fatalf("order = %s, want sync-file,rename,sync-dir", got)
	}
	if b, _ := os.ReadFile(target); string(b) != "new contents" {
		t.Fatalf("target = %q", b)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("a temp file was left behind: %v", entries)
	}
}

// The temp name is random and created exclusively, so a link planted at a
// predictable name is never written through.
func TestWriteNeverFollowsAPlantedLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows; the rule is the same code path")
	}
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(outside, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "UPDATE.DAT")
	// The old agent's fixed temp name, planted as a link.
	if err := os.Symlink(outside, target+".unitrise-tmp"); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(target, []byte("codes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep me" {
		t.Fatalf("the write went through the planted link: %q", b)
	}
}

func TestAFailedFlushLeavesTheOldFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.json")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := syncFile
	t.Cleanup(func() { syncFile = orig })
	syncFile = func(*os.File) error { return os.ErrInvalid }
	if err := WriteFileAtomic(target, []byte("new"), 0o600); err == nil {
		t.Fatal("a failed flush must fail the write")
	}
	if b, _ := os.ReadFile(target); string(b) != "old" {
		t.Fatalf("a failed flush replaced the file: %q", b)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("a failed write left a temp file: %v", entries)
	}
}
