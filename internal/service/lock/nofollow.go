// Package lock — the lock path must name a FILE, never an indirection to one.
//
// # What this closes
//
// [fileLocker.pathFor] derives the lock file's name from the SHA-256 of the
// lock name, so an attacker cannot choose it — but it is derived, which means
// it is PREDICTABLE, and predictable is all the attacker needs. Create the
// directory first, compute the digest of a lock name the victim will use, and
// plant a symbolic link there. The victim's open follows it, the flock and the
// fencing ledger land on a file of the attacker's choosing, and every layer
// above reports success.
//
// Two locks then become one, or one becomes two, with no error anywhere:
//
//   - The victim's lock lands on the attacker's target while the attacker's own
//     lock lands on the real path. Both processes are inside the section, both
//     hold a lease, neither is blocked, and nothing logs.
//   - The redirected ledger is the attacker's file, so the fencing token the
//     victim hands to the protected resource is a number the attacker CHOSE.
//     Measured: a symlink to a file containing "48213\n" — a pidfile is exactly
//     that shape — made Acquire return fence 48214 and rewrite the pidfile with
//     it.
//
// [checkDir] exists to prevent precisely this substitution, and it does not
// reach it: its rule is about UNLINKING an entry that already exists, while
// this attack creates one at a name nobody has taken yet. The sticky bit is no
// help for the same reason — it stops you removing someone else's entry, and
// the attacker owns the symlink they created. `0777|sticky` is a mode the rule
// explicitly ACCEPTS, and it is what /tmp is.
//
// # It is one gap with two spellings, so it closes on both kernels
//
// The refusal is the same sentinel everywhere, [LockPathRedirected], but the
// mechanism is not, because the two kernels refuse in opposite places:
//
//   - Unix asks the kernel not to traverse, and the OPEN fails —
//     see nofollow_unix.go.
//   - Windows asks the kernel to open the LINK ITSELF, the open SUCCEEDS, and
//     the handle is then rejected — see nofollow_windows.go.
//
// Closing it on one platform only was the previous answer and it was the wrong
// one: the same deployment would succeed on Linux and fail on Windows for a
// reason the error could not explain (ADR 0081 §Deferred, now closed).
package lock

import (
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// pathRedirected builds the [LockPathRedirected] refusal.
//
// kind says which indirection was found — "symlink" on Unix, "reparse_point"
// on Windows, the latter covering a symbolic link and a junction alike since
// both are reparse points and both redirect the open. Each spelling is a
// constant in the platform file that raises it, because a constant belongs
// where it is used and neither platform compiles the other's.
//
// observed carries what the kernel actually reported — the errno on Unix, the
// attribute word on Windows. The errno is a FIELD rather than the decision,
// because it is not the same errno on the six Unix kernels this backend runs
// on: measured ELOOP on linux/amd64, and documented EMLINK on FreeBSD and
// DragonFly, EFTYPE on NetBSD, ELOOP on OpenBSD and Darwin. A three-value
// table across six kernels is exactly the shape of thing that is wrong on the
// seventh, so nothing here branches on it.
func pathRedirected(path, kind, observed string) error {
	//: the path is the SDK's own derived filename, never the caller's lock
	//: name, so naming it in full leaks nothing a directory listing does not.
	return kerrs.Wrap(LockPathRedirected, kerrs.WrapParams{},
		kerrs.String("path", path),
		kerrs.String("kind", kind),
		kerrs.String("observed", observed))
}
