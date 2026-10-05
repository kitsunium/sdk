package level

// newVar is NewVar's body: decl_gen.go writes NewVar, from the
// design, as one call of it.
func newVar(initial Level) *Var {
	//: heap-allocate so the atomic holder has a stable address for sharing.
	lv := &Var{}
	//: seed the initial threshold before the holder is observed.
	lv.v.Store(int32(initial))
	//: hand back the seeded holder ready for concurrent Set/Level.
	return lv
}

// set is Var.Set's body: decl_gen.go writes Var.Set, from the
// design, as one call of it.
func (v *Var) set(lvl Level) {
	//: publish the new floor atomically for concurrent readers.
	v.v.Store(int32(lvl))
}

// level is Var.Level's body: decl_gen.go writes Var.Level, from the
// design, as one call of it.
func (v *Var) level() Level {
	//: read the current floor atomically and narrow back to the Level type.
	return Level(int8(v.v.Load()))
}
