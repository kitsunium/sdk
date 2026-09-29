// A CPU or heap profile folded onto the nodes, and the goroutines grouped.

package core

import "time"

// Profile kinds.
const (
	ProfileCPU  = "cpu"  // where CPU time went, sampled for a duration
	ProfileHeap = "heap" // where the live heap was allocated
)

// ProfileResult is a pprof profile of the running product, folded onto its graph:
// every sample is attributed to the node whose code was running — through
// the pprof label kit puts on every handler, subscription, job and loop run,
// or, for the heap, which carries no label, through the node's code functions
// on the sample's stack.
type ProfileResult struct {
	// Kind is one of the Profile constants.
	Kind string `json:"kind"`
	// At is when the profile ended.
	At time.Time `json:"at"`
	// DurationMs is how long a CPU profile sampled.
	DurationMs float64 `json:"durationMs,omitempty"`
	// Unit is what the values count: "ms" of CPU, "bytes" of heap.
	Unit string `json:"unit"`
	// Total is the sum of every sample.
	Total float64 `json:"total"`
	// Nodes are the costs attributed to nodes, highest first.
	Nodes []NodeCostMessage `json:"nodes"`
	// Unattributed is the cost outside any node's code: the runtime, the
	// garbage collector, kit itself, idle loops.
	Unattributed float64 `json:"unattributed"`
	// Top are the costliest functions, by flat cost.
	Top []FuncCostMessage `json:"top"`
	// Flame is the call tree, root first, pruned below 0.5% of Total.
	Flame *FlameNodeMessage `json:"flame,omitempty"`
}

// NodeCostMessage is what one node cost.
// It is the share of the profile's samples spent in the node's code.
type NodeCostMessage struct {
	// Node is the node ID.
	Node string `json:"node"`
	// Value is the cost, in the profile's unit.
	Value float64 `json:"value"`
	// Share is Value over Total, from 0 to 1.
	Share float64 `json:"share"`
	// Top are the node's costliest functions.
	Top []FuncCostMessage `json:"top,omitempty"`
}

// FuncCostMessage is what one function cost.
// It carries the flat and cumulative samples of the function and where it
// lives.
type FuncCostMessage struct {
	// Func is the qualified function name.
	Func string `json:"func"`
	// Source locates it, when it is in the product's module.
	Source *SourceMessage `json:"source,omitempty"`
	// Flat is the cost of the function's own code; Cum includes its callees.
	Flat float64 `json:"flat"`
	Cum  float64 `json:"cum"`
	// Node is the node it was attributed to, when one.
	Node string `json:"node,omitempty"`
}

// FlameNodeMessage is one frame of a flame graph.
// Its children are the frames it called; its value is the samples under it.
type FlameNodeMessage struct {
	// Name is the function.
	Name string `json:"name"`
	// Value is its cumulative cost.
	Value float64 `json:"value"`
	// Node is the node the frame belongs to, when it is a node's code.
	Node string `json:"node,omitempty"`
	// Children are its callees, highest first.
	Children []*FlameNodeMessage `json:"children,omitempty"`
}

// GoroutinesResult is every goroutine of the process, grouped by what it runs.
// Each group names the node its stack runs in, when one does.
type GoroutinesResult struct {
	// At is when they were counted.
	At time.Time `json:"at"`
	// Total is how many goroutines exist.
	Total int `json:"total"`
	// Groups are goroutines sharing a node or loop, a state and a top frame,
	// largest first.
	Groups []GoroutineGroup `json:"groups"`
}

// GoroutineGroup is goroutines doing the same thing.
// They share one stack; Count says how many run it.
type GoroutineGroup struct {
	// Node is the node they run for, from their pprof label, when any.
	Node string `json:"node,omitempty"`
	// Loop is the loop they are, when kit knows: "http accept", a consumer.
	Loop string `json:"loop,omitempty"`
	// State is the runtime's word for what they wait on: "running",
	// "select", "chan receive", "IO wait", "sleep", "semacquire"…
	State string `json:"state"`
	// Count is how many goroutines are in the group.
	Count int `json:"count"`
	// Top is the innermost frame that is not the runtime's.
	Top string `json:"top"`
	// Stack is the group's stack, innermost first, capped.
	Stack []string `json:"stack,omitempty"`
}
