//go:build windows

package queue

import (
	"io/fs"
	"path/filepath"
	"syscall"

	"github.com/kitsunium/sdk/internal/kernel/fs/winacl"
)

// stateRights is what an identifier meaning anybody must not hold on a state
// directory: creating a file (a planted message), creating a directory or a
// junction, or taking an entry away.
const stateRights uint32 = winacl.RightAddFile | winacl.RightAddSubdirectory | winacl.ReplaceRights

// rootWritableByAnyone reports whether an identifier meaning anybody can
// REPLACE an entry of the queue directory — what the sticky bit forbids on
// Unix, spelled as the rights that do it here.
func rootWritableByAnyone(dir string, _ fs.FileInfo) (why, observed string, unusable bool) {
	granted, observed := winacl.GrantsAnyone(filepath.Clean(dir), winacl.ReplaceRights, 0)
	//: the verdict, or an inspection that could not run.
	return dirVerdict(granted, observed)
}

// stateWritableByAnyone reports whether an identifier meaning anybody can put
// an entry into a state directory, take one away, or write the files created
// in it.
func stateWritableByAnyone(dir string, _ fs.FileInfo) (why, observed string, unusable bool) {
	granted, observed := winacl.GrantsAnyone(filepath.Clean(dir), stateRights, winacl.ContentRights)
	//: the verdict, or an inspection that could not run.
	return dirVerdict(granted, observed)
}

// dirVerdict turns the reader's answer into this package's refusal.
//
// It fails CLOSED on an inspection that could not run — the list unreadable,
// or read only in part — where lock, asking the same reader, fails open (ADR
// 0084 §D5). The asymmetry that decides D5 runs the other way here. A lock
// directory wrongly accepted costs the hardening, and a squatter can at worst
// hold the lock; a queue directory wrongly accepted is one a stranger may plant
// a message in, which a consumer then acts on. And before this rule the queue
// refused EVERY directory on Windows, so refusing one whose list nobody could
// read takes away nothing that worked. The Unix rule has no such case: reading
// a mode cannot fail.
func dirVerdict(granted bool, found string) (why, observed string, unusable bool) {
	//: an identifier meaning anybody holds a right the rule forbids.
	if granted {
		//: "world-writable" is the fact on either platform; observed says
		//: which identifier and which rights, since there is no mode to read.
		return "world-writable", found, true
	}
	//: no verdict: the reader names what stopped it, and "could not look"
	//: is not "looked, and nobody may write".
	if found != "" {
		//: refused, carrying the Win32 status or the entry the walk stopped at.
		return "unverifiable", found, true
	}
	//: read to the end, and nobody meaning anybody holds a forbidden right.
	return "", "", false
}

// reparsePoint reports whether a directory entry is a reparse point: a
// symbolic link, and also a junction, which os.Lstat reports as a plain
// directory — and which keeps the messages wherever its author pointed it, as
// a link does.
func reparsePoint(info fs.FileInfo) bool {
	attrs, ok := info.Sys().(*syscall.Win32FileAttributeData)
	//: what os.Lstat returns on Windows; anything else is not a reparse point.
	return ok && attrs.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
