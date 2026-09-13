// Package lock — the lock file's IDENTITY, and the exposure that is detectable
// rather than preventable.
//
// # What was reproduced
//
// In a 0777|sticky directory — /tmp's mode, and a row [checkDir] accepts by
// name — the account that created the lock file OWNS that entry, so the sticky
// bit permits them to unlink it. Not when the lock is free: WHILE THE VICTIM
// HOLDS IT.
//
//	victime détient le verrou : fence=1 inode=69831
//	2e verrou AVANT l'échange : held=false err=<nil> (attendu false)
//	entrée désliée pendant que la victime la détient
//	2e verrou APRÈS l'échange : held=true err=<nil> inode=69832
//	SPLIT : deux détenteurs, fences 1 et 1, inodes 69831 et 69832
//	Extend de la victime : <nil>
//
// Two holders, two inodes, and the fence RESET rather than advanced — both
// report 1. The last line is the one that matters: the victim asked whether it
// still held the lock and was told yes.
//
// # It is detectable, NOT preventable, and the difference is the point
//
// No flag on the open prevents this. The entry is unlinked after the open, by
// an account the directory's permissions genuinely allow to unlink it, and the
// victim's descriptor keeps working because a descriptor outlives its name.
// Three remedies were considered and two of them prevent nothing:
//
//   - An OWNER check on the lock file was rejected in ADR 0082 §Deferred and
//     is still rejected, for the reason given there: it breaks the shared-group
//     arrangement [checkDir] deliberately accepts, where the entry belongs to
//     the OTHER account by design.
//   - O_EXCL on creation plus a recorded identity prevents nothing either, and
//     that was checked rather than assumed. O_EXCL says whether THIS process
//     created the file. It says nothing about who unlinks it afterwards, which
//     is the entire attack — the unlink happens minutes later, on a descriptor
//     that has already been opened and locked.
//   - Comparing the descriptor's identity against the path DETECTS it, which
//     is what ships. The victim finds out at its next [fileLease.Extend], and
//     a Keepalive turns that into a cancelled context for the work inside the
//     section.
//
// So the guarantee is stated as exactly what it is: the lock is not kept, the
// split is not prevented, and the holder is TOLD. A holder that learns it no
// longer holds the lock can stop; a holder that is told it still does cannot.
// The only prevention is a lock directory no other account can write, which is
// what NewFileLocker creates (0700) when the directory is absent.
//
// # Where the check runs, and why not everywhere
//
// At acquisition, between the flock and the fence, because the window between
// the open and the flock is real and closing it is two syscalls. And at
// Extend, because that is the only call a holder makes DURING the section.
//
// Not at Release: releasing is closing a descriptor this lease owns, it
// succeeds whatever the name now points at, and a Release that returned an
// error would break `defer lease.Release(ctx)` at the one moment a caller is
// unwinding.
package lock

import (
	"os"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// observedUnlinked is the `observed` field's value when the lock file's name
// has no entry at all — the attack's own shape, and the only one that can be
// distinguished from a replacement with certainty.
const observedUnlinked string = "unlinked"

// observedReplaced is the `observed` field's value when the name exists and
// resolves to a different file from the one this descriptor holds.
const observedReplaced string = "replaced"

// statter is the single question sameEntry asks of the descriptor that carries
// the lock: describe yourself.
//
// It is an interface rather than *os.File because nothing else about the file
// is needed, and a parameter that can do less is a parameter that can go wrong
// in fewer ways — this function must never write, seek or close the descriptor
// the lease is holding.
type statter interface {
	Stat() (os.FileInfo, error)
}

// sameEntry reports whether path still names the file behind file.
//
// It answers NO only when that can be positively established: the name is
// gone, or the name resolves to a different file. Every inconclusive
// answer — a stat this process is not allowed to take, a medium that did not
// respond — is reported as YES, because a lock that starts refusing renewals
// on a stat hiccup fails the caller harder than the attack it is watching for,
// and there is no third verdict to return.
//
// os.Lstat, never os.Stat: an indirection planted at the name AFTER the open
// is exactly the replacement being looked for, and following it would compare
// this descriptor against the planter's chosen target and find them equal.
//
// os.SameFile is the comparison because it is the portable spelling of the
// question. On Unix it is (dev, ino) off the two stat structures. On Windows
// it is the volume serial and the file index, which the standard library loads
// by opening the path with a desired access of ZERO — a request Windows does
// not subject to the sharing check, which is why this works on a file the
// caller holds open without FILE_SHARE_DELETE.
func sameEntry(file statter, path string) (same bool, observed string) {
	held, heldErr := file.Stat()
	//: the descriptor could not describe itself. Nothing can be concluded.
	if heldErr != nil {
		//: inconclusive reads as unchanged.
		return true, ""
	}
	named, namedErr := os.Lstat(path)
	//: the name is gone. The holder's descriptor is the only thing left
	//: pointing at that file, so the next acquisition will create a NEW one
	//: and lock that instead.
	if namedErr != nil && os.IsNotExist(namedErr) {
		//: positively established.
		return false, observedUnlinked
	}
	//: any other stat failure. Nothing can be concluded.
	if namedErr != nil {
		//: inconclusive reads as unchanged.
		return true, ""
	}
	//: the name exists but is not this file.
	if !os.SameFile(held, named) {
		//: positively established.
		return false, observedReplaced
	}
	//: the name still leads here.
	return true, ""
}

// fileReplaced builds the [LockFileReplaced] refusal.
//
// fence is the token the lease was still handing out, because that is the
// number the protected resource has been accepting and the one an operator has
// to grep their own logs for to find out how far the split reached. It is ZERO
// from Acquire, where no token had been issued yet — which is itself the
// difference between the two sites, so it is a value rather than an omission.
func fileReplaced(op, path, name string, fence uint64, observed string) error {
	//: the path is this package's derived filename, so naming it in full
	//: leaks nothing a directory listing does not.
	return kerrs.Wrap(LockFileReplaced, kerrs.WrapParams{},
		kerrs.String("op", op),
		kerrs.String("path", path),
		kerrs.String("lock", name),
		kerrs.Int64("fence", int64(fence)),
		kerrs.String("observed", observed))
}
