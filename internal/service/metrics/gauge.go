// Package metrics — atomic float64 gauge.
package metrics

import (
	"math"
	"sync/atomic"
)

// memGauge is an instantaneous float64 value stored as IEEE-754 bits in an
// atomic.Uint64 (lock-free Set/Add via compare-and-swap).
//
// It carries no temporality flag and no delta bookkeeping, and that is the OTel
// data model rather than an omission: a gauge is a SAMPLED READING, so there is
// no window for it to cover and OTLP's Gauge message has no temporality field.
// An observable gauge writes through the same Set a synchronous one does.
type memGauge struct {
	bits atomic.Uint64
}

// Set replaces the gauge value.
func (g *memGauge) Set(value float64) {
	//: store the float's bit pattern atomically.
	g.bits.Store(math.Float64bits(value))
}

// Add adjusts the gauge by delta via a compare-and-swap retry loop.
func (g *memGauge) Add(delta float64) {
	//: CAS until our read-modify-write wins against concurrent writers.
	for {
		//: read the current bits + decode.
		old := g.bits.Load()
		//: compute the new bit pattern.
		next := math.Float64bits(math.Float64frombits(old) + delta)
		//: publish iff no concurrent writer changed it.
		if g.bits.CompareAndSwap(old, next) {
			//: our update won.
			return
		}
	}
}

// load reads the current value for Collect.
func (g *memGauge) load() float64 {
	//: decode the atomic bit pattern.
	return math.Float64frombits(g.bits.Load())
}

// newMemGauge builds a zeroed gauge. A package-level function value so the
// Meter's create path passes it without allocating a closure.
func newMemGauge() *memGauge {
	//: a gauge starts at zero and needs no configuration.
	return &memGauge{}
}
