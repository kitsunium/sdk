// Package level — Var, the atomic Level holder behind the Leveler port.
package level

import "sync/atomic"

// Var is an atomically mutable Level holder satisfying Leveler. Its zero value
// is ready to use and reports Info (the Level zero value), so a freshly
// declared Var gates at the same default as a config's unset MinLevel. Set and
// Level are safe for concurrent use; a gate may read Level on the hot path
// while a control goroutine raises or lowers the floor via Set.
type Var struct {
	// v stores the Level as an int32 so the holder is lock-free; Level is an
	// int8, which round-trips through int32 without loss.
	v atomic.Int32
}

// NewVar returns a Var initialised to initial. The returned pointer is the
// Leveler a gate or sink holds; callers later retune it with Set.
func NewVar(initial Level) *Var {
	//: heap-allocate so the atomic holder has a stable address for sharing.
	lv := &Var{}
	//: seed the initial threshold before the holder is observed.
	lv.v.Store(int32(initial))
	//: hand back the seeded holder ready for concurrent Set/Level.
	return lv
}

// Set replaces the current threshold with lvl. The store is atomic, so a
// concurrent Level caller observes either the old or the new value, never a
// torn read.
func (v *Var) Set(lvl Level) {
	//: publish the new floor atomically for concurrent readers.
	v.v.Store(int32(lvl))
}

// Level reports the current threshold. The load is atomic and lock-free,
// keeping it cheap enough for a per-record gate to call on the hot path.
func (v *Var) Level() Level {
	//: read the current floor atomically and narrow back to the Level type.
	return Level(int8(v.v.Load()))
}
