//go:build !windows

package config

import "os"

// TrustProblem is Windows-only: elsewhere the config folder is created 0700
// by root, and no other account can write into it.
func TrustProblem() string { return "" }

func isLink(fi os.FileInfo) bool { return fi.Mode()&os.ModeSymlink != 0 }
