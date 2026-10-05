// Package git answers what a branch changed and what a working tree is at, by
// shelling out to the git binary.
//
// It is the framework's public package over framework/internal/service/git (ADR
// 0158): the value types are aliases of the core git contract's types and the
// functions delegate straight to the service implementation. What a running
// program was BUILT from is not here — that is pkg/v1/proc/process's Build and Self,
// which stay in the SDK; this package asks git about a working tree.
//
// # Usage
//
// Resolve the changed set, then ask about it at whichever granularity matters:
//
//	import (
//		"github.com/kitsunium/sdk/framework/git"
//	)
//
//	res := git.Resolve(ctx, git.Config{Root: "/path/to/repo"})
//	if res.Degraded() {
//		// res.Reason says why; treat EVERYTHING as in scope.
//		return
//	}
//	if res.Set.ContainsLine(file, line) { /* this line moved */ }
//
// # Degrading is not failing, and an empty set is not a degrade
//
// Resolve returns no error. Any condition that prevents a trustworthy answer —
// no repository, a shallow clone, an unresolved default branch, no merge-base,
// a git invocation that failed or timed out — produces a ResolutionValue with
// FullFallback set and a Reason, never a partial set and never an empty one.
//
// The distinction is the reason this package exists in this shape. "Nothing
// changed" and "I could not tell what changed" are opposite instructions to the
// caller, and a resolver that answered the second with an empty set would make a
// scoped review silently pass on a branch it never examined. Check Degraded
// before reading Set.
//
// # What counts as changed
//
// The set spans four sources, because a review that ignored uncommitted work
// would contradict what the author sees: the three-dot merge-base delta to HEAD,
// the index, the working tree, and untracked files. Renames and copies are
// detected (-M -C); a pure rename or a deletion marks the file and its directory
// touched while contributing no line range, so ContainsFile answers true where
// ContainsLine answers false for every line.
//
// Config.Include is the caller's policy for which files belong. A nil Include
// admits everything; a Go tool passes a ".go" suffix test and a generated-file
// probe. The source implementation hard-coded exactly that pair, and this
// package deliberately does not: it is one caller's policy, not the domain's.
//
// # Which spelling of a path answers
//
// Queries are compared lexically, so two spellings of one file do not match
// each other — with one exception, and it is the spelling a caller does not
// choose. git canonicalises the repository root, so pointing Config.Root at a
// symbolic link records every path under the link's target. Resolve therefore
// records every entry under BOTH the root you gave and the one git reports, so
// either answers and a query costs exactly what it did before. An indirection
// anywhere else in a path you query is still lexical and still does not match.
//
// # What a working tree is at
//
// Head answers the question a build description asks about a local module:
// which commit its working tree is on, when that commit was made, and whether
// tracked files differ from it.
//
//	head, err := git.Head(ctx, "/path/to/module")
//	if err != nil {
//		// no repository, no git, or no commit yet: go without.
//	}
//	fmt.Println(head.Revision, head.Time, head.Modified)
//
// Modified counts TRACKED files only, staged or not; an untracked file does
// not make a tree modified. That is narrower than the vcs.modified stamp Go
// writes into a binary, which counts untracked files too. Nothing is cached,
// because modified is the one fact here that changes without a commit.
//
// # Running against a repository you do not control
//
// Every invocation is hardened against a hostile `.git/config`, which travels
// with a clone. `core.fsmonitor` and `core.hooksPath` are neutralised with -c
// (which beats every config file), and `--no-ext-diff` is injected into the
// subcommands that honour `diff.external` — setting that key empty does not
// disable it, it makes git try to execute "" and abort. Both were demonstrated
// executing an attacker-chosen command on a read-only query before the guard,
// and not after.
//
// A second group executes nothing and is guarded for a different reason.
// `diff.srcPrefix`, `diff.dstPrefix`, `diff.mnemonicPrefix` and `diff.noprefix`
// rename the `a/` and `b/` prefixes of a diff header, which files the line
// ranges under a path nobody queries: measured, ContainsFile stayed true and
// ContainsLine went false for a line that had just changed. All four are pinned
// to git's defaults. See the service package's CLAUDE.md for what was
// deliberately NOT hardened, and why.
package git
