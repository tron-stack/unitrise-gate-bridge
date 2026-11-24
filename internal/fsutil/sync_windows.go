//go:build windows

package fsutil

import "golang.org/x/sys/windows"

// Windows has no folder fsync (FlushFileBuffers refuses a directory handle).
// The rename is asked to write through instead, which is how Windows makes a
// move durable.
func syncDirectory(string) error { return nil }

func renameDurable(from, to string) error {
	f, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	t, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(f, t, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
