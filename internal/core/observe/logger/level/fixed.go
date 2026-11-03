package level

// Fixed is a constant Leveler: level.Fixed(l) is a floor that never moves,
// for a handler or a gate built from a plain Level that holds a Leveler. Its
// Level is one return of the value, which a handler's Enabled reads through
// the same interface call a live Var answers.
type Fixed Level

// Level reports the constant threshold.
func (f Fixed) Level() Level {
	//: the value is the threshold.
	return Level(f)
}
