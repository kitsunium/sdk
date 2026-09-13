// Package git — reading a file's content at a specific commit.
package git

import (
	"context"

	corevcs "github.com/kitsunium/sdk/internal/core/vcs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ShowFile returns the content of relPath at commit sha via `git show`.
// relPath is resolved relative to repoRoot (the "./" prefix pins git's
// path resolution to the -C directory instead of the repository top level).
//
// The content is returned VERBATIM. It deliberately does not go through
// runGitOutput, whose trim is right for a SHA or a ref name and wrong for a
// file: leading and trailing whitespace is the file's own, and a
// whitespace-only blob would otherwise come back indistinguishable from empty.
//
// A failure is one of two refusals and never the same one twice. PathAbsent
// says the commit is there and holds no object at that path, which is what a
// caller reconstructing history needs in order to tell "deleted" from
// "emptied" — an empty file is a successful read of "". CommandFailed says
// anything else: an unreachable commit, an unreadable object store, no git.
func ShowFile(ctx context.Context, repoRoot, sha, relPath string) (content string, err error) {
	spec := sha + ":./" + relPath
	content, stderrTail, runErr := runGitBlob(ctx, repoRoot, "show", spec)
	//: A successful read is the whole answer, whitespace and all.
	if runErr == nil {
		//: The blob, verbatim.
		return content, nil
	}
	//: git's stderr names the reason in the operator's language, so the
	//: refusal is decided by two probes rather than by reading a message
	//: whose wording is locale-dependent and unversioned. Both run only on
	//: the failure path, which is already an exceptional one.
	if pathAbsent(ctx, repoRoot, sha, spec) {
		//: The commit exists and has nothing at that path.
		return "", errs.Wrap(runErr, errs.WrapParams{
			Code:    corevcs.CodePathAbsent,
			Reason:  "PATH_ABSENT",
			Public:  "the path does not exist at that commit",
			Private: "service/vcs/git: " + relPath + " is absent from " + sha + ": " + stderrTail,
		})
	}
	//: Anything else stays the generic command failure.
	return "", blobFailure(runErr, stderrTail, "show", spec)
}

// pathAbsent reports whether a failed `git show <sha>:./<path>` failed because
// the tree at sha holds no object at that path.
//
// It is two questions and both have to be asked. `cat-file -e` answers "no such
// object" the same way for a commit that does not resolve and for a path that
// is not in it, so an unreachable sha would otherwise be reported as a missing
// file and a caller would retry with a different path forever.
func pathAbsent(ctx context.Context, repoRoot, sha, spec string) bool {
	//: A commit that does not resolve says nothing about any path in it.
	if !gitProbe(ctx, repoRoot, "cat-file", "-e", sha+"^{commit}") {
		//: Not an absent path — an unreachable commit.
		return false
	}
	//: The commit is readable, so the object's absence is the path's absence.
	return !gitProbe(ctx, repoRoot, "cat-file", "-e", spec)
}
