//go:build unix

// Package rotfile — the Unix half of openFlags: the kernel is asked not to
// traverse an indirection at the log path, so a symbolic link planted in the
// window between refuseSymlink's os.Lstat and the OpenFile that follows it
// fails the open instead of silently redirecting the sink (CWE-59). This
// applies to every reopen after a rotation, not only the first open.
//
// # Why the tag is `unix` and not a list of six GOOS
//
// The capability class is "the pinned toolchain's syscall package defines
// O_NOFOLLOW", and `unix` is the class the toolchain itself declares. Measured
// on go1.27.0 — the toolchain this repo resolves — by compiling `const _ =
// syscall.O_NOFOLLOW` as a LIBRARY package (never linked, so cgo and PIE link
// rules cannot mask the only question being asked) for all 47 GOOS/GOARCH
// pairs in `go tool dist list`. Every pair the `unix` tag selects has it:
//
//	aix/ppc64, android/{386,amd64,arm,arm64}, darwin/{amd64,arm64},
//	dragonfly/amd64, freebsd/{386,amd64,arm,arm64}, illumos/amd64,
//	ios/{amd64,arm64}, linux/{386,amd64,arm,arm64,loong64,mips,mips64,
//	mips64le,mipsle,ppc64,ppc64le,riscv64,s390x}, netbsd/{386,amd64,arm,arm64},
//	openbsd/{386,amd64,arm,arm64,ppc64,riscv64}, solaris/amd64
//
// The four that do not — js/wasm, plan9/{386,amd64,arm}, windows/{386,amd64,
// arm64} — are exactly the complement, and they take open_flags_other.go.
// Source read rather than assumed: $(go env GOROOT)/src/syscall/zerrors_*.go,
// plus syscall_wasip1.go for the one platform outside both sets (see below).
//
// A list of six GOOS would have been a second place to maintain the same fact,
// and its failure mode is the wrong way round: a Unix port Go adds later would
// silently land in the weaker fallback. With `unix`, a Unix port WITHOUT
// O_NOFOLLOW breaks the build instead — loud, which is what a security control
// should be when its premise stops holding. That is ADR 0018 §(c)'s rule
// applied (missing kernel ABI constant → split by build tag, never a runtime
// guard) and ADR 0018 §(a)'s `_unix.go` suffix, "the shared Unix mechanic".
//
// wasip1 is deliberately NOT here although it defines O_NOFOLLOW = 0400: it is
// not a kernel flag there but a lookupflag, `path_open` called without
// LOOKUP_SYMLINK_FOLLOW (go1.27.0 src/syscall/fs_wasip1.go:574), so the
// refusal belongs to whichever WASI host happens to run the module. No lane in
// this repository runs one, so claiming the protection there would be a claim
// nothing here can check. It takes the fallback and the CLAUDE.md table says
// so by name.
package rotfile

import (
	"os"
	"syscall"
)

// openFlags is the OpenFile flag set used for the active file at construction
// and on every reopen. O_NOFOLLOW closes the TOCTOU window between
// refuseSymlink's os.Lstat and the open: with O_CREATE it refuses a DANGLING
// link exactly as it refuses a live one, which matters because the dangling
// one is the shape the attack takes — plant the link, let the victim's
// O_CREATE make the file.
//
// It governs the FINAL component only. A symbolic link at a PARENT component
// is still traversed; that is the caller's own directory tree, and the
// package's "do NOT place the log file under an attacker-writable directory"
// pre-condition is what covers it.
const openFlags int = os.O_APPEND | os.O_CREATE | os.O_WRONLY | syscall.O_NOFOLLOW
