//go:build windows

package stream

import (
	"io/fs"
	"path/filepath"
	"syscall"

	"github.com/kitsunium/sdk/internal/kernel/fs/winacl"
)

// subRights is what an identifier meaning anybody must not hold on a
// partition or the cursors' directory: creating a file (a planted segment or
// cursor), creating a directory or a junction, or taking an entry away.
const subRights uint32 = winacl.RightAddFile | winacl.RightAddSubdirectory | winacl.ReplaceRights

// rootWritableByAnyone reports whether an identifier meaning anybody can
// REPLACE an entry of the stream directory — what the sticky bit forbids on
// Unix, spelled as the rights that do it here.
func rootWritableByAnyone(dir string, _ fs.FileInfo) (why, observed string, unusable bool) {
	granted, observed := winacl.GrantsAnyone(filepath.Clean(dir), winacl.ReplaceRights, 0)
	//: the verdict, or an inspection that could not run.
	return dirVerdict(granted, observed)
}

// subWritableByAnyone reports whether an identifier meaning anybody can put
// an entry into a partition or the cursors' directory, take one away, or
// write the files created in it.
func subWritableByAnyone(dir string, _ fs.FileInfo) (why, observed string, unusable bool) {
	granted, observed := winacl.GrantsAnyone(filepath.Clean(dir), subRights, winacl.ContentRights)
	//: the verdict, or an inspection that could not run.
	return dirVerdict(granted, observed)
}

// dirVerdict turns the reader's answer into this package's refusal. It fails
// CLOSED on an inspection that could not run, as the queue's does and for its
// reason: a directory wrongly accepted is one a stranger may plant a record
// in.
func dirVerdict(granted bool, found string) (why, observed string, unusable bool) {
	//: an identifier meaning anybody holds a right the rule forbids.
	if granted {
		//: "world-writable" is the fact on either platform.
		return "world-writable", found, true
	}
	//: no verdict: "could not look" is not "looked, and nobody may write".
	if found != "" {
		//: refused, carrying what stopped the reader.
		return "unverifiable", found, true
	}
	//: read to the end, and nobody meaning anybody holds a forbidden right.
	return "", "", false
}

// reparsePoint reports whether a directory entry is a reparse point: a
// symbolic link, and also a junction, which os.Lstat reports as a plain
// directory.
func reparsePoint(info fs.FileInfo) bool {
	attrs, ok := info.Sys().(*syscall.Win32FileAttributeData)
	//: what os.Lstat returns on Windows; anything else is not one.
	return ok && attrs.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
