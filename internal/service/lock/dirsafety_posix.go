//go:build !windows

// Package lock — the lock directory's safety verdict where a directory's mode
// bits say who may replace its entries (ADR 0081).
package lock

import (
	"io/fs"
	"os"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// worldWritable is the permission bit that makes a directory's entries
// replaceable by any account.
const worldWritable fs.FileMode = 0o002

// checkDir refuses a directory whose entries any account can replace.
//
// Group-writable is ACCEPTED: a lock shared between two service accounts
// through a common group is a deliberate arrangement, and refusing it would
// push callers to a world-writable directory instead. World-writable WITH the
// sticky bit is accepted for the same reason — that is exactly what /tmp is,
// and the sticky bit is precisely the rule that only an entry's owner may
// unlink it. World-writable WITHOUT it is refused: unlinking the lock file and
// creating a new one gives the next process a different inode to lock, so two
// processes hold "the same" lock and neither can observe the other.
//
// The whole table is pinned by TestTheDirectoryRuleIsOtherWriteAndNotSticky,
// including the four ACCEPTING rows — which is the half a build-tag split
// loses without noticing, since a guard on 0777 alone still passes while the
// deliberate arrangements stop working.
func checkDir(dir string, info fs.FileInfo) error {
	mode := info.Mode()
	//: two ways to be safe, and they carry the same verdict: either no account
	//: outside the owner and group can replace an entry at all, or the sticky
	//: bit says only an entry's owner may unlink it — which is exactly /tmp.
	if mode&worldWritable == 0 || mode&os.ModeSticky != 0 {
		//: nothing to refuse.
		return nil
	}
	//: world-writable and not sticky: the lock file can be swapped underneath.
	return kerrs.Wrap(LockDirectoryUnsafe, kerrs.WrapParams{},
		kerrs.String("path", dir),
		kerrs.String("mode", mode.Perm().String()))
}

// plantable reports whether any account could create an entry in the directory
// a component was found in, and renders what it read.
//
// The path is unused here and is the whole of the answer on Windows, where
// there are no mode bits to read — see dirsafety_windows.go.
//
// The sticky bit is deliberately NOT consulted, and that is the whole
// difference from [checkDir]. Sticky says only an entry's owner may UNLINK it;
// it says nothing about creating one at a name nobody has taken. [checkDir]
// asks who can replace the lock file, which is an unlink, so sticky exempts a
// directory there. [checkChain] asks who could have planted a component of the
// path, which is a creation, so sticky exempts nothing here — and 0777|sticky,
// which is exactly what /tmp is, is plantable.
func plantable(container fs.FileMode, _ string) (yes bool, observed string) {
	//: other-write is the one bit that answers the question. Group-write is
	//: not enough: a directory shared with a group is a deliberate
	//: arrangement, the same one checkDir accepts, and the accounts in that
	//: group are the ones the lock is being shared with.
	if container&worldWritable == 0 {
		//: a verdict, and it is "safe". observed stays EMPTY, which is what
		//: tells the caller a verdict was reached at all — see chain.go's
		//: noteUninspected. Reading a mode cannot fail, so the inconclusive
		//: case this shape exists for is unreachable on this platform.
		return false, ""
	}
	//: plantable, and the mode is what an operator has to change.
	return true, "container=" + container.Perm().String()
}
