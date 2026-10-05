package lock

import (
	corelock "github.com/kitsunium/sdk/internal/core/app/lock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// pathRedirected builds the [corelock.LockPathRedirected] refusal.
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
	return kerrs.Wrap(corelock.LockPathRedirected, kerrs.WrapParams{},
		kerrs.String("path", path),
		kerrs.String("kind", kind),
		kerrs.String("observed", observed))
}
