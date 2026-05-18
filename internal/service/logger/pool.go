// Package logger — declares the recycled event-record bucket consumed
// by the Builder API. Pulling RecordEvent values from a buffer.Recycler
// keeps the hot path allocation-free in steady state — combined with the
// kind-discriminated Value union the per-call cost is dominated by the
// encoder + sink rather than GC pressure.
package logger

import (
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/buffer"
)

// initialAttrCap is the starting capacity reserved for an event's attribute
// slice; chosen to comfortably hold the median log call's attrs without a
// re-allocation while keeping idle pool memory bounded.
const initialAttrCap int = 8

// recordPool recycles *chainBuilder values across Log calls. The factory
// returns a fresh chainBuilder with a pre-allocated attrs slice so the
// steady-state cost of Build() is zero heap allocations.
var recordPool = buffer.NewRecycler[*chainBuilder](newChainBuilder)

// newChainBuilder returns a fresh *chainBuilder with a pre-allocated attrs
// slice.
func newChainBuilder() *chainBuilder {
	//: pre-allocate the attrs slice to skip the first append's growth.
	return &chainBuilder{attrs: make([]corelogger.AttrValue, 0, initialAttrCap)}
}
