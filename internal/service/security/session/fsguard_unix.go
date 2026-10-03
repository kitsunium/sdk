//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

// Package session — the platform gate the file store reads, and the one
// question a mode can answer here and cannot on Windows.
//
// The file store rests on three guarantees, and only two of them are portable.
// Atomic publication is rename(2), which POSIX requires to be atomic and which
// Go's os.Rename also provides on Windows through MoveFileEx. Restrictive
// permissions and advisory locking are not: they are the reason this file has a
// build tag and a sibling that refuses. The lock itself is the kernel's
// (internal/kernel/fs/flock, polled by [fileStore.takeFlock]); what stays here
// is the gate that says both mechanics exist, and [plantable], which a mode
// can answer here and cannot on Windows.
package session

import "io/fs"

// platformNative reports that this GOOS has both mechanics natively. The file
// store's constructor reads it and refuses on the platforms where it is false,
// rather than building a store that would silently provide neither.
//
// It is narrower than internal/kernel/fs/flock.Native, which is also true on
// Windows: the lock exists there, the owner-only permissions do not.
// TestTheStoreIsBuiltOnlyWhereTheKernelCanLock pins that the store's set never
// grows past the lock's.
const platformNative bool = true

// worldWritable is the permission bit that lets any account create an entry in
// a directory.
const worldWritable fs.FileMode = 0o002

// plantable reports whether any account could have created an entry in a
// directory whose mode is container, and renders what it read.
//
// It is internal/service/app/lock's rule for the same question (ADR 0083), read
// off the same pathchain.StepValue.Container. Other-write is the one bit that
// answers it. Group-write is not enough: a directory shared with a group is a
// deliberate arrangement, and its members are accounts the operator chose. The
// sticky bit is deliberately NOT consulted: it says only an entry's owner may
// UNLINK it, and planting a component CREATES an entry at a name nobody has
// taken, so 0777|sticky — exactly what /tmp is — is plantable.
func plantable(container fs.FileMode) (yes bool, observed string) {
	//: nobody outside the owner and the group can create an entry here.
	if container&worldWritable == 0 {
		//: a verdict, and it is "safe"; observed stays empty.
		return false, ""
	}
	//: plantable, and the mode is what an operator has to change.
	return true, "container=" + container.Perm().String()
}
