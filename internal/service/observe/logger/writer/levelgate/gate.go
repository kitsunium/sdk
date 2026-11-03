package levelgate

import (
	"context"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
)

// gateSink forwards Write to inner only when the record meets the floor.
type gateSink struct {
	// inner is the wrapped terminal/middleware sink.
	inner corelogger.Sink
	// floor is the inclusive severity floor, read on every record so a live
	// level.Var moves the gate (NewLeveled, #1015); New and Floor hold a
	// level.Fixed.
	floor level.Leveler
}

// New wraps inner so only records with Level >= min reach it. When min equals
// level.Info — the zero value of a writer config's MinLevel — New returns inner
// unwrapped: the writer then inherits the handler-global level with no extra
// gate and no per-record overhead.
//
// IFACE-PLUGIN: the gate is returned behind the Sink interface; the concrete
// gateSink type stays unexported.
func New(inner corelogger.Sink, min level.Level) corelogger.Sink {
	//: Info is the config zero value — treat it as "inherit", skip the wrapper.
	if min == level.Info {
		//: hand back the sink untouched so the hot path stays flat.
		return inner
	}
	//: a real floor was requested — install the gate.
	return &gateSink{inner: inner, floor: level.Fixed(min)}
}

// Floor wraps inner so only records with Level >= min reach it, for every min —
// Info included. It is the gate without New's reading of Info as "inherit":
// that reading belongs to a writer configuration, whose zero MinLevel must not
// narrow anything, and a caller who names a floor outside one means the floor
// it named. A nil inner yields nil, which a fan-out skips and NewWithSink
// refuses, rather than a gate that fails on its first record.
//
// IFACE-PLUGIN: the gate is returned behind the Sink interface; the concrete
// gateSink type stays unexported.
func Floor(inner corelogger.Sink, min level.Level) corelogger.Sink {
	//: a gate over nothing is nothing, not a panic deferred to the first write.
	if inner == nil {
		//: nil: skipped by a fan-out, refused by NewWithSink.
		return nil
	}
	//: the gate, whatever the floor.
	return &gateSink{inner: inner, floor: level.Fixed(min)}
}

// NewLeveled wraps inner so only records with Level >= floor.Level() reach
// it, the floor read on every record: a level.Var retuned with Set moves the
// gate on the next record. A nil inner yields nil, as Floor's does; a nil
// floor gates at level.Info.
//
// IFACE-PLUGIN: the gate is returned behind the Sink interface; the concrete
// gateSink type stays unexported.
func NewLeveled(inner corelogger.Sink, floor level.Leveler) corelogger.Sink {
	//: a gate over nothing is nothing, not a panic deferred to the first write.
	if inner == nil {
		//: nil: skipped by a fan-out, refused by NewWithSink.
		return nil
	}
	//: a nil floor is the zero MinLevel's.
	if floor == nil {
		floor = level.Fixed(level.Info)
	}
	//: the live gate.
	return &gateSink{inner: inner, floor: floor}
}

// Write forwards records at or above the floor and silently drops the rest,
// reporting a successful (len(p), nil) for drops so fan-out callers never see a
// spurious failure.
func (s *gateSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: below-floor records are dropped as a successful no-op.
	if r.Level < s.floor.Level() {
		//: report the payload as accepted so multi never aggregates a failure.
		return len(p), nil
	}
	//: at or above the floor — delegate to the wrapped sink.
	return s.inner.Write(ctx, r, p)
}

// WriteBatch forwards the records at or above the floor and drops the rest,
// counting a dropped record as delivered, as Write reports a drop (#807). Each
// run of consecutive kept records goes to the inner sink as one sub-slice —
// one WriteBatch when the inner sink is a corelogger.BatchSink, one Write per
// record otherwise — so the gate allocates nothing and keeps the order.
func (s *gateSink) WriteBatch(ctx context.Context, recs []corelogger.RecordEvent, payloads [][]byte) (written int, err error) {
	//: one read of a live floor for the whole batch.
	floor := s.floor.Level()
	start := 0
	//: walk the batch, cutting it at every dropped record.
	for i := range recs {
		//: a kept record extends the current run.
		if recs[i].Level >= floor {
			continue
		}
		//: forward the run before the dropped record.
		if n, ferr := s.forwardRun(ctx, recs[start:i], payloads[start:i]); ferr != nil {
			//: the run's first n records landed; everything before it did too.
			return start + n, ferr
		}
		start = i + 1
	}
	//: the trailing run.
	if n, ferr := s.forwardRun(ctx, recs[start:], payloads[start:]); ferr != nil {
		//: the run's first n records landed.
		return start + n, ferr
	}
	//: every record was delivered or dropped by the floor.
	return len(recs), nil
}

// forwardRun hands a run of kept records to the inner sink and reports how
// many of them, counted from the first, it delivered.
func (s *gateSink) forwardRun(ctx context.Context, recs []corelogger.RecordEvent, payloads [][]byte) (written int, err error) {
	//: nothing kept between two drops.
	if len(recs) == 0 {
		return 0, nil
	}
	//: one call for the run when the inner sink takes one.
	if bs, ok := s.inner.(corelogger.BatchSink); ok {
		return bs.WriteBatch(ctx, recs, payloads)
	}
	//: record by record otherwise.
	for i := range recs {
		//: the first failure ends the run.
		if _, werr := s.inner.Write(ctx, recs[i], payloads[i]); werr != nil {
			return i, werr
		}
	}
	//: the whole run.
	return len(recs), nil
}

// Flush delegates to the wrapped sink.
func (s *gateSink) Flush(ctx context.Context) error {
	//: the gate buffers nothing; forward the flush verbatim.
	return s.inner.Flush(ctx)
}

// Close delegates to the wrapped sink.
func (s *gateSink) Close() error {
	//: the gate owns no resources; forward the close verbatim.
	return s.inner.Close()
}
