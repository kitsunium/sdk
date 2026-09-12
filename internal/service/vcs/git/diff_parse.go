// Package git — parsing git's unified-diff and name-status output into a
// changed set.
package git

import (
	"path/filepath"
	"strconv"
	"strings"

	corevcs "github.com/kitsunium/sdk/internal/core/vcs"
)

// minHunkFields is the minimum whitespace-separated token count of a well-formed
// unified-diff hunk header ("@@", "-a,b", "+c,d").
const minHunkFields int = 3

// nameStatusSingleWidth is the token width of a single-path name-status record
// (status + path) in the `git diff --name-status -z` wire format.
const nameStatusSingleWidth int = 2

// nameStatusRenameWidth is the token width of a rename/copy name-status record
// (status + old path + new path).
const nameStatusRenameWidth int = 3

// nameStatusNewPathOffset is the offset of the rename/copy NEW path within its
// record (after the status and the old path).
const nameStatusNewPathOffset int = 2

// fileBlock is the per-file parser state accumulated while scanning one
// "diff --git" block: the post-edit path, the pre-edit path, and whether any
// +side hunk was seen.
type fileBlock struct {
	target  string
	oldPath string
	hadHunk bool
}

// parseUnifiedDiff feeds the "+"-side line ranges of a `git diff -U0 -M -C`
// payload into set. New/renamed paths are attributed to their post-edit path;
// deletions and pure renames mark the file/package touched without line ranges.
// Generated files (per include) are skipped entirely so a `make generate` churn
// never floods diff mode.
func parseUnifiedDiff(diff, root string, set *ChangedSetValue, include IncludeFunc) {
	var blk fileBlock
	//: Path headers always precede the first @@ in a diff --git block. Once
	//: inside hunks, +/- lines are content: under -U0 a deleted line whose
	//: content starts with "-- " becomes "--- …" and an added line starting
	//: with "++ " becomes "+++ …", which updatePaths would misread as a path
	//: header. Gating to the pre-hunk region keeps the changed-set exact.
	inHunks := false

	//: SplitSeq streams lines without allocating an intermediate slice.
	for line := range strings.SplitSeq(diff, "\n") {
		//: A new file header flushes the previous file's touched-only state.
		if strings.HasPrefix(line, "diff --git ") {
			flushFile(set, root, include, blk)
			blk = fileBlock{}
			inHunks = false
			continue
		}
		//: A hunk header carries the +side range; attribute it to target.
		if strings.HasPrefix(line, "@@ ") {
			inHunks = true
			//: Record a range only when the hunk adds lines to a scoped target.
			if applyHunk(line, blk.target, root, set, include) {
				blk.hadHunk = true
			}
			continue
		}
		//: Inside hunks the line is +/- content, never a path header.
		if inHunks {
			continue
		}
		//: Otherwise the line may update the pre/post-edit path bookkeeping.
		blk.target, blk.oldPath = updatePaths(line, blk.target, blk.oldPath)
	}

	//: Flush the final file block.
	flushFile(set, root, include, blk)
}

// updatePaths interprets a diff header line and returns the (possibly updated)
// post-edit target and pre-edit oldPath. Non-header lines pass through unchanged.
func updatePaths(line, target, oldPath string) (newTarget, newOld string) {
	//: rename/copy source header → pre-edit path.
	if from, ok := cutAnyPrefix(line, "rename from ", "copy from "); ok {
		//: Update the pre-edit path only.
		return target, from
	}
	//: rename/copy target header → post-edit path.
	if to, ok := cutAnyPrefix(line, "rename to ", "copy to "); ok {
		//: Update the post-edit path only.
		return to, oldPath
	}
	//: "--- " header → pre-edit path (a/ stripped; /dev/null keeps fallback).
	if raw, ok := strings.CutPrefix(line, "--- "); ok {
		//: Update the pre-edit path from the old-side header.
		return target, oldPathFrom(raw, oldPath)
	}
	//: "+++ " header → post-edit path (b/ stripped; /dev/null clears target).
	if raw, ok := strings.CutPrefix(line, "+++ "); ok {
		newTgt, _ := newPathFrom(raw)
		//: Update the post-edit path (empty for a deletion).
		return newTgt, oldPath
	}
	//: Unrelated line — bookkeeping unchanged.
	return target, oldPath
}

// flushFile records touched-only state for a file block that produced no +side
// hunk (pure rename, copy, mode change) and for the old side of a deletion or
// rename, so file- and package-scoped rules surface while line-scoped rules do
// not.
func flushFile(set *ChangedSetValue, root string, include IncludeFunc, blk fileBlock) {
	//: New-path side with no +side hunk is file/package-touched, no lines.
	if blk.target != "" && !blk.hadHunk {
		markPath(set, blk.target, root, include)
	}
	//: Old-path side of a deletion/rename keeps its package touched.
	if blk.oldPath != "" && blk.oldPath != blk.target {
		markPath(set, blk.oldPath, root, include)
	}
}

// applyHunk parses one @@ header and, when it adds lines to a scoped target,
// records the range. Returns true when a range was recorded.
func applyHunk(line, target, root string, set *ChangedSetValue, include IncludeFunc) bool {
	//: No target means a deletion (+++ /dev/null) — nothing to add.
	if target == "" {
		//: Skip ranges for a deleted file.
		return false
	}
	rng, ok := parseHunkPlusRange(line)
	//: A zero-line (+c,0) or unparseable hunk records nothing.
	if !ok {
		//: No +side lines in this hunk.
		return false
	}
	abs, scoped := scopedAbs(target, root, include)
	//: Targets the caller's filter excludes are dropped.
	if !scoped {
		//: Skip a filtered-out file.
		return false
	}
	set.addLineRange(abs, rng)
	//: A range was recorded for this file.
	return true
}

// markPath records relPath (and its directory) as touched when the caller's
// filter admits it, with no associated line range.
func markPath(set *ChangedSetValue, relPath, root string, include IncludeFunc) {
	//: Only scoped paths contribute to the touched sets.
	if abs, ok := scopedAbs(relPath, root, include); ok {
		set.markTouched(abs)
	}
}

// scopedAbs resolves a diff-relative path to an absolute path and reports
// whether the caller's filter admits it. A nil filter admits everything.
func scopedAbs(relPath, root string, include IncludeFunc) (abs string, ok bool) {
	candidate := filepath.Join(root, relPath)
	//: The caller's policy decides what belongs in the set; nil admits all.
	if include != nil && !include(candidate) {
		//: Excluded by the caller's filter.
		return "", false
	}
	//: In-scope absolute path.
	return candidate, true
}

// cutAnyPrefix strips whichever of two prefixes line carries, reporting which.
func cutAnyPrefix(line, primary, secondary string) (rest string, ok bool) {
	//: Primary prefix takes precedence.
	if after, found := strings.CutPrefix(line, primary); found {
		//: Stripped via the primary prefix.
		return after, true
	}
	//: Otherwise try the secondary prefix.
	return strings.CutPrefix(line, secondary)
}

// newPathFrom extracts the post-edit path from a "+++ " header value. The bool
// is false for a deletion ("+++ /dev/null"), pairing the empty string so it is
// never an ambiguous lone-string sentinel.
func newPathFrom(raw string) (path string, isFile bool) {
	raw = trimHeaderTab(raw)
	//: /dev/null on the +side marks a deletion — no post-edit path.
	if raw == "/dev/null" {
		//: Deletion has no target path.
		return "", false
	}
	//: Strip the conventional "b/" prefix git prepends to the new path.
	return strings.TrimPrefix(raw, "b/"), true
}

// oldPathFrom extracts the pre-edit path from a "--- " header value, returning
// the fallback for a new file ("--- /dev/null").
func oldPathFrom(raw, fallback string) string {
	raw = trimHeaderTab(raw)
	//: /dev/null on the -side marks a new file — keep any rename-from fallback.
	if raw == "/dev/null" {
		//: No pre-edit path for a new file.
		return fallback
	}
	//: Strip the conventional "a/" prefix git prepends to the old path.
	return strings.TrimPrefix(raw, "a/")
}

// trimHeaderTab strips the trailing TAB git appends to a "---"/"+++" header
// path that contains a space ("+++ b/with space.go\t"), the traditional-diff
// delimiter for the optional timestamp field. Without this strip the path keeps
// a trailing TAB, so the caller's filter sees a name no test matches and the
// spaced filename silently loses its line ranges.
func trimHeaderTab(raw string) string {
	//: The TAB is a header delimiter, never part of the recorded path bytes
	//: (a real tab in a filename forces a c-quoted header instead).
	return strings.TrimSuffix(raw, "\t")
}

// parseNameStatus folds a `git diff --name-status -z` payload into set as
// file/directory membership (no line ranges). The -z wire format is NUL-separated
// and never c-quoted, so paths carrying spaces, tabs, newlines, or non-ASCII
// bytes arrive verbatim — this pass is the authoritative changed-file list and
// guarantees a file whose unified-diff header the parser cannot resolve
// (residual c-quoting) is still recorded as file-touched, never silently
// dropped. Records are `<status>NUL<path>NUL`, with renames/copies carrying two
// paths: `R100NUL<old>NUL<new>NUL`.
func parseNameStatus(payload, root string, set *ChangedSetValue, include IncludeFunc) {
	//: Index-based walk (not SplitSeq): rename records need two-token lookahead.
	tokens := strings.Split(payload, "\x00")
	//: Advance record by record; each record is 2 or 3 tokens wide.
	for i := 0; i < len(tokens); {
		status := tokens[i]
		//: Skip the empty token left by the trailing NUL terminator.
		if status == "" {
			i++
			continue
		}
		//: Renames (R###) and copies (C###) carry two paths: old then new.
		if status[0] == 'R' || status[0] == 'C' {
			//: A truncated two-path record cannot be attributed — stop rather
			//: than misalign the remaining stream.
			if i+nameStatusNewPathOffset >= len(tokens) {
				//: Stop parsing instead of misreading the remaining tokens.
				return
			}
			//: The old side keeps its directory touched (mirrors flushFile).
			markPath(set, tokens[i+1], root, include)
			//: The new path is file- and directory-touched.
			markPath(set, tokens[i+nameStatusNewPathOffset], root, include)
			i += nameStatusRenameWidth
			continue
		}
		//: Single-path record (A/M/D/T/U): one path follows the status.
		if i+1 >= len(tokens) {
			//: Truncated single-path record — stop rather than misalign.
			return
		}
		markPath(set, tokens[i+1], root, include)
		i += nameStatusSingleWidth
	}
}

// parseHunkPlusRange parses the "+"-side of a unified-diff hunk header
// ("@@ -a,b +c,d @@") into an inclusive 1-based range. ok is false when the
// header is malformed or the +side adds zero lines (a pure deletion hunk).
func parseHunkPlusRange(line string) (rng corevcs.LineRangeValue, ok bool) {
	fields := strings.Fields(line)
	//: A well-formed header has at least "@@", "-a,b", "+c,d".
	if len(fields) < minHunkFields {
		//: Malformed header — record nothing.
		return corevcs.LineRangeValue{}, false
	}
	plus, found := plusToken(fields)
	//: A missing +token is unparseable.
	if !found {
		//: No +side token found.
		return corevcs.LineRangeValue{}, false
	}
	start, count, parsed := parseStartCount(plus)
	//: Unparseable, zero-line (+c,0), or sub-1 start (malformed; git always
	//: emits a 1-based +side, e.g. a new file is "@@ -0,0 +1,N @@") add no
	//: usable 1-based changed range.
	if !parsed || count == 0 || start < 1 {
		//: Record nothing for a pure deletion / malformed hunk.
		return corevcs.LineRangeValue{}, false
	}
	//: Inclusive end is start + count - 1.
	return corevcs.LineRangeValue{Start: start, End: start + count - 1}, true
}

// plusToken returns the "+"-side token of a hunk header (with the '+' stripped),
// reporting whether one was found. Per unified-diff grammar the range region is
// "@@ -a,b +c,d @@"; the scan is restricted to the tokens between the two "@@"
// sentinels so a function-context annotation after the closing "@@" that
// happens to start with '+' can never be picked up as a range token.
func plusToken(fields []string) (token string, ok bool) {
	//: Locate the +side token after the opening "@@" sentinel (defensive
	//: against unusual spacing inside the range region).
	for _, f := range fields[1:] {
		//: The closing "@@" sentinel ends the range region; anything after it
		//: is function context and must never be read as a +side token.
		if f == "@@" {
			break
		}
		//: The +side token begins with '+'.
		if after, found := strings.CutPrefix(f, "+"); found {
			//: Return the stripped +side token.
			return after, true
		}
	}
	//: No +side token present in the range region.
	return "", false
}

// parseStartCount splits a unified-diff side token "c,d" (or "c") into its start
// line and line count. A missing count defaults to 1 per diff grammar. ok is
// false when either number fails to parse.
func parseStartCount(token string) (start, count int, ok bool) {
	startStr, countStr, hasComma := strings.Cut(token, ",")
	start, err := strconv.Atoi(startStr)
	//: A non-numeric start makes the whole token unusable.
	if err != nil {
		//: Signal parse failure to the caller.
		return 0, 0, false
	}
	//: A bare "c" (no comma) means a single line.
	if !hasComma {
		//: Default count is 1.
		return start, 1, true
	}
	count, err = strconv.Atoi(countStr)
	//: A non-numeric count makes the token unusable.
	if err != nil {
		//: Signal parse failure to the caller.
		return 0, 0, false
	}
	//: Return the explicit start and count.
	return start, count, true
}
