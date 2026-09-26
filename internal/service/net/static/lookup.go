// Package static — opening a name of the tree, and telling a name that names
// nothing from a tree that failed.
package static

import (
	"errors"
	"io/fs"
)

// lookup is what opening a name found.
type lookup uint8

const (
	// present: the name was opened and stat'ed.
	present lookup = iota
	// absent: the name names nothing — the file system refused the NAME.
	absent
	// broken: the tree holds something by that name and could not open or
	// stat it.
	broken
)

// open opens name in fsys and reads what it is. The caller closes the file
// when found is present; nothing is left open otherwise.
func open(fsys fs.FS, name string) (file fs.File, info fs.FileInfo, found lookup) {
	file, err := fsys.Open(name)
	//: the lookup failed: the name, or the tree?
	if err != nil {
		//: a refusal of the name is a 404, anything else a 500.
		if nameRefused(err) {
			return nil, nil, absent
		}
		return nil, nil, broken
	}
	info, err = file.Stat()
	//: an entry that opened and cannot say what it is: the tree's failure.
	if err != nil {
		closeQuietly(file)
		return nil, nil, broken
	}
	//: opened, and known.
	return file, info, present
}

// nameRefused reports whether a failed lookup was the file system refusing the
// NAME: nothing by that name, a name it cannot hold, a component that is a
// file, a component too long. A client chooses every name it asks for, so a
// client could otherwise turn any of these into a 5xx at will — a Windows
// reserved character, a component past NAME_MAX, "index.html/x". Everything
// else — a permission refused, a descriptor table full, a disk that failed, a
// remote tree that did not answer — is the tree's failure, and a 500.
func nameRefused(err error) bool {
	//: the portable refusals, then the platform's own.
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) || platformNameRefused(err)
}
