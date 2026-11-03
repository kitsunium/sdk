package s3

import (
	"io/fs"
	"time"
)

// fileInfo is what Stat, a file's Stat and a directory entry's Info report.
type fileInfo struct {
	modTime time.Time
	name    string
	size    int64
	mode    fs.FileMode
}

// Name is the base name.
func (i fileInfo) Name() string {
	//: as io/fs names it.
	return i.name
}

// Size is the object's length; zero for a directory.
func (i fileInfo) Size() int64 {
	//: as S3 answered.
	return i.size
}

// Mode is the recorded permission bits, fs.ModeDir for a directory.
func (i fileInfo) Mode() fs.FileMode {
	//: ModeMetadata, or the default.
	return i.mode
}

// ModTime is the object's Last-Modified, to the second; zero for a directory
// with no marker.
func (i fileInfo) ModTime() time.Time {
	//: S3's clock.
	return i.modTime
}

// IsDir reports whether the name is a directory.
func (i fileInfo) IsDir() bool {
	//: from the mode.
	return i.mode.IsDir()
}

// Sys is nil: nothing beneath the description is exposed.
func (i fileInfo) Sys() any {
	//: no vendor type leaks.
	return nil
}
