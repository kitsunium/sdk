//go:build windows

// Package lock — the lock directory's safety verdict on Windows, where the
// question the POSIX rule asks has no answer and the attack it prevents has no
// mechanism (ADR 0081).
package lock

import (
	"io/fs"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// checkDir refuses a directory whose DACL lets any account create or replace
// an entry in it — the Unix rule's INTENT, expressed in the only vocabulary
// Windows has for it.
//
// # The POSIX rule cannot run here — it would refuse everything
//
// Windows has no permission bits. os.Stat SYNTHESISES a mode from the single
// FILE_ATTRIBUTE_READONLY flag — 0444 when set, 0666 when not, plus
// ModeDir|0111 for a directory — so every writable directory on the system
// reports 0777 with no sticky bit, and "other-write set AND sticky unset" is
// true for all of them. Running the rule would make NewFileLocker refuse every
// directory a caller could name, and refuse it as LOCK_DIRECTORY_UNSAFE, which
// reads as a deployment fault rather than a bug in this package. That is
// exactly the failure mode ADR 0018 §(a) exists to prevent.
// TestThePosixDirectoryRuleWouldRefuseEveryDirectory measures it on this
// kernel rather than inferring it from the stdlib source.
//
// # And the attack it prevents is refused by the open, not by the directory
//
// What the POSIX rule protects is not the counter, it is the INODE: an account
// that can unlink the lock file replaces it, the next process locks the NEW
// file while the holder still locks the old one, and both are told they hold
// the same lock. On Windows a holder's own open forbids that. os.OpenFile
// reaches CreateFileW with FILE_SHARE_READ|FILE_SHARE_WRITE and deliberately
// WITHOUT FILE_SHARE_DELETE (Go 1.27, src/syscall/syscall_windows.go, func
// Open), so while any holder has the lock file open neither a delete nor a
// rename can touch it, whatever the directory's ACL permits.
// TestTheLockFileCannotBeUnlinkedOrRenamedWhileItIsOpen pins both verbs, and
// it fails loudly if a future Go release adds the share flag.
//
// # So the DACL is read instead, and the reason it took three ADRs
//
// The check ADR 0081 §Alternatives described — is there an ACE granting write
// or delete to Everyone (S-1-1-0) or Authenticated Users (S-1-5-11)? — is the
// one that matches the Unix rule's INTENT rather than its mechanism, and it is
// now done. It was deferred there and again in ADR 0082 §Deferred on a cost
// estimate of roughly 250 lines of ABI; re-checking that estimate against the
// pinned toolchain rather than restating it is what changed the answer. See
// dacl_windows.go.
//
// It fails OPEN: any failure on the way to a verdict accepts. A wrong refusal
// costs a caller a locker that never builds on a directory that is perfectly
// safe; a wrong acceptance leaves the platform where it already was.
//
// # What is still NOT checked
//
// The audit list (SACL) is not read — it needs a privilege an ordinary account
// does not hold, and it describes what is LOGGED rather than what is allowed.
// BUILTIN\Users (S-1-5-32-545) is not read as "anybody"; see dacl_windows.go
// for why that is a measurement this change does not have, and ADR 0083
// §Deferred for what it would take.
//
// One exposure survives and is named rather than implied: an attacker who can
// write the directory can plant a lock file BEFORE any holder opens one and
// keep it held. That is a denial of service rather than a lost exclusion, and
// this rule now refuses the directory it would happen in.
func checkDir(dir string, _ fs.FileInfo) error {
	writable, observed := dirWritableByAnyone(dir)
	//: nobody meaning "anybody" can put an entry here.
	if !writable {
		//: nothing to refuse.
		return nil
	}
	//: LOCK_DIRECTORY_UNSAFE, naming the identifier and the rights rather than
	//: a mode that would have meant nothing on this platform.
	return kerrs.Wrap(LockDirectoryUnsafe, kerrs.WrapParams{},
		kerrs.String("path", dir),
		kerrs.String("mode", observed))
}

// plantable reports whether any account could create an entry in the directory
// a component was found in.
//
// The mode is ignored, because on Windows there is nothing in it: os.Stat
// synthesises the permission bits from FILE_ATTRIBUTE_READONLY, so every
// writable directory reports 0777. The answer comes from the directory's DACL
// instead — see dacl_windows.go, which also carries why the cost that deferred
// this twice is no longer what it was.
func plantable(_ fs.FileMode, containerPath string) (yes bool, observed string) {
	//: the same question checkDir asks of the lock directory, asked of the
	//: directory a component was found in.
	return dirWritableByAnyone(containerPath)
}
