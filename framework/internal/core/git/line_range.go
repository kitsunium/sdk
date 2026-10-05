package git

// Contains reports whether line falls inside the range. Both bounds are
// inclusive, which is what makes a single-line hunk (Start == End) match.
func (r LineRangeValue) Contains(line int) bool {
	//: inclusive on both ends — a one-line hunk has Start == End.
	return line >= r.Start && line <= r.End
}
