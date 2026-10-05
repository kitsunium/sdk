package logger

import (
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
)

// initialAttrCap is the starting capacity reserved for an event's attribute
// slice; chosen to comfortably hold the median log call's attrs without a
// re-allocation while keeping idle pool memory bounded.
const initialAttrCap int = 8

// recordPool recycles *chainBuilder values across Log calls. The factory
// returns a fresh chainBuilder with a pre-allocated attrs slice, so Build()
// itself allocates nothing in steady state — the one allocation per emit
// comes later, when the handler clones the attrs on Send.
var recordPool = recycler.NewPool[*chainBuilder](newChainBuilder)

// newChainBuilder returns a fresh *chainBuilder with a pre-allocated attrs
// slice.
func newChainBuilder() *chainBuilder {
	//: pre-allocate the attrs slice to skip the first append's growth.
	return &chainBuilder{attrs: make([]corelogger.AttrValue, 0, initialAttrCap)}
}
