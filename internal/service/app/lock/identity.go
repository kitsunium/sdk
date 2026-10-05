package lock

import (
	"os"

	corelock "github.com/kitsunium/sdk/internal/core/app/lock"
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

// fileReplaced builds the [corelock.LockFileReplaced] refusal.
//
// fence is the token the lease was still handing out, because that is the
// number the protected resource has been accepting and the one an operator has
// to grep their own logs for to find out how far the split reached. It is ZERO
// from Acquire, where no token had been issued yet — which is itself the
// difference between the two sites, so it is a value rather than an omission.
func fileReplaced(op, path, name string, fence uint64, observed string) error {
	//: the path is this package's derived filename, so naming it in full
	//: leaks nothing a directory listing does not.
	return kerrs.Wrap(corelock.LockFileReplaced, kerrs.WrapParams{},
		kerrs.String("op", op),
		kerrs.String("path", path),
		kerrs.String("lock", name),
		kerrs.Int64("fence", int64(fence)),
		kerrs.String("observed", observed))
}
