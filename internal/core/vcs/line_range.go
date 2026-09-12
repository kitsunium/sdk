// Package vcs — the LineRangeValue value type: an inclusive run of changed lines.
package vcs

// LineRangeValue is an inclusive, 1-based range of source lines taken from a
// diff hunk's "+"-side, i.e. post-edit line numbers.
//
// A whole-file change — a new or untracked file — is represented by a range
// starting at 1 and ending at a synthetic upper bound, so a membership test
// matches any line without the parser needing to know the file's true length.
type LineRangeValue struct {
	// Start is the first changed line, inclusive and 1-based.
	Start int
	// End is the last changed line, inclusive.
	End int
}

// Contains reports whether line falls inside the range. Both bounds are
// inclusive, which is what makes a single-line hunk (Start == End) match.
func (r LineRangeValue) Contains(line int) bool {
	//: inclusive on both ends — a one-line hunk has Start == End.
	return line >= r.Start && line <= r.End
}
