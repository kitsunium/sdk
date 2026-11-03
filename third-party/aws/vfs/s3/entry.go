package s3

import (
	"io/fs"
	"path"
)

// dirEntry is one name a listing found. Its Info is read when asked for — a
// HeadObject — so a listing costs its pages and no request per entry.
type dirEntry struct {
	fsys *s3FS
	path string
	name string
	dir  bool
}

// Name is the base name.
func (e dirEntry) Name() string {
	//: as the listing split it.
	return e.name
}

// IsDir reports whether the entry is a directory.
func (e dirEntry) IsDir() bool {
	//: a common prefix.
	return e.dir
}

// Type is the entry's type bits.
func (e dirEntry) Type() fs.FileMode {
	//: a directory or a regular file; S3 holds nothing else.
	if e.dir {
		//: fs.ModeDir.
		return fs.ModeDir
	}
	//: a regular file.
	return 0
}

// Info describes the entry as Stat would.
func (e dirEntry) Info() (info fs.FileInfo, err error) {
	//: one description, wherever it is asked for.
	return e.fsys.Stat(e.path)
}

// childPath joins a directory and an entry's name.
func childPath(dir, name string) string {
	//: the root's children carry no prefix.
	if dir == rootName {
		//: the name alone.
		return name
	}
	//: dir/name.
	return path.Join(dir, name)
}
