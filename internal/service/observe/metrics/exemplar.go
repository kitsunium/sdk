package metrics

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// setClocker is an instrument that stamps the time of what it keeps — an
// exemplar — on the meter's clock. The series store hands the clock over
// when it admits the series, before any reader can reach it.
type setClocker interface {
	setClock(clk clock.Clock)
}

// stampClock hands clk to inst when inst keeps exemplars.
func stampClock[T any](inst T, clk clock.Clock) T {
	//: only the instruments that keep exemplars read a clock.
	if c, ok := any(inst).(setClocker); ok {
		c.setClock(clk)
	}
	//: the instrument, stamped.
	return inst
}

// sampledSpan reads the span ctx carries and reports whether a measurement
// recorded under it may become an exemplar: a valid span whose sampled bit
// is set — an exemplar into a trace nobody stored is a dead link (ADR 0051
// §Deferred).
func sampledSpan(ctx context.Context) (span coretrace.SpanContextValue, ok bool) {
	span = coretrace.SpanContextFromContext(ctx)
	//: a valid, sampled span, nothing else.
	return span, span.IsValid() && span.IsSampled()
}

// exemplarReservoir keeps at most one exemplar per slot — a bucket of an
// explicit histogram, the one slot of a sum or an exponential histogram —
// the last measurement offered into it. It is allocated on the first
// sampled measurement, so a series nobody records exemplars on pays nothing.
type exemplarReservoir struct {
	// mu guards slots and held: an offer is four stores, a collection a copy.
	mu sync.Mutex
	// clk stamps each exemplar's time.
	clk clock.Clock
	// slots holds the exemplar of each slot; held says which are set.
	slots []coremetrics.ExemplarValue
	held  []bool
}

// exemplarSlots is one instrument's lazily allocated reservoir.
type exemplarSlots struct {
	// clk is the meter's clock, stamped at admission.
	clk clock.Clock
	// reservoir is nil until the first sampled measurement.
	reservoir atomic.Pointer[exemplarReservoir]
}

// setClock implements setClocker.
func (e *exemplarSlots) setClock(clk clock.Clock) {
	e.clk = clk
}

// offer keeps value, recorded under span, as the exemplar of slot, the
// reservoir sized size on its first use.
func (e *exemplarSlots) offer(size, slot int, value float64, span coretrace.SpanContextValue) {
	r := e.reservoir.Load()
	//: the first sampled measurement of the series allocates the reservoir.
	if r == nil {
		fresh := &exemplarReservoir{clk: e.clk, slots: make([]coremetrics.ExemplarValue, size), held: make([]bool, size)}
		//: the first offer to land publishes it; a racing one uses the winner's.
		if !e.reservoir.CompareAndSwap(nil, fresh) {
			fresh = e.reservoir.Load()
		}
		r = fresh
	}
	r.offer(slot, value, span)
}

// collect returns the exemplars held, in slot order, nil when none is; a
// delta collection empties the reservoir, a cumulative one keeps each
// exemplar until a newer measurement replaces it.
func (e *exemplarSlots) collect(delta bool) []coremetrics.ExemplarValue {
	r := e.reservoir.Load()
	//: no sampled measurement ever reached the series.
	if r == nil {
		return nil
	}
	//: the reservoir's own copy.
	return r.collect(delta)
}

// offer stores value as slot's exemplar, stamped now, with span's
// identifiers.
func (r *exemplarReservoir) offer(slot int, value float64, span coretrace.SpanContextValue) {
	now := r.now()
	r.mu.Lock()
	r.slots[slot] = coremetrics.ExemplarValue{Time: now, Value: value, TraceID: span.TraceID, SpanID: span.SpanID}
	r.held[slot] = true
	r.mu.Unlock()
}

// now is the reservoir clock's reading, the system clock's for an
// instrument built outside a meter.
func (r *exemplarReservoir) now() time.Time {
	//: a series admitted by a meter always carries its clock.
	if r.clk == nil {
		return clock.System.Now()
	}
	//: the meter's clock.
	return r.clk.Now()
}

// collect copies the exemplars held, in slot order, and empties the
// reservoir under delta.
func (r *exemplarReservoir) collect(delta bool) []coremetrics.ExemplarValue {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []coremetrics.ExemplarValue
	//: the held slots, in order.
	for i, held := range r.held {
		//: an empty slot keeps nothing.
		if !held {
			continue
		}
		out = append(out, r.slots[i])
		//: a delta window's exemplars are its own.
		if delta {
			r.held[i] = false
		}
	}
	//: nil when nothing was held.
	return out
}
