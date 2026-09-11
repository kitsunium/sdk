// Package levelgate — what a record costs to be DROPPED.
//
// This gate runs on every record a writer is handed, including every one it
// discards, so its cost is paid by exactly the callers who configured it so
// that work would not happen. The headline row is therefore the drop, not the
// pass: `dropped` against `no gate at all` is the number a consumer who sets
// MinLevel is buying, and `passed` is what the same consumer pays on the
// records that survive.
//
// Everything here runs against a no-op inner sink. The console and file
// packages price the transports; this file prices the branch in front of them.
package levelgate

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// recordEventBytes is what core/logger.Sink copies onto every hop. It is
// derived rather than written down because BENCH.md §2 quotes it and a doc that
// drifts from the struct would be worse than no figure at all: the port takes
// RecordEvent BY VALUE, and this is how much of it travels for the one byte the
// gate reads.
const recordEventBytes = unsafe.Sizeof(corelogger.RecordEvent{})

// benchPayload is a realistic encoded line: the text encoder's output for a
// short message with two attributes lands in this range, so the drop path is
// measured carrying the bytes a real drop would carry rather than a token.
var benchPayload = []byte(
	`2026-09-10T20:15:11.482Z INFO framework_version=dev msg="request served" method=GET status=200` + "\n",
)

// benchSinkN keeps the byte count the benchmarks produce reachable from outside
// the loop so neither the call nor the sink beneath it can be eliminated.
var benchSinkN int

// noopSink is the terminal of every row here: it accepts the payload and does
// nothing else, so a row's ns/op is the gate plus one interface call and
// nothing that belongs to a transport.
type noopSink struct{}

func (noopSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	//: accept the whole payload with no work, so the row measures the caller.
	return len(p), nil
}
func (noopSink) Flush(_ context.Context) error { return nil }
func (noopSink) Close() error                  { return nil }

// passthroughSink is the gate with its branch removed: it delegates every
// record unconditionally. It exists so the `passed` row can be split into the
// comparison and the second hop, which turn out to cost very different amounts.
type passthroughSink struct {
	// inner is the wrapped sink, held exactly as gateSink holds it.
	inner corelogger.Sink
}

func (s *passthroughSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (int, error) {
	//: no comparison at all — the delegation and nothing else.
	return s.inner.Write(ctx, r, p)
}
func (s *passthroughSink) Flush(ctx context.Context) error { return s.inner.Flush(ctx) }
func (s *passthroughSink) Close() error                    { return s.inner.Close() }

// ptrSink is a REFUSED port shape, priced so the report can say what the
// by-value RecordEvent costs rather than assert that it is small. core/logger's
// Sink takes the 104-byte record BY VALUE and is frozen (ADR 0039), and the
// copy is also what stops a sink mutating the record its siblings will see; the
// number is here so that trade is stated with a price on it.
type ptrSink interface {
	// write is Sink.Write with the record handed over by reference.
	write(ctx context.Context, r *corelogger.RecordEvent, p []byte) (int, error)
}

// ptrNoop is noopSink behind the by-reference shape.
type ptrNoop struct{}

// the by-reference shape is exercised only through the interface, so both
// implementations are pinned at compile time rather than at first call.
var (
	_ ptrSink = ptrNoop{}
	_ ptrSink = (*ptrGate)(nil)
)

func (ptrNoop) write(_ context.Context, _ *corelogger.RecordEvent, p []byte) (int, error) {
	//: accept the whole payload with no work.
	return len(p), nil
}

// ptrGate is gateSink behind the by-reference shape.
type ptrGate struct {
	// inner is the wrapped by-reference sink.
	inner ptrSink
	// min is the inclusive severity floor.
	min level.Level
}

func (s *ptrGate) write(ctx context.Context, r *corelogger.RecordEvent, p []byte) (int, error) {
	//: same branch as gateSink, reading through the pointer.
	if r.Level < s.min {
		//: below-floor records are dropped as a successful no-op.
		return len(p), nil
	}
	//: at or above the floor — delegate to the wrapped sink.
	return s.inner.write(ctx, r, p)
}

// opaquePtrSink is opaqueSink for the by-reference shape.
//
//go:noinline
func opaquePtrSink(s ptrSink) ptrSink {
	//: identity, behind a barrier the devirtualiser cannot see through.
	return s
}

// levelerGate is a REFUSED design kept here only to be priced: it reads the
// floor through level.Leveler on every record so a control goroutine can retune
// it with Var.Set. level.Var's own doc comment describes precisely this
// arrangement ("a gate may read Level on the hot path while a control goroutine
// raises or lowers the floor via Set") and no gate in the tree implements it.
// BENCH.md §3 carries the numbers and the reason it is not shipped.
type levelerGate struct {
	// inner is the wrapped sink, exactly as gateSink holds it.
	inner corelogger.Sink
	// min is the live floor; the whole point of the variant is that it moves.
	min level.Leveler
}

func (s *levelerGate) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (int, error) {
	//: same branch as gateSink, with the floor read through the port.
	if r.Level < s.min.Level() {
		//: below-floor records are dropped as a successful no-op.
		return len(p), nil
	}
	//: at or above the floor — delegate to the wrapped sink.
	return s.inner.Write(ctx, r, p)
}
func (s *levelerGate) Flush(ctx context.Context) error { return s.inner.Flush(ctx) }
func (s *levelerGate) Close() error                    { return s.inner.Close() }

// varGate is the levelerGate with the port removed: it holds the concrete
// *level.Var, so its Write does the atomic load and NOT the second interface
// call the Leveler port would add. The pair separates the price of the atomic
// from the price of reading it through an interface — which is what the two
// rows show is actually being paid.
type varGate struct {
	// inner is the wrapped sink.
	inner corelogger.Sink
	// min is the live floor, held concretely.
	min *level.Var
}

func (s *varGate) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (int, error) {
	//: same branch as gateSink, over a direct atomic load.
	if r.Level < s.min.Level() {
		//: below-floor records are dropped as a successful no-op.
		return len(p), nil
	}
	//: at or above the floor — delegate to the wrapped sink.
	return s.inner.Write(ctx, r, p)
}
func (s *varGate) Flush(ctx context.Context) error { return s.inner.Flush(ctx) }
func (s *varGate) Close() error                    { return s.inner.Close() }

// mutexGate is the other REFUSED design, and the one the "atomic or mutex"
// question is usually asked about: a floor guarded by an RWMutex so Set can
// publish a new value. It is priced under contention for the same reason —
// a read lock is not free when eight goroutines take it on every record.
type mutexGate struct {
	// inner is the wrapped sink.
	inner corelogger.Sink
	// mu guards min; RLock is taken on every record, Lock only on a retune.
	mu sync.RWMutex
	// min is the floor the mutex protects.
	min level.Level
}

func (s *mutexGate) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (int, error) {
	//: read the floor under the read lock, which is the cost being priced.
	s.mu.RLock()
	min := s.min
	s.mu.RUnlock()
	//: below-floor records are dropped as a successful no-op.
	if r.Level < min {
		//: report the payload as accepted, exactly as gateSink does.
		return len(p), nil
	}
	//: at or above the floor — delegate to the wrapped sink.
	return s.inner.Write(ctx, r, p)
}
func (s *mutexGate) Flush(ctx context.Context) error { return s.inner.Flush(ctx) }
func (s *mutexGate) Close() error                    { return s.inner.Close() }

// set retunes the floor, which is the only thing the mutex buys.
func (s *mutexGate) set(l level.Level) {
	//: publish the new floor under the write lock.
	s.mu.Lock()
	s.min = l
	s.mu.Unlock()
}

// opaqueSink hands a Sink back through a call the compiler will not inline, so
// the concrete type is not provable at the call site and the interface call
// stays an interface call. Production resolves its sink through the writer
// registry and never knows the concrete type either; a devirtualised row would
// be measuring a program nobody runs.
//
//go:noinline
func opaqueSink(s corelogger.Sink) corelogger.Sink {
	//: identity, behind a barrier the devirtualiser cannot see through.
	return s
}

// benchRecord builds the record every row writes: a level and nothing else, so
// the recordEventBytes copy the port's by-value signature forces is present but
// no attribute allocation is.
func benchRecord(l level.Level) corelogger.RecordEvent {
	//: the gate reads exactly one field; the rest travels because the port says so.
	return corelogger.RecordEvent{Level: l}
}

// BenchmarkWrite prices the three states a record can be in: no gate installed
// at all (what New returns for the Info sentinel), dropped by the gate, and
// passed through it.
func BenchmarkWrite(b *testing.B) {
	ctx := context.Background()
	rows := []struct {
		// name labels the row in the report.
		name string
		// sink is the Sink under measurement, already opaque to the compiler.
		sink corelogger.Sink
		// rec is the record driven through it.
		rec corelogger.RecordEvent
	}{
		//: New(inner, Info) returns inner unwrapped — this is that path.
		{"no_gate", opaqueSink(New(noopSink{}, level.Info)), benchRecord(level.Info)},
		//: the headline: a record below an Error floor, dropped.
		{"dropped", opaqueSink(New(noopSink{}, level.Error)), benchRecord(level.Info)},
		//: the same gate on a record that survives it.
		{"passed", opaqueSink(New(noopSink{}, level.Warn)), benchRecord(level.Error)},
		//: the gate with its comparison removed — splits `passed` into the
		//: branch and the second hop.
		{"passthrough", opaqueSink(&passthroughSink{inner: noopSink{}}), benchRecord(level.Error)},
	}
	for _, row := range rows {
		b.Run(row.name, serialRow(ctx, row.sink, row.rec))
	}
}

// serialRow builds the measured closure OUTSIDE the loop that varies, so no
// benchmark parameter is captured by a closure declared in a range body.
func serialRow(ctx context.Context, sink corelogger.Sink, rec corelogger.RecordEvent) func(*testing.B) {
	//: the returned closure captures parameters, not loop variables.
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		total := 0
		for range b.N {
			n, err := sink.Write(ctx, rec, benchPayload)
			if err != nil {
				b.Fatalf("write: %v", err)
			}
			total += n
		}
		benchSinkN = total
	}
}

// parallelRow is serialRow driven from GOMAXPROCS goroutines.
func parallelRow(ctx context.Context, sink corelogger.Sink, rec corelogger.RecordEvent) func(*testing.B) {
	//: the returned closure captures parameters, not loop variables.
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			total := 0
			for pb.Next() {
				n, err := sink.Write(ctx, rec, benchPayload)
				if err != nil {
					b.Fatalf("write: %v", err)
				}
				total += n
			}
			benchSinkN = total
		})
	}
}

// BenchmarkWriteParallel answers the question a gate read by every record and
// written rarely always raises. The shipped gate holds an immutable field, so
// it has no shared mutable state at all; the two variants that would let the
// floor move are priced beside it.
func BenchmarkWriteParallel(b *testing.B) {
	ctx := context.Background()
	rec := benchRecord(level.Info)
	lv := level.NewVar(level.Error)
	rows := []struct {
		// name labels the row.
		name string
		// sink is the gate variant under measurement.
		sink corelogger.Sink
	}{
		//: the shipped gate: an immutable field, no synchronisation.
		{"immutable", opaqueSink(New(noopSink{}, level.Error))},
		//: refused variant 1 — an atomic load through level.Leveler.
		{"atomic_leveler", opaqueSink(&levelerGate{inner: noopSink{}, min: lv})},
		//: the same atomic load without the port's second interface call.
		{"atomic_concrete", opaqueSink(&varGate{inner: noopSink{}, min: lv})},
		//: refused variant 2 — an RWMutex read on every record.
		{"mutex", opaqueSink(&mutexGate{inner: noopSink{}, min: level.Error})},
	}
	for _, row := range rows {
		b.Run(row.name, parallelRow(ctx, row.sink, rec))
	}
}

// BenchmarkWriteParallelRetuned repeats the two mutable variants with a control
// goroutine actually moving the floor, which is the only reason either would
// exist. The immutable gate cannot appear here: retuning it is not a method
// call, it is rebuilding the writer.
func BenchmarkWriteParallelRetuned(b *testing.B) {
	ctx := context.Background()
	rec := benchRecord(level.Info)
	lv := level.NewVar(level.Error)
	mg := &mutexGate{inner: noopSink{}, min: level.Error}
	rows := []struct {
		// name labels the row.
		name string
		// sink is the gate variant under measurement.
		sink corelogger.Sink
		// retune publishes a new floor from the control goroutine.
		retune func(level.Level)
	}{
		//: atomic store against concurrent atomic loads.
		{"atomic", opaqueSink(&levelerGate{inner: noopSink{}, min: lv}), lv.Set},
		//: write lock against concurrent read locks.
		{"mutex", opaqueSink(mg), mg.set},
	}
	for _, row := range rows {
		b.Run(row.name, retunedRow(ctx, row.sink, rec, row.retune))
	}
}

// retunedRow runs parallelRow with a control goroutine genuinely moving the
// floor, which is the only reason a mutable variant would exist at all.
func retunedRow(ctx context.Context, sink corelogger.Sink, rec corelogger.RecordEvent, retune func(level.Level)) func(*testing.B) {
	//: the returned closure captures parameters, not loop variables.
	return func(b *testing.B) {
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			//: alternate the floor as fast as the scheduler allows, so the
			//: readers below never observe a quiescent value.
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
					retune(level.Level(int8(i&1)*8 + 8))
				}
			}
		})
		parallelRow(ctx, sink, rec)(b)
		b.StopTimer()
		close(stop)
		wg.Wait()
	}
}

// BenchmarkNew prices construction, which happens once per writer and is here
// only so the report can say that the Info sentinel's "no wrapper" answer is
// free rather than merely cheap.
func BenchmarkNew(b *testing.B) {
	inner := noopSink{}
	rows := []struct {
		// name labels the row.
		name string
		// min is the floor handed to New.
		min level.Level
	}{
		//: the sentinel: New hands inner back untouched.
		{"info_unwrapped", level.Info},
		//: a real floor: New allocates the gate.
		{"error_wrapped", level.Error},
	}
	for _, row := range rows {
		b.Run(row.name, newRow(inner, row.min))
	}
}

// newRow builds the measured closure outside the loop that varies.
func newRow(inner corelogger.Sink, min level.Level) func(*testing.B) {
	//: the returned closure captures parameters, not loop variables.
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		var s corelogger.Sink
		for range b.N {
			s = New(inner, min)
		}
		benchNewSink.Store(&s)
	}
}

// benchNewSink keeps the constructed sink reachable so New is not eliminated.
var benchNewSink atomic.Pointer[corelogger.Sink]

// BenchmarkRecordCopy prices the port's by-value record against the same drop
// taken through a by-reference shape. Both rows are one interface call and one
// comparison; the only difference between them is recordEventBytes, which
// core/logger's frozen Sink signature copies onto every hop.
func BenchmarkRecordCopy(b *testing.B) {
	ctx := context.Background()
	rec := benchRecord(level.Info)
	byValue := opaqueSink(New(noopSink{}, level.Error))
	byPointer := opaquePtrSink(&ptrGate{inner: ptrNoop{}, min: level.Error})
	b.Run("by_value_"+strconv.Itoa(int(recordEventBytes))+"B", serialRow(ctx, byValue, rec))
	b.Run("by_pointer", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		total := 0
		for range b.N {
			n, err := byPointer.write(ctx, &rec, benchPayload)
			if err != nil {
				b.Fatalf("write: %v", err)
			}
			total += n
		}
		benchSinkN = total
	})
}

// BenchmarkWriteDynamic is BenchmarkWrite/dropped for the two floors that could
// move, measured UNCONTENDED. It is the control for BenchmarkWriteParallel: a
// row that looks acceptable here and collapses there is the whole reason the
// question gets asked about a gate rather than about a counter.
func BenchmarkWriteDynamic(b *testing.B) {
	ctx := context.Background()
	rec := benchRecord(level.Info)
	lv := level.NewVar(level.Error)
	rows := []struct {
		// name labels the row.
		name string
		// sink is the gate variant under measurement.
		sink corelogger.Sink
	}{
		//: the shipped gate, for reference.
		{"immutable", opaqueSink(New(noopSink{}, level.Error))},
		//: atomic load behind the level.Leveler port.
		{"atomic_leveler", opaqueSink(&levelerGate{inner: noopSink{}, min: lv})},
		//: atomic load with no port in the way.
		{"atomic_concrete", opaqueSink(&varGate{inner: noopSink{}, min: lv})},
		//: RWMutex read on every record.
		{"mutex", opaqueSink(&mutexGate{inner: noopSink{}, min: level.Error})},
	}
	for _, row := range rows {
		b.Run(row.name, serialRow(ctx, row.sink, rec))
	}
}
