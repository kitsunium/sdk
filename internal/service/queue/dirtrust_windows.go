//go:build windows

// Package queue — who else can write a queue directory, asked of Windows in the
// only vocabulary it has for it: the directory's DACL, read by the one reader
// this repository has (internal/service/lock, ADR 0084/0086).
//
// # Why the mode rule cannot run here
//
// os.Stat SYNTHESISES a mode from FILE_ATTRIBUTE_READONLY on Windows, so every
// writable directory reports 0777 with no sticky bit, and the Unix rule refused
// every queue directory a caller could name — as QUEUE_DIRECTORY_UNUSABLE, an
// error that blames the deployment for the platform (ADR 0018 §(a)'s failure
// mode). The first Windows run of the whole suite found it: every /file case.
//
// # The same two questions, asked of the DACL
//
// The root is refused when an identifier meaning anybody may take away an
// entry it did not create — lock's ReplaceRights, the Windows spelling of
// "world-writable and not sticky". A state is refused when such an identifier
// may put an entry there at all — a file (a planted message) or a directory
// or junction — or take one away, or alter a file created there through what
// that file would inherit: on Unix a message is written 0600 whatever the
// directory allows, here it inherits the directory's list.
//
// # Where it does not reach
//
// The broker cannot yet RUN here: internal/service/vfs refuses Windows by
// design (no flushable directory handle, a mode that is not an ACL), so NewFile
// returns UNSUPPORTED_PLATFORM once these checks pass. They run anyway, so the
// refusal a caller meets on Windows is the platform's rather than a false
// verdict on their directory — and so the rules are right on the day vfs gains
// a Windows backend.
package queue

import (
	"io/fs"
	"path/filepath"
	"syscall"

	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// stateRights is what an identifier meaning anybody must not hold on a state
// directory: creating a file (a planted message), creating a directory or a
// junction, or taking an entry away.
const stateRights uint32 = svclock.RightAddFile | svclock.RightAddSubdirectory | svclock.ReplaceRights

// rootWritableByAnyone reports whether an identifier meaning anybody can
// REPLACE an entry of the queue directory — what the sticky bit forbids on
// Unix, spelled as the rights that do it here.
func rootWritableByAnyone(dir string, _ fs.FileInfo) (why, observed string, unusable bool) {
	granted, observed := svclock.GrantsAnyone(filepath.Clean(dir), svclock.ReplaceRights, 0)
	//: the verdict, or an inspection that could not run.
	return dirVerdict(granted, observed)
}

// stateWritableByAnyone reports whether an identifier meaning anybody can put
// an entry into a state directory, take one away, or write the files created
// in it.
func stateWritableByAnyone(dir string, _ fs.FileInfo) (why, observed string, unusable bool) {
	granted, observed := svclock.GrantsAnyone(filepath.Clean(dir), stateRights, svclock.ContentRights)
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
