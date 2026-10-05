package git

import (
	"context"

	coregit "github.com/kitsunium/sdk/framework/internal/core/git"
	svcgit "github.com/kitsunium/sdk/framework/internal/service/git"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// CodeRepositoryUnresolved identifies a path that is not inside a readable
// repository. Match it with errs.HasCode: it is the one refusal a caller can act
// on by falling back to a non-VCS path.
const CodeRepositoryUnresolved errs.Code = coregit.CodeRepositoryUnresolved

// CodeCommandFailed identifies a version-control command that exited non-zero —
// a corrupted object store, a permission error, a missing binary.
const CodeCommandFailed errs.Code = coregit.CodeCommandFailed

// CodePathAbsent identifies a path that does not exist at the requested commit,
// which is deliberately distinct from a successful read of an empty file. It is
// what ShowFile returns when the commit resolves and its tree holds no object
// at the path.
const CodePathAbsent errs.Code = coregit.CodePathAbsent

// ChangedSet is what a branch changed, queryable by line, file or directory. It
// aliases the core git port.
type ChangedSet = coregit.ChangedSet

// LineRange is an inclusive, 1-based run of changed lines. It aliases the core
// git value type.
type LineRange = coregit.LineRangeValue

// Resolution is the outcome of resolving a changed set, including the degraded
// outcome where none could be trusted. It aliases the core git value type.
type Resolution = coregit.ResolutionValue

// Config is where the repository is and which files the caller counts as
// changed. It aliases the service type: these are one implementation's
// construction parameters, which is what ADR 0074 says belongs with the engine.
type Config = svcgit.Config

// Include decides which files belong in the changed set. A nil Include admits
// every file.
type Include = svcgit.IncludeFunc

// HeadState is what a working tree is at: the commit HEAD names, its
// committer date in the offset it was recorded with, and whether a tracked
// file differs from it. It aliases the service type — the core git port models no
// commit, so this is one engine's value (ADR 0074).
type HeadState = svcgit.HeadValue

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
// cleaned, and failures are never memoized.
//
// The memo is re-validated on every call against two lstats — roughly a
// thousandth of the subprocess they stand in for — so a repository that moved,
// one deleted and re-created, and a root that becomes its own repository under
// a parent one each get a fresh answer. A repository created at a directory
// BETWEEN root and the worktree top level is not noticed.
func GitDir(ctx context.Context, root string) (gitDir string, err error) {
	//: delegate verbatim to the service implementation.
	return svcgit.GitDir(ctx, root)
}

// Head reports what the working tree containing dir is at: its head commit,
// that commit's time, and whether tracked files differ from it.
//
// A dir outside any repository — or absent, or on a machine without git — is
// CodeRepositoryUnresolved. A repository whose HEAD names no commit yet, a
// bare repository and a failed read are CodeCommandFailed. The three git
// commands it runs are hardened like every other one here, and the commit
// time is read from the raw commit object rather than through `git log`, so a
// planted signature program has nothing to verify.
func Head(ctx context.Context, dir string) (head HeadState, err error) {
	//: delegate verbatim to the service implementation.
	return svcgit.Head(ctx, dir)
}

// ShowFile returns the content of relPath at commit sha, verbatim.
//
// Its two refusals are different instructions. CodePathAbsent means the commit
// is readable and holds nothing at that path, which is what tells "deleted"
// from "emptied" — an empty file is a successful read of "". CodeCommandFailed
// means anything else: a commit that does not resolve, an unreadable object
// store, no git. Match with errs.HasCode.
func ShowFile(ctx context.Context, repoRoot, sha, relPath string) (content string, err error) {
	//: delegate verbatim to the service implementation.
	return svcgit.ShowFile(ctx, repoRoot, sha, relPath)
}
