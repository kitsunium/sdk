// Package console_test — what the default writer costs, and where that cost
// actually lives.
//
// The console writer is one of the two writers ADR 0015 leaves ON by default,
// so every consumer who never configures logging is running this path. Nothing
// in this repository had ever measured it.
//
// A console writer writes to a terminal in production and to something else in
// every test that captures it, and those are not the same cost. Three
// destinations are measured and each is labelled with the deployment it stands
// for; no row here writes to a terminal, because a terminal's cost is the
// terminal emulator's and is neither reproducible nor this package's.
//
//	io.Discard   the package floor — no syscall at all. NOT reachable through
//	             the factory; it exists to separate this SDK's work from the
//	             kernel's, and no deployment looks like it.
//	/dev/null    one real write(2) the kernel discards. This is `app 2>/dev/null`
//	             and it is the floor a deployment can actually have.
//	os.Pipe      one real write(2) with a consumer on the other end. This is a
//	             container's stderr, a `| tee`, a log collector — and it is the
//	             row a reader deploying this should believe.
package console_test

import (
	"context"
	"io"
	"os"
	"strconv"
	"sync"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	servicelogger "github.com/kitsunium/sdk/internal/service/logger"
	"github.com/kitsunium/sdk/internal/service/logger/encoder"
	consolesink "github.com/kitsunium/sdk/internal/service/logger/sink/console"
	_ "github.com/kitsunium/sdk/internal/service/writer/console"
)

// benchLine is the payload every row carries unless it says otherwise: the text
// encoder's output for a short message with three attributes, which is what a
// real service emits. Its length is what BENCH.md's byte columns report, and
// SetBytes derives it rather than restating it.
var benchLine = []byte(
	`2026-09-10T20:15:11.482Z INFO msg="request served" method=GET status=200 dur=1.4ms` + "\n",
)

// benchN keeps the byte counts reachable from outside the loops so no call can
// be eliminated.
var benchN int

// noopSink terminates the end-to-end rows so the handler and the encoder can be
// measured without a transport under them.
type noopSink struct{}

func (noopSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	//: accept the whole payload with no work.
	return len(p), nil
}
func (noopSink) Flush(_ context.Context) error { return nil }
func (noopSink) Close() error                  { return nil }

// devNull opens /dev/null as an *os.File so the row carries a genuine write(2).
func devNull(b *testing.B) *os.File {
	b.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatalf("opening %s: %v", os.DevNull, err)
	}
	b.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			b.Logf("closing %s: %v", os.DevNull, cerr)
		}
	})
	//: a real descriptor the kernel writes to and discards.
	return f
}

// drainedPipe returns the write end of a pipe whose read end is drained by a
// goroutine, so the row measures the write and never the reader's absence. A
// pipe that fills would park the writing goroutine and the benchmark would be
// measuring the scheduler.
func drainedPipe(b *testing.B) *os.File {
	b.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		b.Fatalf("creating the pipe: %v", err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		//: consume as fast as the writer produces; the copy itself is the
		//: reader a deployment has, and its cost is on the other goroutine.
		if _, cerr := io.Copy(io.Discard, r); cerr != nil {
			b.Logf("draining the pipe: %v", cerr)
		}
	})
	b.Cleanup(func() {
		if cerr := w.Close(); cerr != nil {
			b.Logf("closing the pipe writer: %v", cerr)
		}
		wg.Wait()
		if cerr := r.Close(); cerr != nil {
			b.Logf("closing the pipe reader: %v", cerr)
		}
	})
	//: the write end, with a consumer guaranteed to be reading.
	return w
}

// factorySink builds the sink a consumer actually gets — writer.Open("console",
// cfg) — with os.Stderr pointed at dst for the duration of the construction and
// restored immediately. The swap is what the package's own external test
// already does to capture output; it is here so the rows measure the shipped
// composition (levelgate over the terminal sink) rather than a reassembly of it
// that could drift.
func factorySink(b *testing.B, dst *os.File, cfg writer.ConsoleConfig) corelogger.Sink {
	b.Helper()
	saved := os.Stderr
	os.Stderr = dst
	s, err := writer.Open("console", cfg)
	os.Stderr = saved
	if err != nil {
		b.Fatalf("writer.Open(console): %v", err)
	}
	//: the real product of the real factory, pointed somewhere reproducible.
	return s
}

// BenchmarkWrite is the headline: one record, one destination, three
// deployments. Every row is the sink writer.Open hands back, except the
// io.Discard row, which the factory cannot produce and which is labelled as the
// package floor rather than as a deployment.
func BenchmarkWrite(b *testing.B) {
	ctx := context.Background()
	rec := corelogger.RecordEvent{Level: level.Info}
	discard, derr := consolesink.New(io.Discard)
	if derr != nil {
		b.Fatalf("building the io.Discard sink: %v", derr)
	}
	rows := []struct {
		// name labels the destination.
		name string
		// sink is the console sink under measurement.
		sink corelogger.Sink
	}{
		//: the package floor: mutex + context check + an interface call.
		{"io.Discard", discard},
		//: one real write(2) the kernel throws away.
		{"devnull", factorySink(b, devNull(b), writer.ConsoleConfig{})},
		//: one real write(2) with a consumer on the far end.
		{"pipe", factorySink(b, drainedPipe(b), writer.ConsoleConfig{})},
	}
	for _, row := range rows {
		b.Run(row.name, writeRow(ctx, row.sink, rec, benchLine))
	}
}

// writeRow builds the measured closure OUTSIDE the loop that varies, so no
// benchmark parameter is captured by a closure declared in a range body.
func writeRow(ctx context.Context, sink corelogger.Sink, rec corelogger.RecordEvent, payload []byte) func(*testing.B) {
	//: the returned closure captures parameters, not loop variables.
	return func(b *testing.B) {
		b.SetBytes(int64(len(payload)))
		b.ReportAllocs()
		b.ResetTimer()
		total := 0
		for range b.N {
			n, err := sink.Write(ctx, rec, payload)
			if err != nil {
				b.Fatalf("write: %v", err)
			}
			total += n
		}
		benchN = total
	}
}

// BenchmarkWriteSize sweeps the payload against the two destinations that carry
// a syscall, so the report can say what part of a write scales with the bytes
// and what part does not.
func BenchmarkWriteSize(b *testing.B) {
	ctx := context.Background()
	rec := corelogger.RecordEvent{Level: level.Info}
	sizes := []int{96, 1 << 10, 8 << 10}
	dests := []struct {
		// name labels the destination.
		name string
		// sink is the console sink under measurement.
		sink corelogger.Sink
	}{
		{"devnull", factorySink(b, devNull(b), writer.ConsoleConfig{})},
		{"pipe", factorySink(b, drainedPipe(b), writer.ConsoleConfig{})},
	}
	for _, d := range dests {
		for _, size := range sizes {
			payload := make([]byte, size)
			for i := range payload {
				payload[i] = 'x'
			}
			payload[size-1] = '\n'
			b.Run(d.name+"/"+strconv.Itoa(size)+"B", writeRow(ctx, d.sink, rec, payload))
		}
	}
}

// BenchmarkWriteContext prices the guard every record pays before any byte
// moves: `ctx != nil && ctx.Err() != nil`. The rows exist because Err() is a
// method on an interface whose implementation the caller chooses, and a
// cancellable context is what a request-scoped logger actually carries.
func BenchmarkWriteContext(b *testing.B) {
	rec := corelogger.RecordEvent{Level: level.Info}
	sink, err := consolesink.New(io.Discard)
	if err != nil {
		b.Fatalf("building the io.Discard sink: %v", err)
	}
	cancellable, cancel := context.WithCancel(b.Context())
	defer cancel()
	rows := []struct {
		// name labels the context shape.
		name string
		// ctx is the context handed to Write.
		ctx context.Context
	}{
		//: the shape a background emitter carries.
		{"background", context.Background()},
		//: the shape a request-scoped emitter carries.
		{"cancellable", cancellable},
		//: a value wrapper over a cancellable parent — Err() walks up to it.
		{"value_over_cancellable", context.WithValue(cancellable, benchKey{}, 1)},
		//: no context at all; the nil check short-circuits.
		{"nil", nil},
	}
	for _, row := range rows {
		b.Run(row.name, writeRow(row.ctx, sink, rec, benchLine))
	}
}

// benchKey is the key type for the value_over_cancellable row.
type benchKey struct{}

// BenchmarkSinkFloor decomposes the io.Discard row into the two things it is
// made of, because BenchmarkWriteContext produced a result that reads as an
// error: the row that passes NO context — and therefore skips the Err() call
// entirely — measures SLOWER than the row that passes a cancellable one. The
// rows below are what says why. On this Zen 1 part a LOCK-prefixed instruction
// is expensive, the console sink takes one on every record, and in a loop this
// tight two of them land close enough together to stall; three nanoseconds of
// unrelated work between them lets the first retire.
func BenchmarkSinkFloor(b *testing.B) {
	var mu sync.Mutex
	w := benchDiscard()
	b.Run("mutex_pair", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		total := 0
		for range b.N {
			mu.Lock()
			//: one integer increment, because an EMPTY critical section is a
			//: linted defect and cannot be committed. It is also very nearly
			//: free, and the third row below is what says how nearly.
			total++
			mu.Unlock()
		}
		benchN = total
	})
	b.Run("iface_write", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		total := 0
		for range b.N {
			n, err := w.Write(benchLine)
			if err != nil {
				b.Fatalf("write: %v", err)
			}
			total += n
		}
		benchN = total
	})
	b.Run("mutex_pair_plus_iface_write", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		total := 0
		for range b.N {
			mu.Lock()
			n, err := w.Write(benchLine)
			mu.Unlock()
			if err != nil {
				b.Fatalf("write: %v", err)
			}
			total += n
		}
		benchN = total
	})
}

// benchDiscard hands io.Discard back through a barrier the devirtualiser cannot
// see through, so the control pays the same indirect call the sink does.
//
//go:noinline
func benchDiscard() io.Writer {
	//: identity, opaque to the compiler.
	return io.Discard
}

// BenchmarkWriteParallel drives one sink from GOMAXPROCS goroutines, which is
// what a server does. The console sink serialises on its own mutex and the
// *os.File serialises again on the descriptor's internal write lock, so the two
// destinations that carry a syscall are the ones where the answer lives.
func BenchmarkWriteParallel(b *testing.B) {
	ctx := context.Background()
	rec := corelogger.RecordEvent{Level: level.Info}
	discard, derr := consolesink.New(io.Discard)
	if derr != nil {
		b.Fatalf("building the io.Discard sink: %v", derr)
	}
	rows := []struct {
		// name labels the destination.
		name string
		// sink is the console sink under measurement.
		sink corelogger.Sink
	}{
		{"io.Discard", discard},
		{"devnull", factorySink(b, devNull(b), writer.ConsoleConfig{})},
		{"pipe", factorySink(b, drainedPipe(b), writer.ConsoleConfig{})},
	}
	for _, row := range rows {
		b.Run(row.name, parallelRow(ctx, row.sink, rec))
	}
}

// parallelRow is writeRow driven from GOMAXPROCS goroutines.
func parallelRow(ctx context.Context, sink corelogger.Sink, rec corelogger.RecordEvent) func(*testing.B) {
	//: the returned closure captures parameters, not loop variables.
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			total := 0
			for pb.Next() {
				n, err := sink.Write(ctx, rec, benchLine)
				if err != nil {
					b.Fatalf("write: %v", err)
				}
				total += n
			}
			benchN = total
		})
	}
}

// BenchmarkOpen prices construction. It runs once per writer and is here only
// so the report can state what the MinLevel sentinel buys at that moment
// (levelgate.New hands the sink back unwrapped) rather than assert it.
func BenchmarkOpen(b *testing.B) {
	null := devNull(b)
	rows := []struct {
		// name labels the row.
		name string
		// cfg is the config handed to the factory.
		cfg writer.ConsoleConfig
	}{
		//: the zero config: stderr, inherit — no gate is installed.
		{"default_no_gate", writer.ConsoleConfig{}},
		//: an explicit floor: a gate is allocated over the sink.
		{"min_level_error", writer.ConsoleConfig{MinLevel: level.Error}},
	}
	for _, row := range rows {
		b.Run(row.name, openRow(null, row.cfg))
	}
}

// openRow builds the measured closure outside the loop that varies.
func openRow(null *os.File, cfg writer.ConsoleConfig) func(*testing.B) {
	//: the returned closure captures parameters, not loop variables.
	return func(b *testing.B) {
		saved := os.Stderr
		os.Stderr = null
		b.ReportAllocs()
		b.ResetTimer()
		var s corelogger.Sink
		for range b.N {
			var err error
			s, err = writer.Open("console", cfg)
			if err != nil {
				b.Fatalf("writer.Open(console): %v", err)
			}
		}
		b.StopTimer()
		os.Stderr = saved
		benchOpened = s
	}
}

// benchOpened keeps the constructed sink reachable so Open is not eliminated.
var benchOpened corelogger.Sink

// BenchmarkEmit is the END-TO-END row: a full record through the text encoder
// and the generic handler, landing in this writer. The noop row is the same
// emit with the transport removed, so the difference is the writer's share of
// what a consumer pays per line — which is the number the "one alloc per emit"
// claim in the root CLAUDE.md has never been stated against.
func BenchmarkEmit(b *testing.B) {
	ctx := context.Background()
	rows := []struct {
		// name labels the transport under the handler.
		name string
		// sink is the transport.
		sink corelogger.Sink
	}{
		//: encoder + handler with no transport at all.
		{"noop_sink", noopSink{}},
		//: the same, into the package floor.
		{"console_discard", mustDiscard(b)},
		//: the same, into a real descriptor the kernel discards.
		{"console_devnull", factorySink(b, devNull(b), writer.ConsoleConfig{})},
		//: the same, into a real descriptor with a reader.
		{"console_pipe", factorySink(b, drainedPipe(b), writer.ConsoleConfig{})},
	}
	for _, row := range rows {
		b.Run(row.name, emitRow(ctx, row.sink))
	}
}

// emitRow builds the measured closure outside the loop that varies.
func emitRow(ctx context.Context, sink corelogger.Sink) func(*testing.B) {
	//: the returned closure captures parameters, not loop variables.
	return func(b *testing.B) {
		h, err := servicelogger.NewHandler(encoder.NewText(clock.System), sink, level.Info)
		if err != nil {
			b.Fatalf("building the handler: %v", err)
		}
		rec := corelogger.RecordEvent{
			Level:   level.Info,
			Message: "request served",
			Attrs: []corelogger.AttrValue{
				{Key: "method", Value: corelogger.StringValue("GET")},
				{Key: "status", Value: corelogger.IntValue(200)},
			},
		}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if herr := h.Handle(ctx, rec); herr != nil {
				b.Fatalf("handle: %v", herr)
			}
		}
	}
}

// mustDiscard builds the io.Discard console sink or fails the benchmark.
func mustDiscard(b *testing.B) corelogger.Sink {
	b.Helper()
	s, err := consolesink.New(io.Discard)
	if err != nil {
		b.Fatalf("building the io.Discard sink: %v", err)
	}
	//: the package floor as a Sink.
	return s
}
