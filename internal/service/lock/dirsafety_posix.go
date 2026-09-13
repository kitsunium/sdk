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
