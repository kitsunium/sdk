package proc

// MemorySource names what decided the Go soft memory limit, so a caller that
// sees no limit applied can tell WHY without re-deriving it.
//
// The zero value is deliberately unclaimed. A MemorySource is produced by a
// derivation, never chosen by a caller, so a zero reaching a log line is a value
// this package never minted rather than one of the four real outcomes wearing a
// default.
type MemorySource uint8

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

// MemoryBound names which control-group file bound a soft-memory-limit
// derivation, so a log line says whether the throttle (memory.high) or the hard
// cap (memory.max) set the allowance.
//
// The zero value is deliberately unclaimed, as MemorySource's is: it is the
// Bound of every derivation that read no allowance.
type MemoryBound uint8

const (
	// MemoryBoundHigh reports that a memory.high file bound the derivation: the
	// throttle above which the kernel reclaims and slows the group's allocations,
	// long before memory.max would kill it.
	//
	// The run starts at iota+1 so the zero stays unminted — see the type comment.
	MemoryBoundHigh MemoryBound = iota + 1
	// MemoryBoundMax reports that a hard cap bound the derivation: a cgroup v2
	// memory.max file, or cgroup v1's memory.limit_in_bytes — v1 has no throttle
	// to read beside it.
	MemoryBoundMax
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

// String renders the bound for a log line: "high", "max", or "unknown" for a
// value this package never mints, the zero included.
func (b MemoryBound) String() string {
	//: a closed set of two, plus the honest answer for anything else.
	switch b {
	//: the throttle.
	case MemoryBoundHigh:
		//: memory.high.
		return "high"
	//: the hard cap.
	case MemoryBoundMax:
		//: memory.max or memory.limit_in_bytes.
		return "max"
	//: a value this package never mints, including the zero.
	default:
		//: name the absence rather than invent a bound.
		return "unknown"
	}
}

// MemoryLimitValue is an immutable record of one soft-memory-limit derivation:
// the cgroup allowance it read, the limit it derived, and what decided the
// outcome.
//
// Allowance and Limit are zero on every outcome except MemorySourceCgroup, with
// one exception: MemorySourceBelowFloor carries the Allowance it read, because
// the reason the limit was discarded is only legible next to the cap that
// produced it.
type MemoryLimitValue struct {
	// Allowance is the tightest cgroup memory cap governing this process, in
	// bytes, or zero when none was read.
	Allowance int64
	// Limit is the Go soft memory limit derived from Allowance, in bytes, or
	// zero when no limit was applied.
	Limit int64
	// Source names what decided the outcome.
	Source MemorySource
	// Bound names the file that set Allowance — memory.high or the hard cap —
	// on every outcome that carries an Allowance, and is zero otherwise.
	Bound MemoryBound
}

// Applied reports whether the derivation installed a limit. It is true for
// exactly one source, and it is the predicate a caller wants rather than a
// comparison against Limit — a zero Limit and "no limit applied" are the same
// state today, and tying call sites to that coincidence would be fragile.
func (m MemoryLimitValue) Applied() bool {
	//: MemorySourceCgroup is the only outcome that installs anything.
	return m.Source == MemorySourceCgroup
}
