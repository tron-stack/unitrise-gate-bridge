//go:build !windows

package fsutil

import "os"

// syncDirectory flushes the folder entry so the rename itself survives a
// power cut.
func syncDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func renameDurable(from, to string) error { return os.Rename(from, to) }
