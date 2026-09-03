//go:build !windows

package config

// Unix file modes (0700 dir, 0600 config) already keep the secret private -
// nothing extra to do.
func secureDir(string) error { return nil }
