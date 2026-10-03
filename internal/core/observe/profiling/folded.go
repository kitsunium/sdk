// Package profiling — a profile folded onto owners: what each owner and each
// function cost, and the flame graph, in the sample type's own unit. Folding
// is the engine's, internal/service/observe/profiling.Fold.
package profiling

// FlameRoot is the name of a flame graph's root frame, which stands for the
// whole profile.
const FlameRoot string = "root"

// FoldedValue is a profile folded: in the sample type's own unit —
// nanoseconds of CPU, bytes of heap — never rounded.
type FoldedValue struct {
	// Flame is the flame graph, rooted at [FlameRoot], its children the
	// outermost frames; nil when Total is zero.
	Flame *FlameNodeValue
	// Owners holds each owner's cost, the costliest first.
	Owners []OwnerCostValue
	// Top holds the costliest functions: by flat cost, then cumulative, then
	// name.
	Top []FunctionCostValue
	// SampleType is what was folded.
	SampleType SampleTypeValue
	// Total is the sum of every sample's value.
	Total int64
	// Unattributed is the part of Total charged to nobody. Total is exactly
	// Unattributed plus every owner's Value.
	Unattributed int64
}

// OwnerCostValue is what one owner cost: the sum of the samples charged to
// it, and its own costliest functions.
type OwnerCostValue struct {
	// Owner is the name Attribute returned.
	Owner string
	// Top holds the owner's costliest functions.
	Top []FunctionCostValue
	// Value is the sum of the samples charged to the owner.
	Value int64
}

// FunctionCostValue is what one function cost, flat and cumulative, and where
// it is defined.
type FunctionCostValue struct {
	// Function is the function's name, as the runtime spells it.
	Function string
	// File and StartLine say where it is defined, when the profile knows.
	File string
	// Flat is the cost of samples whose innermost frame is the function.
	Flat int64
	// Cum is the cost of samples with the function anywhere on the stack,
	// each sample counted once however many times the function recurses.
	Cum int64
	// StartLine is the function's first line; zero when unknown.
	StartLine int
}

// FlameNodeValue is one frame of a flame graph: a function reached along one
// path, and what the samples through it cost.
type FlameNodeValue struct {
	// Name is the function's name; [FlameRoot] for the root.
	Name string
	// Children are the functions it called, costliest first.
	Children []*FlameNodeValue
	// Value is the cost of every sample through this path.
	Value int64
}
