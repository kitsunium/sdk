package git

// contains is LineRangeValue.Contains's body: decl_gen.go writes LineRangeValue.Contains, from the
// design, as one call of it.
func (r LineRangeValue) contains(line int) bool {
	//: inclusive on both ends — a one-line hunk has Start == End.
	return line >= r.Start && line <= r.End
}
