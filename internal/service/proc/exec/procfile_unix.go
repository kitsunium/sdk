//go:build unix

// Package exec — Unix procfs writer: a tiny os.WriteFile shim used to set
// /proc/<pid>/oom_score_adj, isolated so the platform-specific path handling
// stays out of the attribute logic.
package exec

import "os"

// procFileMode is the os.WriteFile mode for procfs control files; the kernel
// ignores it for existing procfs entries, so it only satisfies the signature.
const procFileMode os.FileMode = 0o644

// writeProcFile writes data to a procfs control file at path.
func writeProcFile(path, data string) error {
	//: a single short write to the procfs control file; the kernel validates it.
	return os.WriteFile(path, []byte(data), procFileMode)
}
