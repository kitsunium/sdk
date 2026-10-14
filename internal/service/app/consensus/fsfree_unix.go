//go:build linux || darwin

package consensus

import "syscall"

// fsFree returns the bytes an unprivileged writer may still use on the
// filesystem holding dir, -1 when unknown.
func fsFree(dir string) int64 {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return -1
	}
	return int64(s.Bavail) * blockSize(s.Bsize)
}

// blockSize widens Statfs_t.Bsize, whose type is the platform's: int64 on
// linux/amd64 and arm64, int32 on linux/386 and arm, uint32 on darwin. The
// conversion is required on all but the first; written over a type parameter
// it is one conversion that holds on every one of them, where int64(s.Bsize)
// reads as redundant to a linter run on linux/amd64 (KTN-VAR-TYPECONV,
// kodflow/ktn-linter#725).
func blockSize[T int32 | uint32 | int64](v T) int64 { return int64(v) }
