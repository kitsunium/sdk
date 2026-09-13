// Package git — the changed set: which files, line ranges and directories a
// branch touched. The concrete implementation of the core vcs.ChangedSet port.
package git

import (
	"path/filepath"
	"strings"

	corevcs "github.com/kitsunium/sdk/internal/core/vcs"
)

// wholeFileEnd is the synthetic upper bound used for a whole-file change (new
// or untracked file), so ContainsLine matches any line in such a file without
// the parser needing to know the file's true length.
const wholeFileEnd int = 1 << 30

// changedFilesHint pre-sizes the changed-set maps. A typical branch touches a
// handful of files; this avoids the first few rehashes without over-allocating.
const changedFilesHint int = 16

// ChangedSetValue holds the files, line ranges, and directories that a
// branch changed versus its merge-base. All paths are absolute and normalised
// through filepath.Clean so membership tests match the engine's resolved issue
// positions. Build it with NewChangedSetValue and the add* methods; query it
// with the Contains* methods.
type ChangedSetValue struct {
	// repoRoot is the absolute repository top-level, retained for diagnostics.
	repoRoot string
	// lineRangesByFile maps an absolute file path to its changed "+"-side line
	// ranges. Empty for a pure rename or a deletion (file touched, no lines).
	lineRangesByFile map[string][]corevcs.LineRangeValue
	// touchedFiles is the set of absolute file paths that appear in the diff.
	touchedFiles map[string]struct{}
	// touchedDirs is the set of absolute directories enclosing touched files,
	// the predicate for directory-scoped queries.
	touchedDirs map[string]struct{}
	// aliasRoot is a second spelling of repoRoot — the one the caller used to
	// reach the repository, when that path traverses a symbolic link git
	// resolved away. Empty when the caller's spelling already is git's. It is
	// read only while the set is being built: every entry is recorded under
	// both spellings, so no query pays for it.
	aliasRoot string
}

// NewChangedSetValue returns an empty changed-set rooted at the given absolute
// repository top-level.
func NewChangedSetValue(repoRoot string) *ChangedSetValue {
	//: No second spelling: every path queried is expected to be git's own.
	return newChangedSetValue(repoRoot, "")
}

// newChangedSetValue returns an empty changed-set rooted at git's absolute
// repository top-level, and recording aliasRoot as a second, equally valid
// spelling of it. An empty aliasRoot means the two spellings coincide.
func newChangedSetValue(repoRoot, aliasRoot string) *ChangedSetValue {
	//: Pre-size for a typical handful of changed files (a clean branch still
	//: yields an empty set — the hint only bounds early rehashing).
	return &ChangedSetValue{
		repoRoot:         repoRoot,
		lineRangesByFile: make(map[string][]corevcs.LineRangeValue, changedFilesHint),
		touchedFiles:     make(map[string]struct{}, changedFilesHint),
		touchedDirs:      make(map[string]struct{}, changedFilesHint),
		aliasRoot:        aliasRoot,
	}
}

// childPrefix returns the prefix every path strictly below dir carries.
//
// It is dir plus a separator, EXCEPT where dir already ends in one — which is
// what a filesystem root is, "/" on Unix and `C:\` on Windows. Appending there
// produces "//", a prefix no cleaned path carries, so a repository rooted at
// the filesystem root would mirror nothing at all. The separator cannot simply
// be dropped either: "/repo" without it also prefixes "/repo-other/a.go", which
// is a different tree.
func childPrefix(dir string) string {
	//: A directory that already ends in a separator is its own child prefix.
	if strings.HasSuffix(dir, separator) {
		//: Appending would make "//", which no cleaned path begins with.
		return dir
	}
	//: One separator turns a directory into the prefix of what it contains.
	return dir + separator
}

// spelledAs returns the path absPath carries under the caller's own spelling of
// the repository root, and whether there is one.
//
// Every path recorded here is built from git's canonical top-level, because
// that is what --show-toplevel answers. A caller that reached the repository
// through a symbolic link spells its own paths through that link, and
// filepath.Clean is lexical, so the two never meet: every query answers false,
// on a resolution that is neither degraded nor empty — a whole branch read as
// unchanged, which is the one answer this package must never give.
//
// The second spelling is recorded at BUILD time rather than resolved at query
// time, and that is the whole placement argument: the alias is known once, when
// the set is constructed, while queries are unbounded — a caller asks
// ContainsLine once per diagnostic. Paying here keeps the read path exactly
// what internal/service/vcs/git/BENCH.md measured: a map lookup, no branch on
// the alias, and zero allocations.
func (s *ChangedSetValue) spelledAs(absPath string) (aliased string, ok bool) {
	//: No second spelling was recorded — the caller's is git's own.
	if s.aliasRoot == "" {
		//: Nothing to mirror.
		return "", false
	}
	rest, under := strings.CutPrefix(absPath, childPrefix(s.repoRoot))
	//: A path outside the repository root has no spelling under the alias.
	if !under {
		//: Nothing to mirror.
		return "", false
	}
	//: The same file, named the way the caller names it.
	return filepath.Join(s.aliasRoot, rest), true
}

// addLineRange records a changed line range on absFile and marks the file and
// its directory as touched.
func (s *ChangedSetValue) addLineRange(absFile string, rng corevcs.LineRangeValue) {
	clean := filepath.Clean(absFile)
	s.lineRangesByFile[clean] = append(s.lineRangesByFile[clean], rng)
	//: The same range under the caller's own spelling of the root, so a query
	//: through the path they configured finds it without a per-query rewrite.
	if aliased, ok := s.spelledAs(clean); ok {
		s.lineRangesByFile[aliased] = append(s.lineRangesByFile[aliased], rng)
	}
	//: A file with line ranges is always also file- and package-touched.
	s.markTouched(clean)
}

// addWholeFile records a whole-file change (new / untracked file): every line
// is in the diff.
func (s *ChangedSetValue) addWholeFile(absFile string) {
	//: Represent the whole file as a single 1..wholeFileEnd range.
	s.addLineRange(absFile, corevcs.LineRangeValue{Start: 1, End: wholeFileEnd})
}

// markTouched records absFile and its directory as touched WITHOUT adding any
// line range — used for pure renames and deletions so file- and directory-scoped
// queries still answer while line-scoped ones do not.
func (s *ChangedSetValue) markTouched(absFile string) {
	clean := filepath.Clean(absFile)
	s.touchedFiles[clean] = struct{}{}
	s.touchedDirs[filepath.Dir(clean)] = struct{}{}
	//: Mirrored under the caller's spelling for the same reason as the ranges.
	if aliased, ok := s.spelledAs(clean); ok {
		s.touchedFiles[aliased] = struct{}{}
		s.touchedDirs[filepath.Dir(aliased)] = struct{}{}
	}
}

// ContainsLine reports whether line on absFile is within a changed hunk.
func (s *ChangedSetValue) ContainsLine(absFile string, line int) bool {
	ranges, ok := s.lineRangesByFile[filepath.Clean(absFile)]
	//: No recorded ranges → the line is outside the diff (or file untouched).
	if !ok {
		//: Absent file cannot contain a changed line.
		return false
	}
	//: Any inclusive range covering the line is a hit.
	for _, r := range ranges {
		//: Inclusive bounds on both ends.
		if line >= r.Start && line <= r.End {
			//: Line falls within a changed hunk.
			return true
		}
	}
	//: No range covered the line.
	return false
}

// ContainsFile reports whether absFile appears anywhere in the diff,
// including as the old side of a rename or a deletion.
func (s *ChangedSetValue) ContainsFile(absFile string) bool {
	_, ok := s.touchedFiles[filepath.Clean(absFile)]
	//: Membership in the touched-file set is the answer.
	return ok
}

// ContainsDir reports whether absDir directly encloses any touched file. It is
// NOT recursive: the parent of a touched directory is not itself touched.
func (s *ChangedSetValue) ContainsDir(absDir string) bool {
	_, ok := s.touchedDirs[filepath.Clean(absDir)]
	//: Membership in the touched-directory set is the answer.
	return ok
}

// IsEmpty reports whether the changed-set contains no touched files at all
// (clean branch, or a branch whose every change the caller's filter excluded).
func (s *ChangedSetValue) IsEmpty() bool {
	//: Touched files is the canonical population marker.
	return len(s.touchedFiles) == 0
}
