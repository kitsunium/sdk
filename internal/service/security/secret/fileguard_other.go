//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly)

// Package secret — the file store's honest refusal where its mechanics do not
// exist (ADR 0018 §(a): a uniform typed sentinel, never a silent downgrade and
// never a build break).
//
// The store rests on vfs.NewOS, which refuses the same platforms for reasons
// its own osguard_other.go records: on Windows a directory cannot be flushed,
// so a published name and the bytes it names can disagree after a crash, and
// a permission mode is not an ACL, so 0600 would exclude nobody. A secret
// store that reported success while doing neither is the one this domain
// exists not to ship. The memory and environment stores work everywhere.
package secret

// platformNative reports that this GOOS lacks a mechanic the file store needs,
// so NewFile refuses with core/proc.UnsupportedPlatform before creating the
// directory it could then never use.
const platformNative bool = false
