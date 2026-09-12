//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/git .

// Package git answers what a branch changed, by shelling out to the git binary.
//
// It is the thin public facade over internal/service/vcs/git: the value types
// are aliases of the core vcs types and the functions delegate straight to the
// service implementation.
//
// # Usage
//
// Resolve the changed set, then ask about it at whichever granularity matters:
//
//	import (
//		"github.com/kitsunium/sdk/pkg/v1/git"
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
// # Running against a repository you do not control
//
// Every invocation is hardened against a hostile `.git/config`, which travels
// with a clone. `core.fsmonitor` and `core.hooksPath` are neutralised with -c
// (which beats every config file), and `--no-ext-diff` is injected into the
// subcommands that honour `diff.external` — setting that key empty does not
// disable it, it makes git try to execute "" and abort. Both were demonstrated
// executing an attacker-chosen command on a read-only query before the guard,
// and not after. See the service package's CLAUDE.md for what was deliberately
// NOT hardened, and why.
package git

import (
	"context"

	corevcs "github.com/kitsunium/sdk/internal/core/vcs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcgit "github.com/kitsunium/sdk/internal/service/vcs/git"
)

// CodeRepositoryUnresolved identifies a path that is not inside a readable
// repository. Match it with errs.HasCode: it is the one refusal a caller can act
// on by falling back to a non-VCS path.
const CodeRepositoryUnresolved errs.Code = corevcs.CodeRepositoryUnresolved

// CodeCommandFailed identifies a version-control command that exited non-zero —
// a corrupted object store, a permission error, a missing binary.
const CodeCommandFailed errs.Code = corevcs.CodeCommandFailed

// CodePathAbsent identifies a path that does not exist at the requested commit,
// which is deliberately distinct from a successful read of an empty file.
const CodePathAbsent errs.Code = corevcs.CodePathAbsent

// ChangedSet is what a branch changed, queryable by line, file or directory. It
// aliases the core vcs port.
type ChangedSet = corevcs.ChangedSet

// LineRange is an inclusive, 1-based run of changed lines. It aliases the core
// vcs value type.
type LineRange = corevcs.LineRangeValue

// Resolution is the outcome of resolving a changed set, including the degraded
// outcome where none could be trusted. It aliases the core vcs value type.
type Resolution = corevcs.ResolutionValue

// Config is where the repository is and which files the caller counts as
// changed. It aliases the service type: these are one implementation's
// construction parameters, which is what ADR 0074 says belongs with the engine.
type Config = svcgit.Config

// Include decides which files belong in the changed set. A nil Include admits
// every file.
type Include = svcgit.IncludeFunc

// Resolve computes the changed set for the repository containing cfg.Root,
// returning a degraded Resolution rather than an error when it cannot be
// trusted. Check Resolution.Degraded before reading Set.
func Resolve(ctx context.Context, cfg Config) Resolution {
	//: delegate verbatim to the service implementation.
	return svcgit.Resolve(ctx, cfg)
}

// GitDir resolves the git directory governing root — the one holding HEAD and
// the index — and memoizes it per root.
//
// It never assumes `.git` is a directory: in a linked worktree or a submodule it
// is a `gitdir:` pointer FILE, and this follows it. The result is absolute and
// cleaned. Failures are not memoized, so a later `git init` is picked up.
func GitDir(ctx context.Context, root string) (gitDir string, err error) {
	//: delegate verbatim to the service implementation.
	return svcgit.GitDir(ctx, root)
}

// ShowFile returns the content of relPath at commit sha. A path absent at that
// commit is an error, which is what lets a caller tell "deleted" from "emptied".
func ShowFile(ctx context.Context, repoRoot, sha, relPath string) (content string, err error) {
	//: delegate verbatim to the service implementation.
	return svcgit.ShowFile(ctx, repoRoot, sha, relPath)
}
