// Package winacl — the reader: a directory's discretionary list, fetched with
// GetNamedSecurityInfoW and walked entry by entry with GetAce.
//
// # The cost estimate that deferred it three times, re-checked
//
// ADR 0081 §Alternatives priced a DACL check at roughly 250 lines of ABI and
// deferred it; ADR 0082 §Deferred carried that forward unchanged. Reading
// go1.27's syscall package rather than assuming shrinks it to two entry points,
// because the standard library already exports everything else this needs:
//
//   - syscall.StringToSid builds a SID from its string form, so the well-known
//     identifiers cost nothing (security_windows.go, ConvertStringSidToSidW);
//   - (*syscall.SID).String renders one back, so the comparison is a string
//     comparison and EqualSid is not needed at all;
//   - syscall.LocalFree releases the security descriptor.
//
// What is left is GetNamedSecurityInfoW and GetAce, bound from advapi32.dll
// with syscall.NewLazyDLL — the same discipline ADR 0081 used to bind
// LockFileEx from kernel32 (internal/kernel/fs/flock), and for the same
// reason: golang.org/x/sys is banned SDK-wide (ADR 0018), and binding two
// stable exports adds no module, no go.sum entry and no MODULE.bazel change.
//
// # Every way to the end of the list is named
//
// The walk reaches a verdict only by reading the whole list. Every failure on
// the way — a path that cannot be converted, an object whose security
// information cannot be read, an ACE that cannot be fetched — answers "no
// grant found" AND names what stopped it, so a caller can tell it from the
// verdict. Neither answer is a refusal: the reader decides nothing (see the
// package comment), and the SDK's two callers resolve "could not look" in
// opposite directions, each for a reason of its own.
//
// Package winacl — the hypothetical accounts a DACL is evaluated against, and
// why one map keyed by SID is the wrong shape.
//
// Package winacl reads a Windows directory's discretionary access control list
// and answers one question of it: does an identifier meaning ANYBODY —
// Everyone, Authenticated Users, BUILTIN\Users — hold a right, on the directory
// itself or on the files that will be created in it?
//
// # Why a list and not a mode
//
// On Windows os.Stat has no permission bits to read. It SYNTHESISES a mode
// from FILE_ATTRIBUTE_READONLY, so every writable directory reports 0777 with
// no sticky bit and every read-only one 0555, and a rule written against the
// mode — "other-write and not sticky", "other-write at all" — refuses every
// directory a caller could name. The question such a rule asks is perfectly
// answerable on this platform; it is just not a mode. It is the directory's
// DACL, and this package is the one reader of it the SDK has: a second reader
// would be a second place to get eight ACE shapes, the deny subtraction and the
// NULL DACL wrong (ADR 0084, ADR 0086, ADR 0095).
//
// # Two masks, because one list answers two questions with different bits
//
// GrantsAnyone takes onDirectory, asked of the entries that apply to the
// directory itself, and onFilesWithin, asked of the entries the FILES created
// in it will inherit. The second has no Unix counterpart: a file created 0600
// on Unix is 0600 whatever its directory allows, while on Windows the mode
// passed to os.OpenFile means nothing and a new file takes the directory's
// inheritable entries instead. [ReplaceRights], [CreateRights] and
// [ContentRights] name the three questions the SDK's directory rules ask, and
// the single rights they are built from are exported beside them; WHICH mask a
// caller asks, of which directory, is the caller's rule.
//
// # It measures and never decides
//
// GrantsAnyone reports a grant FOUND, or no grant found, and names in observed
// what it found or what stopped it — and observed is empty only when the list
// was read to the end and grants nothing asked about. "Could not look" is
// therefore never "looked, and nobody may write", and what a caller does with
// the first is the caller's decision: the SDK's two callers make opposite ones
// on purpose (ADR 0084 §D5, ADR 0095). That is the rule of this kernel family:
// measure here, refuse in the domain.
//
// # Bound with no dependency
//
// The reader is two advapi32 exports, GetNamedSecurityInfoW and GetAce, bound
// with syscall.NewLazyDLL — golang.org/x/sys is banned SDK-wide (ADR 0018) —
// and the standard library supplies the rest: syscall.StringToSid,
// (*syscall.SID).String and syscall.LocalFree. A SID is compared by byte
// equality on its rendered string, so EqualSid is never bound. The reader is
// in dacl_windows.go and tokens_windows.go; this file, which every platform
// compiles, holds only the vocabulary, so a rule can name its masks in a file
// of its own without a build tag.
package winacl
