//go:build !linux

package seglog

import "os"

// datasync is os.File.Sync off Linux: F_FULLFSYNC on darwin, stronger and
// slower than fdatasync, and the plain fsync elsewhere.
func datasync(f *os.File) error { return f.Sync() }
