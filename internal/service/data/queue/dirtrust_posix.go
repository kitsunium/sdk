//go:build !windows

// Package queue — who else can write a queue directory, asked of the mode bits:
// the whole answer on Unix, where a directory's other-write bit and its sticky
// bit are exactly the two facts the question needs.
package queue

import (
	"io/fs"
	"os"
)

// worldWritable is the other-write bit.
const worldWritable fs.FileMode = 0o002

// rootWritableByAnyone reports whether an account outside the owner and group
// can REPLACE an entry of the queue directory.
//
// Two ways to be safe, and they carry the same verdict: either no such account
// can write the directory at all, or the sticky bit says only an entry's owner
// may unlink it — which is exactly what /tmp is, and all the root needs, since
// nothing lives there but the three states.
func rootWritableByAnyone(_ string, info fs.FileInfo) (why, observed string, unusable bool) {
	mode := info.Mode()
	//: no other-write, or other-write under the sticky bit.
	if mode&worldWritable == 0 || mode&os.ModeSticky != 0 {
		//: acceptable.
		return "", "", false
	}
	//: world-writable without the sticky bit: any account could unlink a
	//: state, which is a silent drain, or replace one, a silent injection.
	return "world-writable", mode.String(), true
}

// stateWritableByAnyone reports whether an account outside the owner and group
// can put an entry into a state directory at all. In a state an entry IS a
// message, so the sticky bit exempts nothing here: it stops an unlink, not a
// planted message.
func stateWritableByAnyone(_ string, info fs.FileInfo) (why, observed string, unusable bool) {
	mode := info.Mode()
	//: owner and group only.
	if mode&worldWritable == 0 {
		//: acceptable.
		return "", "", false
	}
	//: what the root accepts and a state must not: world-writable, sticky.
	if mode&os.ModeSticky != 0 {
		//: any account can plant a message here.
		return "sticky-world-writable", mode.String(), true
	}
	//: world-writable without the sticky bit.
	return "world-writable", mode.String(), true
}

// reparsePoint reports whether a directory entry is a reparse point. Only
// Windows has them; a Unix indirection is a symbolic link, which the mode
// already says.
func reparsePoint(_ fs.FileInfo) bool {
	//: nothing beyond ModeSymlink to look for here.
	return false
}
