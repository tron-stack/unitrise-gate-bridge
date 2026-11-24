//go:build windows

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/acl"
)

// TrustProblem reads the config folder's permissions, and those of
// config.json and each state file in it, and says why they can't be trusted
// ("" = they can, or the folder doesn't exist yet).
func TrustProblem() string {
	dir := Dir()
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return ""
	}
	folder, err := acl.Read(dir)
	if err != nil {
		return fmt.Sprintf("The Bridge couldn't read who can change the config folder (%s: %v)", dir, err)
	}
	var files []acl.Object
	for _, name := range append([]string{filepath.Base(Path())}, StateFiles...) {
		p := filepath.Join(dir, name)
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		f, err := acl.Read(p)
		if err != nil {
			return fmt.Sprintf("The Bridge couldn't read who can change %s (%v)", p, err)
		}
		files = append(files, f)
	}
	return trustDecision(folder, files)
}

func isLink(fi os.FileInfo) bool {
	if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return true
	}
	d, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	return ok && d.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
