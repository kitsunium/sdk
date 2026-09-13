//go:build windows

// Package lock — the lock directory's safety verdict on Windows, where the
// question the POSIX rule asks has no answer and the attack it prevents has no
// mechanism (ADR 0081).
package lock

import (
	"io/fs"
)

// checkDir accepts every directory on Windows. The acceptance is a decision
// with two measurements behind it, not a stub.
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
// # What is NOT checked, stated rather than implied
//
// A DACL check — is there an ACE granting write or delete to Everyone
// (S-1-1-0) or Authenticated Users (S-1-5-11)? — is the answer that would
// match the Unix rule's intent rather than its mechanism, and it is not done
// here. ADR 0081 §Alternatives carries the argument and the price. Two
// exposures survive this decision and are named there: an attacker who can
// write the directory can plant a lock file BEFORE any holder opens one and
// keep it held, which is a denial of service rather than a lost exclusion; and
// on a host where SeCreateSymbolicLinkPrivilege is available to non-admins,
// a reparse point planted at a lock file's path redirects it.
func checkDir(_ string, _ fs.FileInfo) error {
	//: accepted, and the doc comment above is the verdict rather than the
	//: absence of one.
	return nil
}
