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

// originHEAD is the remote-tracking symbolic ref that names the default branch.
// It is spelled in full rather than as "origin/HEAD" so rev-parse resolves the
// remote-tracking ref and cannot be steered onto a refs/origin/HEAD a
// repository planted higher in its search order.
const originHEAD string = "refs/remotes/origin/HEAD"

// separator is the OS path separator as a string, for the prefix and suffix
// tests that rewrite one spelling of the repository root into the other.
const separator string = string(filepath.Separator)

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
	root, spelled, ok := repoTopLevel(ctx, cfg.Root)
	//: No repository → everything in scope (the documented fallback).
	if !ok {
		//: Degrade loudly: everything is in scope.
		return fullFallback("not a git repository — everything in scope")
	}
	shallow, known := shallowState(ctx, root)
	//: A probe that did not answer is not a repository with full history.
	if !known {
		//: Degrade loudly rather than guess the answer git refused to give.
		return fullFallback("shallow probe failed — everything in scope")
	}
	//: A shallow clone lacks the history needed for a trustworthy merge-base.
	if shallow {
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

	set := newChangedSetValue(root, spelled)
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

// repoTopLevel resolves the repository root from a hint directory; an empty
// hint resolves from the process working directory.
//
// It answers with TWO spellings of one directory. canonical is git's own
// --show-toplevel, in the operating system's path form, which every recorded
// path is built from. asSpelled is the same directory named the way the caller
// named it, and is empty unless the caller's spelling differs from git's — a
// symbolic link git resolved away, or on Windows an 8.3 short name git
// expanded — so without it a caller whose paths use that spelling queries a
// set keyed under a root it never spells.
func repoTopLevel(ctx context.Context, hint string) (canonical, asSpelled string, ok bool) {
	start := hint
	//: An empty hint means "the repository around the working directory".
	if start == "" {
		start = "."
	}
	abs, err := filepath.Abs(start)
	//: An unresolvable hint cannot anchor a repo lookup.
	if err != nil {
		//: Signal "no repo" so the caller falls back.
		return "", "", false
	}
	out, err := runGitOutput(ctx, abs, "rev-parse", "--show-toplevel")
	//: A git failure means the hint is not inside a repo.
	if err != nil {
		//: Signal "no repo".
		return "", "", false
	}
	//: git prints '/'-separated paths on every platform, Windows included,
	//: while every recorded path is built by filepath.Join in the OS's own
	//: form. Keyed raw, the root was `C:/…` against entries under `C:\…` on
	//: Windows, so the alias rewrite — a prefix match on the root — never
	//: matched, and every query through the caller's spelling answered false.
	canonical = filepath.Clean(out)
	asSpelled, _ = spelledTopLevel(abs, canonical)
	//: Resolved top-level, plus the caller's own spelling of it when it differs.
	//: A hint that already reaches the root directly yields no second spelling,
	//: and "" is exactly what the changed set reads as "there is none".
	return canonical, asSpelled, true
}

// spelledTopLevel returns the repository top-level named the way the caller
// named it, or "" when the caller's path already reaches it directly.
//
// The only EvalSymlinks this package performs happen here, and only when the
// hint is not already inside the canonical root — which is the ordinary case,
// so the ordinary case pays nothing. What they buy is an O(1) prefix rewrite
// on every later query instead of a syscall per query.
//
// Both sides are resolved before they are compared. The hint, because that is
// where the caller's indirection lives. The canonical root, because what git
// resolves is git's business and differs by platform: on Windows it expands an
// 8.3 short name (`RUNNER~1`, which is how %TEMP% is spelled on a CI runner)
// and EvalSymlinks does too, but whether it follows a link is the git build's
// choice. Comparing git's answer to a fully resolved hint would name no alias
// whenever the two stopped at different places.
func spelledTopLevel(absHint, canonical string) (alias string, ok bool) {
	clean := filepath.Clean(absHint)
	//: The caller is already speaking git's spelling — nothing to alias.
	if clean == canonical || strings.HasPrefix(clean, canonical+separator) {
		//: No alias.
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(clean)
	//: A hint whose links do not resolve keeps the purely lexical behaviour.
	if err != nil {
		//: No alias.
		return "", false
	}
	root, rootErr := filepath.EvalSymlinks(canonical)
	//: A root that no longer resolves is compared as git spelled it.
	if rootErr != nil {
		root = canonical
	}
	//: The alias is the hint with its in-repository tail stripped back off.
	return trimRepoTail(clean, root, resolved)
}

// trimRepoTail removes from clean the path the resolved hint holds below
// canonical, leaving the caller's spelling of the top level. It returns ""
// when the resolved hint is not under canonical, or when the tail is not
// literally present in the caller's spelling — which is the case where the
// indirection sits inside the last components and no prefix can describe it.
func trimRepoTail(clean, canonical, resolved string) (alias string, ok bool) {
	rel, err := filepath.Rel(canonical, resolved)
	//: A hint that does not sit under the canonical root cannot name it.
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+separator) {
		//: No alias.
		return "", false
	}
	//: The hint IS the top level, spelled the caller's way.
	if rel == "." {
		//: The whole hint is the alias.
		return clean, true
	}
	trimmed, cut := strings.CutSuffix(clean, separator+rel)
	//: Without a literal tail match the alias would be a guess, so there is none.
	if !cut {
		//: No alias.
		return "", false
	}
	//: The caller's spelling of the top level.
	return trimmed, true
}

// shallowState reports whether the repository is a shallow clone, and whether
// the probe answered at all.
//
// Those are two facts and only one of them is safe to guess. A probe that
// fails — a git too old for the flag, a git that is not there, a repository
// that became unreadable between two invocations — has told us nothing, and
// nothing is not "full history": reading it that way hands back a set scoped
// against history that was truncated. known=false is what makes Resolve
// degrade instead of computing a diff it cannot trust.
func shallowState(ctx context.Context, root string) (shallow, known bool) {
	out, err := runGitOutput(ctx, root, "rev-parse", "--is-shallow-repository")
	//: The probe did not run — we know nothing, and nothing is not "false".
	if err != nil {
		//: Unknown: Resolve degrades.
		return false, false
	}
	//: git answers with exactly two words, and anything else is a git whose
	//: output this package was not written against.
	switch out {
	//: git's own word for a truncated history.
	case "true":
		//: Shallow, and we know it.
		return true, true
	//: git's own word for full history — stated by git, not assumed by us.
	case "false":
		//: Not shallow, and we know it.
		return false, true
	//: Anything else is a git whose answer we cannot read.
	default:
		//: Unknown: Resolve degrades.
		return false, false
	}
}

// resolveBaseRef resolves the default-branch ref, preferring the symbolic
// origin/HEAD and falling back to a fixed candidate list.
func resolveBaseRef(ctx context.Context, root string) (string, bool) {
	out, err := runGitOutput(ctx, root, "rev-parse", "--verify", "--quiet", "--abbrev-ref", originHEAD)
	//: origin/HEAD names the true default branch on most clones — and
	//: --verify answers only while that branch still EXISTS, which
	//: symbolic-ref does not check: it reports a symref whose target was
	//: pruned exactly as happily as a live one, and every clone taken before
	//: an upstream renamed master to main is in that state until someone runs
	//: `git remote set-head`. --abbrev-ref renders it as "origin/main",
	//: the same shape the candidate list below uses.
	if err == nil && out != "" {
		//: A verified default-branch ref.
		return out, true
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
