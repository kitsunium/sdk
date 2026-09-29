// A CPU or heap profile folded onto the nodes, and the goroutines grouped.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// Profile kinds.
const (
	ProfileCPU  string = core.ProfileCPU  // where CPU time went, sampled for a duration
	ProfileHeap string = core.ProfileHeap // where the live heap was allocated
)

type (
	// Profile is a pprof profile of the running product, folded onto its graph:
	// every sample is attributed to the node whose code was running — through
	// the pprof label kit puts on every handler, subscription, job and loop run,
	// or, for the heap, which carries no label, through the node's code functions
	// on the sample's stack.
	Profile = core.ProfileResult
)

type (
	// NodeCost is what one node cost.
	// It is the share of the profile's samples spent in the node's code.
	NodeCost = core.NodeCostMessage
)

type (
	// FuncCost is what one function cost.
	// It carries the flat and cumulative samples of the function and where it
	// lives.
	FuncCost = core.FuncCostMessage
)

type (
	// FlameNode is one frame of a flame graph.
	// Its children are the frames it called; its value is the samples under it.
	FlameNode = core.FlameNodeMessage
)

type (
	// Goroutines is every goroutine of the process, grouped by what it runs.
	// Each group names the node its stack runs in, when one does.
	Goroutines = core.GoroutinesResult
)

type (
	// GoroutineGroup is goroutines doing the same thing.
	// They share one stack; Count says how many run it.
	GoroutineGroup = core.GoroutineGroup
)
