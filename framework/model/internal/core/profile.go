// A CPU or heap profile folded onto the nodes, and the goroutines grouped.

package core

// Profile kinds.
const (
	ProfileCPU  = "cpu"  // where CPU time went, sampled for a duration
	ProfileHeap = "heap" // where the live heap was allocated
)
