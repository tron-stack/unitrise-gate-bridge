// Package fsutil holds the one way the agent replaces a file it owns: the
// gate file the gate software imports, config.json, and the delta rosters.
//
// A reader (the gate software, or the agent itself after a power cut) must
// see the old file or the new one, never half of one, and never an empty
// file because the rename reached the disk before the data did. So the data
// goes into a fresh temporary file beside the target, is flushed to disk
// (fsync), and only then is renamed over the target; the folder entry is
// flushed afterwards where the OS supports it (on Windows the rename itself
// is asked to write through instead).
//
// The temporary file gets a RANDOM name created with O_EXCL (os.CreateTemp):
// the gate file lives in a folder ordinary users can usually write, and a
// fixed temp name there could be pre-planted as a link that the agent, running
// as the system account, would then write through.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// Hooks the tests use to see the order of the flush and the rename.
var (
	syncFile = func(f *os.File) error { return f.Sync() }
	syncDir  = syncDirectory
	rename   = renameDurable
)

// WriteFileAtomic replaces path with data, durably. perm applies where the
// OS honors file modes.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".unitrise-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()      //nolint:errcheck - already failing
			os.Remove(tmp) //nolint:errcheck - best effort; never leave a stray
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := syncFile(f); err != nil {
		return fmt.Errorf("flush %s to disk: %w", filepath.Base(path), err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	if err := rename(tmp, path); err != nil {
		return err
	}
	ok = true
	// The rename is done; a failure to flush the folder entry is reported but
	// the new file is already in place.
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("flush folder %s: %w", dir, err)
	}
	return nil
}
