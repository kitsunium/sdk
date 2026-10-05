// Package logfile is the hardened open both file sinks share: the append-only
// sink in internal/service/observe/logger/sink/file and the rotating one in
// internal/service/observe/logger/writer/rotfile, which re-runs it on every reopen after a
// rotation. A path whose final component is a symbolic link is refused twice —
// by an os.Lstat BEFORE the open (policy) and by O_NOFOLLOW AT the open where
// the kernel has it (the TOCTOU window the check leaves) — and both refusals
// name the indirection the same way, so an operator reading one line can tell
// a planted link from a full disk (CWE-59).
//
// Both sinks carried a copy of all of it; the copies differed in nothing but
// their error code and their wording (OPEN_FAILED 0.3.14.* for the sink,
// ROT_FILE_OPEN_FAILED for rotfile). Those are what this package does not own:
// a sink hands it a RefusalSpec carrying its two wraps, and every refusal leaves
// under that sink's code, in its words. It declares no code.
//
// Package logfile — the fallback openFlags for the platforms whose syscall
// package has no O_NOFOLLOW at all: windows, plan9, js/wasm — and wasip1, which
// has the constant but not a kernel behind it (see open_flags_unix.go for why
// it is here rather than there).
//
// On these platforms the symlink refusal is RefuseSymlink's os.Lstat and
// NOTHING ELSE, so the window between that check and the os.OpenFile that
// follows it is open. That is a real difference in protection and it is
// written down in the CLAUDE.md platform tables (this package's and each
// sink's) rather than smoothed over: a table claiming uniform protection would
// be worse than no table.
//
// Closing it here needs a different primitive, not a different flag — Windows
// has FILE_FLAG_OPEN_REPARSE_POINT, which OPENS the link and requires the
// handle to be rejected afterwards, the opposite shape from a failing open
// (ADR 0082 §D2 measured both for internal/service/app/lock). That is a separate
// piece of work and is named as one; this file does not pretend to be it.
//
// Package logfile — the Unix half of openFlags: the kernel is asked not to
// traverse an indirection at the log path, so a symbolic link planted in the
// window between RefuseSymlink's os.Lstat and the os.OpenFile that follows it
// fails the open instead of silently redirecting the sink (CWE-59) — at
// construction of either file sink, and on every reopen after a rotation.
//
// # Why the tag is `unix` and not a list of six GOOS
//
// The capability class is "the pinned toolchain's syscall package defines
// O_NOFOLLOW", and `unix` is the class the toolchain itself declares. Measured
// on go1.27.1 — the toolchain this repo resolves — by compiling
// `const _ = syscall.O_NOFOLLOW` as a LIBRARY package (never linked, so cgo and
// PIE link rules cannot mask the only question being asked) for all 47
// GOOS/GOARCH pairs of `go tool dist list`. All 39 pairs the `unix` tag selects
// compile it:
//
//	aix/ppc64, android/{386,amd64,arm,arm64}, darwin/{amd64,arm64},
//	dragonfly/amd64, freebsd/{386,amd64,arm,arm64}, illumos/amd64,
//	ios/{amd64,arm64}, linux/{386,amd64,arm,arm64,loong64,mips,mips64,
//	mips64le,mipsle,ppc64,ppc64le,riscv64,s390x}, netbsd/{386,amd64,arm,arm64},
//	openbsd/{386,amd64,arm,arm64,ppc64,riscv64}, solaris/amd64
//
// The other 8 pairs are js/wasm, plan9/{386,amd64,arm},
// windows/{386,amd64,arm64} and wasip1/wasm. Seven of them genuinely lack the
// constant; wasip1 has it and is excluded anyway, for the reason at the end of
// this comment. All eight take open_flags_other.go.
//
// A list of six GOOS would have been a second place to maintain the same fact,
// and its failure mode is the wrong way round: a Unix port Go adds later would
// silently land in the weaker fallback. With `unix`, a Unix port WITHOUT
// O_NOFOLLOW breaks the build instead — loud, which is what a security control
// should be when its premise stops holding. That is ADR 0018 §(c)'s rule
// applied (a missing kernel ABI constant is split by build tag, never guarded
// at runtime) and ADR 0018 §(a)'s `_unix.go` suffix, "the shared Unix mechanic".
//
// wasip1 is deliberately NOT here although it defines O_NOFOLLOW = 0400: there
// it is not a kernel flag but a lookupflag, `path_open` called without
// LOOKUP_SYMLINK_FOLLOW (go1.27.1 src/syscall/fs_wasip1.go:574), so the refusal
// belongs to whichever WASI host runs the module. No lane in this repository
// runs one, so claiming the protection there would be a claim nothing here can
// check. It takes the fallback and the CLAUDE.md table says so by name.
package logfile
