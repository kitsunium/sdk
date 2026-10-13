//go:build !linux

package consensus

import (
	"os"
	"path/filepath"
)

// datasync is os.File.Sync off Linux: F_FULLFSYNC on darwin, stronger and
// slower than fdatasync, and the plain fsync elsewhere (D4, R8-06).
func datasync(f *os.File) error { return f.Sync() }

// runtimeMarkerDir is the second marker's place: the per-boot temporary
// directory the platform offers.
func runtimeMarkerDir() string { return filepath.Join(os.TempDir(), "kitsunium-consensus") }
