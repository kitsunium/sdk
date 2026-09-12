// Package git — resolving what a branch changed versus its merge-base with the
// default branch.
package git

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	corevcs "github.com/kitsunium/sdk/internal/core/vcs"
)

var (
	// defaultBranchCandidates are the fallback refs tried, in order, when
	// origin/HEAD does not resolve the default branch symbolically.
	defaultBranchCandidates = []string{"origin/main", "origin/master", "main", "master"}

	// diffBaseArgs is the fixed `git diff` argument prefix shared by every diff
	// source: minimal context (-U0) with rename (-M) and copy (-C) detection.
	diffBaseArgs = []string{"diff", "-U0", "-M", "-C"}

	// nameStatusBaseArgs is the fixed argument prefix of the membership pass:
	// NUL-separated (-z), never c-quoted file statuses with the same rename
	// (-M) and copy (-C) detection as diffBaseArgs, immune to exotic filenames
	// (spaces, tabs, newlines, non-ASCII) that defeat unified-diff headers.
	nameStatusBaseArgs = []string{"diff", "--name-status", "-z", "-M", "-C"}
)

// fullFallback builds a ResolutionValue signalling the degrade, with the given
// reason. Nil Set + FullFallback=true is the contract a caller checks to treat
// everything as in scope.
func fullFallback(reason string) corevcs.ResolutionValue {
	//: Nil Set + FullFallback=true is the documented degrade signal.
	return corevcs.ResolutionValue{FullFallback: true, Reason: reason}
}

// Resolve computes the changed set for the repository containing cfg.Root
// (a directory hint; "" resolves from the current working directory).
//
// It is fail-safe by design: any condition that prevents a trustworthy diff
// (no repository, shallow clone, unresolved default branch, no merge-base)
// yields a ResolutionValue with FullFallback=true and a Reason — never an error
// and never a silent empty diff. The caller degrades to treating everything as
// in scope, and surfaces Reason.
//
// That asymmetry is the whole design. "Nothing changed" and "I could not tell
// what changed" are opposite instructions to a caller, and a resolver that
// returned an empty set for the second would make a review pass on a branch it
// never looked at.
func Resolve(ctx context.Context, cfg Config) corevcs.ResolutionValue {
	root, ok := repoTopLevel(ctx, cfg.Root)
	//: No repository → everything in scope (the documented fallback).
	if !ok {
		//: Degrade loudly: everything is in scope.
		return fullFallback("not a git repository — everything in scope")
	}
	//: A shallow clone lacks the history needed for a trustworthy merge-base.
	if isShallow(ctx, root) {
		//: Degrade loudly rather than trust truncated history.
		return fullFallback("shallow clone — everything in scope")
	}
	baseRef, ok := resolveBaseRef(ctx, root)
	//: No default branch → cannot define "changed vs main".
	if !ok {
		//: Degrade loudly: everything is in scope.
		return fullFallback("default branch unresolved — everything in scope")
	}
	baseSHA, ok := mergeBase(ctx, root, baseRef)
	//: Unrelated histories / unborn branch → no comparison point.
	if !ok {
		//: Degrade loudly: everything is in scope.
		return fullFallback("no merge-base with " + baseRef + " — everything in scope")
	}
	headSHA, err := runGitOutput(ctx, root, "rev-parse", "HEAD")
	//: An unresolved HEAD (empty repo) → everything in scope.
	if err != nil {
		//: Degrade loudly: everything is in scope.
		return fullFallback("HEAD unresolved — everything in scope")
	}

	set := NewChangedSetValue(root)
	//: A git failure or timeout while collecting the diff yields a PARTIAL
	//: changed-set; surfacing that would silently hide real issues. Degrade
	//: loudly to a full scan instead (fail-safe: never a silent empty diff).
	if err := collectChanges(ctx, root, baseSHA, set, cfg.Include); err != nil {
		//: Degrade loudly on any diff-collection failure.
		return fullFallback("git diff failed or timed out — everything in scope")
	}

	//: Successful resolution — the set is trustworthy.
	return corevcs.ResolutionValue{
		Set:     set,
		BaseRef: baseRef,
		BaseSHA: baseSHA,
		HeadSHA: headSHA,
	}
}

// repoTopLevel resolves the absolute repository root. A hint directory is used
// to locate the repo; an empty hint resolves from the process working dir.
func repoTopLevel(ctx context.Context, hint string) (string, bool) {
	args := []string{"rev-parse", "--show-toplevel"}
	//: When a hint is given, run git inside it so the right repo is found.
	if hint != "" {
		abs, err := filepath.Abs(hint)
		//: An unresolvable hint cannot anchor a repo lookup.
		if err != nil {
			//: Signal "no repo" so the caller falls back.
			return "", false
		}
		out, err := runGitOutput(ctx, abs, "rev-parse", "--show-toplevel")
		//: A git failure means the hint is not inside a repo.
		if err != nil {
			//: Signal "no repo".
			return "", false
		}
		//: Resolved top-level for the hinted repo.
		return out, true
	}
	out, err := runGitOutput(ctx, ".", args...)
	//: A git failure means the cwd is not inside a repo.
	if err != nil {
		//: Signal "no repo".
		return "", false
	}
	//: Resolved top-level for the cwd repo.
	return out, true
}

// isShallow reports whether the repository is a shallow clone.
func isShallow(ctx context.Context, root string) bool {
	out, err := runGitOutput(ctx, root, "rev-parse", "--is-shallow-repository")
	//: A failed probe is treated as "not shallow" — base resolution then
	//: applies its own fallbacks.
	if err != nil {
		//: Cannot confirm shallow → assume full history.
		return false
	}
	//: git prints "true"/"false".
	return out == "true"
}

// resolveBaseRef resolves the default-branch ref, preferring the symbolic
// origin/HEAD and falling back to a fixed candidate list.
func resolveBaseRef(ctx context.Context, root string) (string, bool) {
	out, err := runGitOutput(ctx, root, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	//: origin/HEAD resolves the true default branch on most clones.
	if err == nil && out != "" {
		//: Strip the refs/remotes/ prefix to get e.g. "origin/main".
		return strings.TrimPrefix(out, "refs/remotes/"), true
	}
	//: Fall back to conventional default-branch refs.
	for _, cand := range defaultBranchCandidates {
		//: Accept the first candidate that names a real commit.
		if gitProbe(ctx, root, "rev-parse", "--verify", "--quiet", cand+"^{commit}") {
			//: Use the first existing candidate.
			return cand, true
		}
	}
	//: No default branch could be resolved.
	return "", false
}

// mergeBase returns the merge-base commit between baseRef and HEAD.
func mergeBase(ctx context.Context, root, baseRef string) (string, bool) {
	out, err := runGitOutput(ctx, root, "merge-base", baseRef, "HEAD")
	//: No common ancestor (unrelated histories) → no comparison point.
	if err != nil || out == "" {
		//: Signal "no base".
		return "", false
	}
	//: The merge-base SHA defines the branch's true starting point.
	return out, true
}

// collectChanges populates set from the four diff sources that together define
// "changed vs the default branch": committed (merge-base→HEAD), staged
// (index), unstaged (working tree), and untracked files. Any git failure or
// timeout is returned so the caller can degrade loudly to a full scan rather
// than surface a partial (silently under-reported) changed-set.
func collectChanges(ctx context.Context, root, baseSHA string, set *ChangedSetValue, include IncludeFunc) error {
	//: Committed delta from the merge-base to HEAD (the branch's contribution).
	if err := addDiff(ctx, root, set, include, baseSHA, "HEAD"); err != nil {
		//: Propagate so the caller falls back loudly.
		return err
	}
	//: Staged but uncommitted edits (the index).
	if err := addDiff(ctx, root, set, include, "--cached"); err != nil {
		//: Propagate so the caller falls back loudly.
		return err
	}
	//: Unstaged working-tree edits.
	if err := addDiff(ctx, root, set, include); err != nil {
		//: Propagate so the caller falls back loudly.
		return err
	}
	//: Untracked files (whole-file changes).
	return addUntracked(ctx, root, set, include)
}

// addDiff folds one diff source into set in two passes: first the NUL-separated
// `git diff --name-status -z` file list (authoritative file/package membership,
// robust to any filename), then the unified `git diff -U0 -M -C` payload solely
// for "+"-side line ranges. A genuine git failure (bad ref, killed by the
// context deadline) is returned; an empty diff (no changes, exit 0) contributes
// nothing and is not an error.
func addDiff(ctx context.Context, root string, set *ChangedSetValue, include IncludeFunc, extraArgs ...string) error {
	//: Membership first: the -z name-status list cannot be defeated by
	//: c-quoting, so a file whose unified-diff header is unresolvable
	//: (tab/newline in the name) is still file-touched — no silent drops, ever.
	if err := addNameStatus(ctx, root, set, include, extraArgs...); err != nil {
		//: Propagate the failure to trigger a loud full-scan fallback.
		return err
	}
	//: Concat clones the package-level prefix so appending extraArgs never
	//: mutates the shared backing array.
	args := slices.Concat(diffBaseArgs, extraArgs)
	out, err := runGitOutput(ctx, root, args...)
	//: A real git failure (or timeout) must surface — never a silent partial.
	if err != nil {
		//: Propagate the failure to trigger a loud full-scan fallback.
		return err
	}
	//: An empty diff (exit 0, no changes) legitimately contributes nothing.
	if out == "" {
		//: Nothing to fold in from this source.
		return nil
	}
	parseUnifiedDiff(out, root, set, include)
	//: Source folded in successfully.
	return nil
}

// addNameStatus runs `git diff --name-status -z -M -C <extraArgs...>` and folds
// the NUL-separated file list into set as file/package membership (no line
// ranges — those come from the unified-diff pass). A genuine git failure (or
// timeout) is returned; an empty list (exit 0) contributes nothing and is not
// an error.
func addNameStatus(ctx context.Context, root string, set *ChangedSetValue, include IncludeFunc, extraArgs ...string) error {
	//: Concat clones the package-level prefix so appending extraArgs never
	//: mutates the shared backing array.
	args := slices.Concat(nameStatusBaseArgs, extraArgs)
	out, err := runGitOutput(ctx, root, args...)
	//: A real git failure (or timeout) must surface — never a silent partial.
	if err != nil {
		//: Propagate the failure to trigger a loud full-scan fallback.
		return err
	}
	//: An empty list (exit 0, no changes) legitimately contributes nothing.
	if out == "" {
		//: Nothing to fold in from this source.
		return nil
	}
	parseNameStatus(out, root, set, include)
	//: Membership folded in successfully.
	return nil
}

// addUntracked adds untracked, non-ignored files as whole-file changes. A git
// failure (or timeout) is returned; no untracked files (exit 0) is not an error.
func addUntracked(ctx context.Context, root string, set *ChangedSetValue, include IncludeFunc) error {
	//: -z makes ls-files emit NUL-separated, never-quoted paths so untracked
	//: files with exotic names (tab, newline, quote) survive parsing too.
	out, err := runGitOutput(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	//: A real probe failure (or timeout) must surface for a loud fallback.
	if err != nil {
		//: Propagate the failure.
		return err
	}
	//: No untracked files legitimately contributes nothing.
	if out == "" {
		//: Nothing to add.
		return nil
	}
	//: Each NUL-separated entry is a repo-relative untracked path (SplitSeq
	//: avoids the slice; the trailing NUL yields one empty entry, skipped below).
	for rel := range strings.SplitSeq(out, "\x00") {
		//: Skip blank lines from the split.
		if rel == "" {
			continue
		}
		//: Only untracked files the caller's filter admits enter the set.
		if abs, ok := scopedAbs(rel, root, include); ok {
			set.addWholeFile(abs)
		}
	}
	//: All untracked files folded in.
	return nil
}
