// Package metrics — the per-name kind binding.
package metrics

import (
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// Test_memMeter_bindName pins the name-reuse guard, and why it panics rather
// than returning an error.
//
// Fetching an instrument is not a fallible operation in the port's shape —
// Counter(name) returns a Counter, full stop — so there is nowhere to put an
// error. And the mistake it catches is always a programming one: the same metric
// name bound to two kinds means one of the two call sites is wrong, and every
// value it records is landing in the wrong instrument. A panic in development is
// the cheapest possible way to find that; silently returning the existing
// instrument of the wrong kind would be the most expensive.
//
// The kind is tracked per NAME, not per series: labels vary within one metric,
// its kind does not.
func Test_memMeter_bindName(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the kind the name is already bound to, if any.
		bind *instrumentKind
		//: the kind being requested now.
		want      instrumentKind
		wantPanic bool
	}
	counter, gauge, histogram := kindCounter, kindGauge, kindHistogram

	tests := []tc{
		{name: "an unbound name as a counter", want: kindCounter},
		{name: "an unbound name as a gauge", want: kindGauge},
		{name: "an unbound name as a histogram", want: kindHistogram},
		{name: "a counter fetched again", bind: &counter, want: kindCounter},
		{name: "a gauge fetched again", bind: &gauge, want: kindGauge},
		{name: "a histogram fetched again", bind: &histogram, want: kindHistogram},
		{name: "a counter fetched as a gauge", bind: &counter, want: kindGauge, wantPanic: true},
		{name: "a counter fetched as a histogram", bind: &counter, want: kindHistogram, wantPanic: true},
		{name: "a gauge fetched as a counter", bind: &gauge, want: kindCounter, wantPanic: true},
		{name: "a gauge fetched as a histogram", bind: &gauge, want: kindHistogram, wantPanic: true},
		{name: "a histogram fetched as a counter", bind: &histogram, want: kindCounter, wantPanic: true},
		{name: "a histogram fetched as a gauge", bind: &histogram, want: kindGauge, wantPanic: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		meter, ok := NewMeter().(*memMeter)
		if !ok {
			t.Fatal("NewMeter did not return a memMeter")
		}
		const metric string = "requests"
		if c.bind != nil {
			meter.names[metric] = &nameState{kind: *c.bind}
		}

		defer func() {
			r := recover()
			if !c.wantPanic {
				if r != nil {
					t.Errorf("bindName panicked: %v", r)
				}
				return
			}
			if r == nil {
				t.Fatal("a cross-kind reuse did not panic")
			}
			//: the panic names the typed conflict, so the message points at
			//: the contract rather than at a nil map access.
			msg, isString := r.(string)
			if !isString || msg != coremetrics.InstrumentKindConflict.Error() {
				t.Errorf("the panic value is %v, want the InstrumentKindConflict message", r)
			}
		}()

		state := meter.bindName(metric, c.want)
		//: a successful bind always hands back live bookkeeping — admit
		//: increments through it, so a nil here would be a nil dereference
		//: on the very next line of the create path.
		if state == nil {
			t.Fatal("bindName returned no state")
		}
		if state.kind != c.want {
			t.Errorf("the name is bound to kind %d, want %d", state.kind, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
