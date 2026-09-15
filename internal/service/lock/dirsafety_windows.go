//go:build windows

// Package lock — the lock directory's safety verdict on Windows, where the
// question the POSIX rule asks has no answer and the attack it prevents has no
// mechanism (ADR 0081).
package lock

import (
	"io/fs"
	"log"

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
// Failing open silently would be a different thing, and is not what happens.
// An acceptance that rests on a verdict and an acceptance that rests on a
// failed inspection are indistinguishable from the return value — there is
// only one nil — so the second one LOGS. That is the same channel
// internal/service/entitlement uses for the same shape of degradation ("cannot
// guard the roster cache … concurrent refreshes on this machine are not
// serialised"), and it fires only when the platform API refused to answer.
//
// # The two halves, and why the second one has no Unix counterpart
//
// The rights this refuses are the ones that let a stranger take away the entry
// a holder created — FILE_DELETE_CHILD, WRITE_DAC, WRITE_OWNER. FILE_ADD_FILE
// is deliberately not among them: creating an entry at a free name is exactly
// what the sticky bit permits, and ADR 0052's table has accepted 0777|sticky
// since it was written.
//
// It also reads what the FILES created here will inherit, which the Unix rule
// never has to. A lock file there is created 0600 whatever the directory's
// mode says; here it takes the directory's inheritable entries instead, so a
// directory nobody can unlink from can still hand every account the fencing
// ledger. See contentRights in dacl_windows.go.
//
// # What is still NOT checked
//
// The audit list (SACL) is not read, and cannot help: no ACE type a SACL may
// carry GRANTS anything — audit and alarm entries describe logging, and the
// mandatory label, the scoped policy identifier and the access filter only
// RESTRICT. Reading it could move this verdict towards accepting and never
// towards refusing (ADR 0086 §D5).
//
// One exposure survives and is named rather than implied: an attacker who can
// create an entry in the directory can plant a lock file BEFORE any holder
// opens one and keep it held. That is a denial of service rather than a lost
// exclusion, it is what the accepting half of this rule buys, and it is the
// same exposure /tmp has carried on Unix since ADR 0052.
func checkDir(dir string, _ fs.FileInfo) error {
	writable, observed := dirGrantsAnyone(dir, replaceRights, contentRights)
	//: nobody meaning "anybody" can put an entry here — or the question could
	//: not be asked, which accepts and SAYS SO rather than passing silently.
	if !writable {
		//: accepted, with or without a verdict behind it.
		return acceptedDir(dir, observed)
	}
	//: LOCK_DIRECTORY_UNSAFE, naming the identifier and the rights rather than
	//: a mode that would have meant nothing on this platform.
	return kerrs.Wrap(LockDirectoryUnsafe, kerrs.WrapParams{},
		kerrs.String("path", dir),
		kerrs.String("mode", observed))
}

// plantable reports whether any account could create a component in the
// directory an indirection was found in.
//
// The mode is ignored, because on Windows there is nothing in it: os.Stat
// synthesises the permission bits from FILE_ATTRIBUTE_READONLY, so every
// writable directory reports 0777. The answer comes from the directory's DACL
// instead — see dacl_windows.go, which also carries why the cost that deferred
// this twice is no longer what it was.
//
// It is a DIFFERENT question from [checkDir]'s and asks for different rights,
// exactly as the POSIX pair differ over the sticky bit: a component is a
// directory, so what matters is FILE_ADD_SUBDIRECTORY, and unlinking an entry
// that already exists is beside the point. %ProgramData% is where the two
// answers part company on a real deployment — a lock directory under it is
// accepted, a junction planted in it is not.
func plantable(_ fs.FileMode, containerPath string) (yes bool, observed string) {
	//: only the rights that put a directory at a free name; nothing is
	//: inherited by a component, so the second mask is empty.
	return dirGrantsAnyone(containerPath, createRights, 0)
}

// acceptedDir accepts a directory, and says so out loud when the acceptance
// rests on an inspection that could not run rather than on a verdict.
//
// [dirGrantsAnyone] answers "not writable by anybody" for both, because
// there is no third verdict to return and refusing on a Win32 failure would
// cost a caller a locker on a directory that is perfectly safe. The two are
// still different facts, and an operator debugging why a lock directory was
// accepted needs to be able to tell them apart. Nothing here fires on the
// ordinary path: observed is empty whenever the DACL was actually read.
func acceptedDir(dir, observed string) error {
	//: a verdict was reached and it was "safe".
	if observed == "" {
		//: accepted.
		return nil
	}
	//: the DACL could not be read at all. Accepting is the decision; being
	//: quiet about it is not.
	log.Printf("cannot read the lock directory's access control list at %s (%s); it is accepted unchecked, so a directory any account can write would not be refused", dir, observed)
	//: accepted, and recorded.
	return nil
}
