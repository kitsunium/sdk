// Package selfupdate — the three ports a self-update needs from its
// environment: the network, the disk, and the stream between them.
package selfupdate

import (
	"io"
	"net/http"
	"os"
)

// Getter performs the HTTP GETs a self-update needs: the release metadata, the
// archive, the checksum manifest and its signature.
//
// It is one method because that is all the domain needs, and a caller supplying
// a policy-carrying client (timeouts, redirect rules, proxies) is supplying it
// once for all four.
type Getter interface {
	// Get performs an HTTP GET request.
	Get(url string) (resp *http.Response, err error)
}

// FileSystem is the disk half of replacing a running binary.
//
// EvalSymlinks is not optional decoration: an executable reached through a
// symlink must be replaced at its REAL path, or the update writes over the link
// and the next run still executes the old binary.
type FileSystem interface {
	// Executable returns the path of the current executable.
	Executable() (path string, err error)
	// EvalSymlinks resolves symlinks in the given path.
	EvalSymlinks(path string) (resolved string, err error)
	// CreateTemp creates a temporary file.
	CreateTemp(dir, pattern string) (file *os.File, err error)
	// Chmod changes file permissions.
	Chmod(name string, mode os.FileMode) error
	// Rename renames a file. It is the atomic half of the replacement and must
	// happen on the same filesystem as the target.
	Rename(oldpath, newpath string) error
	// Remove removes a file.
	Remove(name string) error
}

// Copier streams the verified archive to its temporary destination. It is a port
// so a test can fail the copy midway — the case where a partially written binary
// must never be renamed into place.
type Copier interface {
	// Copy copies from src to dst, returning the number of bytes written.
	Copy(dst io.Writer, src io.Reader) (written int64, err error)
}
