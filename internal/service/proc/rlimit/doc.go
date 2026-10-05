// Package rlimit — applies per-process setrlimit(2) resource ceilings.
//
// This file holds the platform-neutral surface: the exported Apply and
// PrepareSysProcAttr entry points delegate to the build-tagged applyLimits /
// prepareLimits implementations (rlimit_linux.go on Linux, rlimit_other.go
// everywhere else). Core declares the Resource enum and LimitValue pair; this
// package maps each Resource to the platform RLIMIT_* constant and issues the
// syscall, wrapping failures in the central proc sentinels.
//
// Package rlimit — Linux setrlimit/prlimit64 implementation.
//
// Package rlimit — non-Unix stub returning UnsupportedPlatform. setrlimit(2) is
// a Unix mechanic (Linux via rlimit_linux.go, Darwin and the BSDs via
// rlimit_unix.go); the remaining targets (Windows, plan9, js/wasm) have no
// equivalent, so every entry point degrades to the typed sentinel.
//
// Package rlimit — RLIMIT_AS mapping for the non-Linux Unix targets whose stdlib
// syscall exports it (Darwin, FreeBSD, NetBSD, DragonFly). OpenBSD has no
// address-space rlimit — RLIMIT_AS is absent from its kernel ABI — so it supplies
// the no-op in rlimit_table_openbsd.go instead, leaving ResourceAS unmapped
// (UnknownResource) rather than failing to compile on the missing constant.
//
// Package rlimit — OpenBSD adds no resources beyond the common Unix set:
// RLIMIT_AS is absent from its kernel ABI, so ResourceAS stays unmapped and a
// request for it surfaces the typed UnknownResource — the honest "this platform
// cannot set that limit" answer — rather than a build failure on the missing
// constant.
//
// Package rlimit — non-Linux Unix setrlimit(2) implementation. Darwin and the
// BSDs expose setrlimit(2) for the calling process exactly as Linux does, so a
// Spec's resource ceilings are honoured natively (applied post-fork on self by
// the exec trampoline). The Linux-only prlimit64(2) path for a FOREIGN pid has
// no portable equivalent here, so an Apply targeting another process degrades to
// the typed UnsupportedPlatform sentinel rather than silently limiting the
// caller. The Resource→RLIMIT_* table maps only the constants stdlib syscall
// exports on every target; RLIMIT_NPROC/RLIMIT_MEMLOCK live in golang.org/x/sys
// (banned), and RLIMIT_AS is absent on OpenBSD — both surface UnknownResource
// via the build-tagged table rather than a wrong limit or a build break.
package rlimit
