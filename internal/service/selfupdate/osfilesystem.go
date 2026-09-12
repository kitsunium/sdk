// Package selfupdate replaces the running binary with a newer signed release.
package selfupdate

import (
	"os"
	"path/filepath"
)

// osFileSystem implements FileSystem using standard os package.
type osFileSystem struct{}

// Executable returns the path of the current executable.
func (osFileSystem) Executable() (path string, execErr error) {
	//: Use standard library to retrieve current executable path.
	return os.Executable()
}

// EvalSymlinks resolves symlinks in the given path.
func (osFileSystem) EvalSymlinks(path string) (resolved string, evalErr error) {
	//: Follow symlinks to find the real executable location.
	return filepath.EvalSymlinks(path)
}

// CreateTemp creates a temporary file.
func (osFileSystem) CreateTemp(dir, pattern string) (tmpFile *os.File, createErr error) {
	//: Create secure temporary file for binary download.
	return os.CreateTemp(dir, pattern)
}

// Chmod changes file permissions.
func (osFileSystem) Chmod(name string, mode os.FileMode) error {
	//: Set executable permissions before binary replacement.
	return os.Chmod(name, mode)
}

// Rename renames a file.
func (osFileSystem) Rename(oldpath, newpath string) error {
	//: Atomically replace old binary with downloaded version.
	return os.Rename(oldpath, newpath)
}

// Remove removes a file.
func (osFileSystem) Remove(name string) error {
	//: Clean up temporary file on error.
	return os.Remove(name)
}
