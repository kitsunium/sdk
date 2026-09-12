// Package selfupdate replaces the running binary with a newer signed release.
package selfupdate

import (
	"io"
	"net/http"
	"os"
)

// Getter defines the interface for HTTP GET operations.
// This allows for dependency injection and easier testing.
type Getter interface {
	// Get performs an HTTP GET request.
	Get(url string) (*http.Response, error)
}

// FileSystem defines the interface for file system operations.
// This allows for dependency injection and easier testing.
type FileSystem interface {
	// Executable returns the path of the current executable.
	Executable() (string, error)

	// EvalSymlinks resolves symlinks in the given path.
	EvalSymlinks(path string) (string, error)

	// CreateTemp creates a temporary file.
	CreateTemp(dir, pattern string) (*os.File, error)

	// Chmod changes file permissions.
	Chmod(name string, mode os.FileMode) error

	// Rename renames a file.
	Rename(oldpath, newpath string) error

	// Remove removes a file.
	Remove(name string) error
}

// Copier defines the interface for io.Copy operations.
// This allows for dependency injection and easier testing.
type Copier interface {
	// Copy copies from src to dst.
	Copy(dst io.Writer, src io.Reader) (int64, error)
}
