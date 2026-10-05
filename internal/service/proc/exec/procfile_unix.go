//go:build unix

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
