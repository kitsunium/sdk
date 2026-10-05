package proc

const (
	// MemorySourceOperator reports that GOMEMLIMIT carried a non-empty value.
	// The Go runtime has already applied it and the derivation stands aside:
	// overriding an operator's explicit decision would contradict them silently.
	//
	// The run starts at iota+1 so the zero stays unminted — see the type comment.
	MemorySourceOperator MemorySource = iota + 1
	// MemorySourceUnconstrained reports that no control group governing this
	// process declares a memory cap: not containerised, capped at "max", or not
	// Linux at all. There is nothing to derive a limit from.
	MemorySourceUnconstrained
	// MemorySourceBelowFloor reports that a cap was found but the limit derived
	// from it fell under the usable floor. Holding the runtime beneath it would
	// spin the collector continuously without averting the kill, so the
	// derivation declines rather than applying a limit it expects to hurt.
	MemorySourceBelowFloor
	// MemorySourceCgroup reports the only outcome that yields a limit: a real
	// cap was read from the control-group hierarchy and the derived value is
	// usable.
	MemorySourceCgroup
)

// String renders the source for a log line. A value this package never mints —
// including the zero — renders as "unknown" rather than as a number, because it
// is a defect worth reading as one.
func (s MemorySource) String() string {
	//: a closed set of four, plus the honest answer for anything else.
	switch s {
	//: the operator spoke first.
	case MemorySourceOperator:
		//: GOMEMLIMIT was already set.
		return "operator"
	//: no cgroup declares a ceiling.
	case MemorySourceUnconstrained:
		//: nothing bounds this process.
		return "unconstrained"
	//: a ceiling exists but is too tight to honour usefully.
	case MemorySourceBelowFloor:
		//: the derived limit was discarded.
		return "below-floor"
	//: the cgroup allowance decided it.
	case MemorySourceCgroup:
		//: derived and applied.
		return "cgroup"
	//: a value this package never mints, including the zero.
	default:
		//: name the absence rather than invent a source.
		return "unknown"
	}
}

// applied is MemoryLimitValue.Applied's body: decl_gen.go writes MemoryLimitValue.Applied, from the
// design, as one call of it.
func (m MemoryLimitValue) applied() bool {
	//: MemorySourceCgroup is the only outcome that installs anything.
	return m.Source == MemorySourceCgroup
}
