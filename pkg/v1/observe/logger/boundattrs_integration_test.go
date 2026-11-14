//go:build !race

package logger_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// boundRuns is how many emits each arm performs inside the measured window.
const boundRuns int = 2000

// bytesOnlySink discards the bytes and says it reads nothing else
// (logger.AttrsRequester).
type bytesOnlySink struct{ discardSink }

func (bytesOnlySink) WantsAttrs() bool { return false }

// boundSet is n bound attributes of the service shape.
func boundSet(n int) []logger.Attr {
	attrs := make([]logger.Attr, n)
	for i := range attrs {
		attrs[i] = logger.String("bound_"+strconv.Itoa(i), "value-of-attribute-"+strconv.Itoa(i))
	}
	return attrs
}

// TestBoundAttrsCostNoAllocation (#1016) pins the exact allocation count of an
// emit as a function of what the sink reads and what the record carries: an
// emit with no attribute of its own allocates nothing, whatever the number of
// bound attributes — the handler hands the sink the bound array itself — and
// an emit to a Sink that reads only the bytes allocates nothing either, its
// own attributes neither merged nor copied. Only an emit with attributes of
// its own to a Sink that reads them pays the one copy.
//
// The counts are totals over boundRuns emits on the window's own stack
// (mallocsOver), race off: the race-off allocation lane runs this file
// (tools/alloc-lane-targets.txt names //pkg/v1/observe/logger:logger_test).
func TestBoundAttrsCostNoAllocation(t *testing.T) {
	type tc struct {
		name    string
		sink    logger.Sink
		bound   int
		own     []logger.Attr
		perEmit uint64
	}
	tests := []tc{
		{name: "32 bound, none of its own", sink: discardSink{}, bound: 32},
		{name: "8 bound, none of its own", sink: discardSink{}, bound: 8},
		{name: "32 bound and its own, to a sink that reads only the bytes", sink: bytesOnlySink{}, bound: 32, own: allocProbeAttrs},
		{name: "32 bound and its own, to a sink that reads them", sink: discardSink{}, bound: 32, own: allocProbeAttrs, perEmit: 1},
	}
	//: the runtime's lazily built caches are paid on Loggers no window measures.
	for _, s := range []logger.Sink{discardSink{}, bytesOnlySink{}} {
		warm, err := logger.NewWithSink(logger.SinkConfig{Sink: s, Encoder: logger.TextEncoder()})
		if err != nil {
			t.Fatalf("building the warm-up Logger: %v", err)
		}
		warm = warm.With(boundSet(8)...)
		for range allocRuntimeWarmup {
			logger.LogAttrs(t.Context(), warm, logger.LevelInfo, "warm", allocProbeAttrs)
			logger.LogAttrs(t.Context(), warm, logger.LevelInfo, "warm", nil)
		}
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		lg, err := logger.NewWithSink(logger.SinkConfig{Sink: c.sink, Encoder: logger.TextEncoder()})
		if err != nil {
			t.Fatalf("NewWithSink err = %v", err)
		}
		lg = lg.With(boundSet(c.bound)...)
		ctx := context.Background()
		got := mallocsOver(boundRuns, func() { logger.LogAttrs(ctx, lg, logger.LevelInfo, "request served", c.own) })
		reportWindow(t, "emits", got)
		//: an EXACT total: perEmit times boundRuns.
		if want := c.perEmit * uint64(boundRuns); got.own != want {
			t.Errorf("%s: %d emits performed %d allocations, want exactly %d (%d a call)", c.name, boundRuns, got.own, want, c.perEmit)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
