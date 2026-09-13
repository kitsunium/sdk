//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

// Package lock — the Unix half of "do not follow an indirection at the lock
// path": O_NOFOLLOW, and a refusal that does not depend on which errno the
// kernel chose to spell it with.
package lock

import (
	"os"
	"syscall"
)

// openLockFile opens the lock file, refusing to traverse a symbolic link
// planted at the final component of path.
//
// O_NOFOLLOW is POSIX and present on every GOOS this file is built for. With
// O_CREAT it does NOT create the link's target: a dangling link is refused
// exactly as a live one is, which was measured alongside the live case because
// the dangling one is the shape the attack actually takes — the attacker plants
// a link to a path that does not exist yet and lets the victim create it.
//
// What it does NOT cover is the rest of the path. O_NOFOLLOW governs the FINAL
// component only; a symlink at a PARENT component is still traversed, measured
// on linux/amd64 in the same probe. That is a different exposure and a much
// weaker one — the parent components are [FileConfig.Dir], which the caller
// chose, while the final component is a name this package derives and an
// attacker can predict. It is named in ADR 0081 rather than silently implied
// to be covered.
//
// The errno is not consulted. See [pathRedirected] for the three different
// errnos the six kernels here produce for the same condition; os.Lstat answers
// the question those errnos were only evidence for, identically everywhere.
func openLockFile(path string) (file *os.File, err error) {
	opened, openErr := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, lockFileMode)
	//: the kernel refused to traverse, or the medium failed.
	if openErr != nil {
		//: LOCK_PATH_REDIRECTED or LOCK_BACKEND_FAILED.
		return nil, classifyOpenFailure(path, openErr)
	}
	//: the caller owns the returned descriptor — it IS the lock, and closing
	//: it here would release what this call was made to take.
	return opened, nil
}

// classifyOpenFailure decides whether a failed open met an indirection or the
// medium.
//
// os.Lstat does not traverse the final component, so it answers for the link
// the open would not follow. It is DIAGNOSIS, never the security decision: the
// refusal was already taken by the kernel, so a planter who removes the link
// between the two calls changes which sentinel is reported and cannot change
// whether the open was refused. That is the difference from an Lstat-then-open
// check, which is a genuine TOCTOU — and it is why the order here is the
// reverse of internal/service/writer/rotfile's, which checks first.
func classifyOpenFailure(path string, openErr error) error {
	info, statErr := os.Lstat(path)
	//: a deliberate act, and the errno it arrived as travels as a field
	//: because it is not the same errno on the six kernels this file serves.
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		//: LOCK_PATH_REDIRECTED, naming the path and what the kernel said.
		return pathRedirected(path, kindSymlink, openErr.Error())
	}
	//: LOCK_BACKEND_FAILED.
	return backendFailed("open", path, openErr)
}
