//go:build !windows

package stream

import (
	"io/fs"
	"os"
)

// worldWritable is the other-write bit.
const worldWritable fs.FileMode = 0o002

// rootWritableByAnyone reports whether an account outside the owner and group
// can REPLACE an entry of the stream directory: other-write without the
// sticky bit. A sticky world-writable directory — what /tmp is — is
// accepted, since nothing lives at the root but the lock file and the
// subdirectories, which only their owner may then unlink.
func rootWritableByAnyone(_ string, info fs.FileInfo) (why, observed string, unusable bool) {
	mode := info.Mode()
	//: no other-write, or other-write under the sticky bit.
	if mode&worldWritable == 0 || mode&os.ModeSticky != 0 {
		//: acceptable.
		return "", "", false
	}
	//: any account could unlink a partition, or replace one.
	return "world-writable", mode.String(), true
}

// subWritableByAnyone reports whether an account outside the owner and group
// can put an entry into a partition or the cursors' directory at all. There
// an entry IS a segment or a cursor, so the sticky bit exempts nothing: it
// stops an unlink, not a planted one.
func subWritableByAnyone(_ string, info fs.FileInfo) (why, observed string, unusable bool) {
	mode := info.Mode()
	//: owner and group only.
	if mode&worldWritable == 0 {
		//: acceptable.
		return "", "", false
	}
	//: what the root accepts and a subdirectory must not.
	if mode&os.ModeSticky != 0 {
		//: any account can plant a segment or a cursor here.
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
