// Package selfupdate — the sentinels this IMPLEMENTATION emits, as opposed to
// the ones the domain contract names.
//
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Every Public here is wire-safe, so none of them names a path, a URL, a host,
// a tag or an asset. That is not squeamishness: the public half is the one
// documented safe to put in a response body, and this package's inputs are a
// release host and a filesystem — every particular it could name came from one
// of the two. What it says instead is the one thing an operator has to take
// away, which is whether the binary they are running was replaced.
//
// Where it happened and what the filesystem said live in Private and Fields.
// They are not lost to the operator: diagnose renders them and
// ExplainUpgradeFailure prints them under the sentence, because a terminal the
// operator owns is not a wire.
package selfupdate

// exitUsage matches sysexits EX_USAGE (64). A candidate install with no tag is
// a command that was written wrong; nothing about the system is broken.
const exitUsage int = 64

// exitCantCreate matches sysexits EX_CANTCREAT (73). Staging, replacing and an
// authorised escalation all fail because an output file could not be created
// or moved, which is exactly what that status is for.
const exitCantCreate int = 73
