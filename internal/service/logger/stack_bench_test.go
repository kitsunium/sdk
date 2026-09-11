package logger_test

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svclogger "github.com/kitsunium/sdk/internal/service/logger"
	"github.com/kitsunium/sdk/internal/service/logger/encoder"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/async"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/encwrite"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/failover"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/multi"
	mwrecover "github.com/kitsunium/sdk/internal/service/logger/middleware/recover"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/route"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/sample"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/tee"
	"github.com/kitsunium/sdk/internal/service/logger/sink/console"
	"github.com/kitsunium/sdk/internal/service/logger/sink/file"
	"github.com/kitsunium/sdk/internal/service/logger/sink/memory"
	"github.com/kitsunium/sdk/internal/service/logger/sink/syslog"

	_ "github.com/kitsunium/sdk/internal/service/crypto/aesgcm"
	_ "github.com/kitsunium/sdk/internal/service/crypto/hkdfsha256"
)

const (
	// benchMemoryResetEvery bounds the memory sink's retained buffer during
	// the benchmark. Without it the sink accumulates every record of the run,
	// and the line measures an unbounded slice growing — memmove over hundreds
	// of megabytes — rather than the per-record cost a caller pays.
	benchMemoryResetEvery int = 1024

	// benchSampleDropRate is large enough that essentially every write in a
	// benchmark run is dropped, so the line measures the drop path.
	benchSampleDropRate int = 1 << 30

	// benchAsyncRing is deliberately large so the drainer has room to keep up
	// with a tight producer loop over a discard downstream; the drop counter
	// reported by the benchmark says whether it did.
	benchAsyncRing int = 1 << 16

	// benchAsyncBurst is the number of records published between two untimed
	// drains. Kept well under benchAsyncRing so the ring cannot saturate and
	// the timed region is the hand-off and nothing else.
	benchAsyncBurst int = 1024
)

// benchAcceptedBytes is the package-level observation point for every sink and
// middleware benchmark below. The control sink is a discard sink, and a
// discard sink whose byte count nothing reads is exactly the shape the
// optimiser is allowed to erase — which would publish a fictitious
// sub-nanosecond control and make every middleware look infinitely expensive.
// Accumulating into a package-level variable keeps the call observable.
var benchAcceptedBytes int64

// benchWriteErr parks the error every benchmarked Write returns, for the same
// reason benchAcceptedBytes parks the byte count.
var benchWriteErr error

// discardSink is the CONTROL for every middleware line in this file: a
// terminal Sink that does the minimum the port allows — count the payload and
// report it accepted. Every middleware benchmark wraps exactly this, so the
// difference between a middleware's line and the control's line IS the
// middleware.
type discardSink struct{}

// Write accepts p, records its length at package scope, and reports success.
func (discardSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	benchAcceptedBytes += int64(len(p))
	return len(p), nil
}

// Flush is a no-op — the control buffers nothing.
func (discardSink) Flush(_ context.Context) error { return nil }

// Close is a no-op — the control owns nothing.
func (discardSink) Close() error { return nil }

// errBenchDownstream is the failure a rejectSink reports. Package-level so building
// it is never inside a timed loop.
var errBenchDownstream = errors.New("bench: downstream refused the record")

// rejectSink is the control's failing twin: it does the same accounting and
// then reports the record refused. It drives the failed-over and
// all-primaries-failed paths.
type rejectSink struct{}

// Write records the payload length and reports the record refused.
func (rejectSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	benchAcceptedBytes += int64(len(p))
	return 0, errBenchDownstream
}

// Flush is a no-op.
func (rejectSink) Flush(_ context.Context) error { return nil }

// Close is a no-op.
func (rejectSink) Close() error { return nil }

// noopConn is a net.Conn whose Write does nothing but count. It is injected
// through syslog.Config.Dialer — the package's own documented seam — so the
// syslog benchmark can separate the RFC5424 framing cost from the transport
// cost without inventing a syslog daemon.
type noopConn struct{ net.Conn }

// Write records the frame length and reports it fully written.
func (noopConn) Write(p []byte) (int, error) {
	benchAcceptedBytes += int64(len(p))
	return len(p), nil
}

// Close is a no-op — there is no descriptor behind this conn.
func (noopConn) Close() error { return nil }

// benchPayload is the encoded line every sink-level benchmark delivers. It is
// a real text-encoder line so the byte count is representative.
var benchPayload = []byte(
	"2026-09-10T12:34:56.789Z INFO user login accepted user=\"a-value-of-moderate-length\" " +
		"tenant=\"a-value-of-moderate-length\" region=\"a-value-of-moderate-length\" n=42\n")

// benchRecord is the record travelling beside benchPayload. Built once at
// package scope: a record built inside a timed loop measures the builder.
var benchRecord = corelogger.RecordEvent{
	Time:    time.Date(2026, 9, 10, 12, 34, 56, 789_000_000, time.UTC),
	Level:   level.Info,
	Message: "user login accepted",
	Attrs: []corelogger.AttrValue{
		{Key: "user", Value: corelogger.StringValue("a-value-of-moderate-length")},
		{Key: "tenant", Value: corelogger.StringValue("a-value-of-moderate-length")},
		{Key: "region", Value: corelogger.StringValue("a-value-of-moderate-length")},
		{Key: "n", Value: corelogger.Int64Value(42)},
	},
}

// runSinkWrite is the shared timed loop for every sink-level line: it calls
// Write with a pre-built record and payload and parks both results.
func runSinkWrite(b *testing.B, s corelogger.Sink) {
	b.Helper()
	ctx := b.Context()
	//: one untimed call so any lazy first-call state (pool warm-up, page
	//: fault on the first file write) is not charged to iteration one.
	_, benchWriteErr = s.Write(ctx, benchRecord, benchPayload)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, benchWriteErr = s.Write(ctx, benchRecord, benchPayload)
	}
}

// -----------------------------------------------------------------------
// 1. The control, and the four terminal sinks.
// -----------------------------------------------------------------------

// BenchmarkSink_Discard is THE control. Every middleware line below wraps
// exactly this sink, so any line measuring faster than this one is noise, not
// a result.
func BenchmarkSink_Discard(b *testing.B) {
	runSinkWrite(b, discardSink{})
}

// BenchmarkSink_Console_Discard measures the console sink over an
// io.Discard-shaped writer: the mutex, the context check and the interface
// call, with no terminal in the loop.
func BenchmarkSink_Console_Discard(b *testing.B) {
	s, err := console.New(io.Discard)
	if err != nil {
		b.Fatalf("console.New err = %v", err)
	}
	runSinkWrite(b, s)
}

// BenchmarkSink_Memory measures the in-memory test sink. It is the only sink
// that retains the RECORD rather than the bytes, so this line prices the
// defensive deep clone of the attribute slice. The retained buffer is reset
// outside the timer every benchMemoryResetEvery writes.
func BenchmarkSink_Memory(b *testing.B) {
	s := memory.NewMemory()
	ctx := b.Context()
	_, benchWriteErr = s.Write(ctx, benchRecord, benchPayload)
	s.Reset()
	b.ReportAllocs()
	b.ResetTimer()
	n := 0
	for b.Loop() {
		_, benchWriteErr = s.Write(ctx, benchRecord, benchPayload)
		n++
		//: bound the retained buffer so the append's amortised growth does
		//: not become the thing being measured.
		if n%benchMemoryResetEvery == 0 {
			b.StopTimer()
			s.Reset()
			b.StartTimer()
		}
	}
}

// benchPrivateDir returns a 0700 directory owned by this benchmark. b.TempDir
// is not used: TMPDIR here carries a POSIX default ACL that widens the mode to
// 0775, and code that refuses a non-private directory would then silently take
// a different path than production.
func benchPrivateDir(b *testing.B) string {
	b.Helper()
	dir := filepath.Join(os.TempDir(), "logger-bench-"+b.Name())
	dir = filepath.Clean(dir)
	//: a leftover directory from an aborted run must not change the mode we
	//: are about to assert, so remove it before creating.
	if rerr := os.RemoveAll(dir); rerr != nil {
		b.Fatalf("RemoveAll err = %v", rerr)
	}
	if merr := os.Mkdir(dir, 0o700); merr != nil {
		b.Fatalf("Mkdir err = %v", merr)
	}
	//: Mkdir's mode is masked by umask AND widened by any inherited default
	//: ACL, so the explicit Chmod is what actually makes the directory private.
	if cerr := os.Chmod(dir, 0o700); cerr != nil {
		b.Fatalf("Chmod err = %v", cerr)
	}
	b.Cleanup(func() {
		if rerr := os.RemoveAll(dir); rerr != nil {
			b.Errorf("cleanup RemoveAll err = %v", rerr)
		}
	})
	return dir
}

// BenchmarkSink_File measures the file sink appending to a real file in a
// private 0700 directory. The write dominates; the mutex and the context check
// are the same two costs the console sink pays.
func BenchmarkSink_File(b *testing.B) {
	dir := benchPrivateDir(b)
	s, err := file.New(filepath.Join(dir, "bench.log"))
	if err != nil {
		b.Fatalf("file.New err = %v", err)
	}
	b.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			b.Errorf("Close err = %v", cerr)
		}
	})
	runSinkWrite(b, s)
}

// BenchmarkSink_Syslog_FramingOnly measures the RFC5424 framing with the
// transport removed: the connection is a counting no-op injected through the
// package's own Config.Dialer seam. This is the message FORMATTING cost.
func BenchmarkSink_Syslog_FramingOnly(b *testing.B) {
	s, err := syslog.NewWithConfig("udp", "127.0.0.1:514", syslog.Config{
		Dialer: func(_, _ string) (net.Conn, error) { return noopConn{}, nil },
	})
	if err != nil {
		b.Fatalf("syslog.NewWithConfig err = %v", err)
	}
	b.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			b.Errorf("Close err = %v", cerr)
		}
	})
	runSinkWrite(b, s)
}

// BenchmarkSink_Syslog_UDPLoopback measures framing PLUS a real datagram to a
// real listening socket on loopback. The delta against FramingOnly is the
// transport.
//
// GOROUTINE LIFECYCLE: this benchmark starts exactly one goroutine,
// drainPacketConn. It is owned by the b.Cleanup that closes pc: the goroutine
// blocks in ReadFrom, that Close makes ReadFrom return an error, and
// drainPacketConn returns on any read error. It therefore cannot outlive the
// benchmark, holds no lock, writes to nothing shared, and needs no join — the
// socket close IS the stop signal. It exists because an undrained UDP receive
// buffer fills and the kernel starts discarding, which would silently turn
// this line into a measurement of sendto-into-the-void.
func BenchmarkSink_Syslog_UDPLoopback(b *testing.B) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Skipf("no loopback UDP available in this environment: %v", err)
	}
	b.Cleanup(func() {
		if cerr := pc.Close(); cerr != nil {
			b.Errorf("ListenPacket Close err = %v", cerr)
		}
	})
	//: drain the socket so the kernel receive buffer cannot fill and start
	//: dropping — a benchmark that silently stops delivering measures nothing.
	go drainPacketConn(pc)
	s, serr := syslog.New("udp", pc.LocalAddr().String())
	if serr != nil {
		b.Fatalf("syslog.New err = %v", serr)
	}
	b.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			b.Errorf("Close err = %v", cerr)
		}
	})
	runSinkWrite(b, s)
}

// drainPacketConn reads and discards datagrams until the socket is closed.
func drainPacketConn(pc net.PacketConn) {
	buf := make([]byte, 4096)
	for {
		//: a read error means the socket was closed by the cleanup — stop.
		if _, _, err := pc.ReadFrom(buf); err != nil {
			return
		}
	}
}

// -----------------------------------------------------------------------
// 2. Per-middleware cost, each wrapping the discard control.
// -----------------------------------------------------------------------

// BenchmarkMW_Route_FirstMatch measures the router when the first predicate
// accepts: one closure call plus one level comparison.
func BenchmarkMW_Route_FirstMatch(b *testing.B) {
	s := route.New(nil, route.Params{When: route.LevelAtLeast(level.Info), Sink: discardSink{}})
	runSinkWrite(b, s)
}

// BenchmarkMW_Route_FourthMatch measures the router when three predicates are
// evaluated and rejected before the fourth accepts, so the delta against
// FirstMatch is three predicate evaluations.
func BenchmarkMW_Route_FourthMatch(b *testing.B) {
	never := route.LevelAtLeast(level.Error + 1)
	s := route.New(nil,
		route.Params{When: never, Sink: discardSink{}},
		route.Params{When: never, Sink: discardSink{}},
		route.Params{When: never, Sink: discardSink{}},
		route.Params{When: route.LevelAtLeast(level.Info), Sink: discardSink{}},
	)
	runSinkWrite(b, s)
}

// BenchmarkMW_Route_Fallback measures the router when no predicate matches and
// the catch-all takes the record.
func BenchmarkMW_Route_Fallback(b *testing.B) {
	never := route.LevelAtLeast(level.Error + 1)
	s := route.New(discardSink{}, route.Params{When: never, Sink: discardSink{}})
	runSinkWrite(b, s)
}

// BenchmarkMW_Sample_Kept measures the sampling decision on a record that is
// KEPT — rate 1 forwards every write.
func BenchmarkMW_Sample_Kept(b *testing.B) {
	s, err := sample.New(discardSink{}, 1)
	if err != nil {
		b.Fatalf("sample.New err = %v", err)
	}
	runSinkWrite(b, s)
}

// BenchmarkMW_Sample_Dropped measures the sampling decision on a record that
// is DROPPED. Sampling exists to make the dropped record cheap; if this line
// is not clearly below Sample_Kept, the middleware is not doing its job.
func BenchmarkMW_Sample_Dropped(b *testing.B) {
	s, err := sample.New(discardSink{}, benchSampleDropRate)
	if err != nil {
		b.Fatalf("sample.New err = %v", err)
	}
	runSinkWrite(b, s)
}

// BenchmarkMW_Multi_1 measures fan-out to one destination.
func BenchmarkMW_Multi_1(b *testing.B) {
	runSinkWrite(b, multi.New(discardSink{}))
}

// BenchmarkMW_Multi_2 measures fan-out to two destinations.
func BenchmarkMW_Multi_2(b *testing.B) {
	runSinkWrite(b, multi.New(discardSink{}, discardSink{}))
}

// BenchmarkMW_Multi_3 measures fan-out to three destinations — the last width
// at which multi's per-Write error scratchpad still fits the compiler's
// implicit stack budget.
func BenchmarkMW_Multi_3(b *testing.B) {
	runSinkWrite(b, multi.New(discardSink{}, discardSink{}, discardSink{}))
}

// BenchmarkMW_Multi_4 measures fan-out to four destinations.
func BenchmarkMW_Multi_4(b *testing.B) {
	runSinkWrite(b, multi.New(discardSink{}, discardSink{}, discardSink{}, discardSink{}))
}

// BenchmarkMW_Multi_8 measures fan-out to eight destinations, so the report
// can say whether the per-Write cost past the allocation cliff stays linear.
func BenchmarkMW_Multi_8(b *testing.B) {
	runSinkWrite(b, multi.New(
		discardSink{}, discardSink{}, discardSink{}, discardSink{},
		discardSink{}, discardSink{}, discardSink{}, discardSink{},
	))
}

// BenchmarkMW_Tee_1 measures tee fan-out to one primary.
func BenchmarkMW_Tee_1(b *testing.B) {
	runSinkWrite(b, tee.NewTeeSink(tee.Config{Primaries: []corelogger.Sink{discardSink{}}}))
}

// BenchmarkMW_Tee_2 measures tee fan-out to two primaries.
func BenchmarkMW_Tee_2(b *testing.B) {
	runSinkWrite(b, tee.NewTeeSink(tee.Config{Primaries: []corelogger.Sink{discardSink{}, discardSink{}}}))
}

// BenchmarkMW_Tee_4 measures tee fan-out to four primaries.
func BenchmarkMW_Tee_4(b *testing.B) {
	runSinkWrite(b, tee.NewTeeSink(tee.Config{Primaries: []corelogger.Sink{
		discardSink{}, discardSink{}, discardSink{}, discardSink{},
	}}))
}

// BenchmarkMW_Recover measures the deferred recover on the happy path — the
// classic hidden cost, since the defer runs on every call whether or not
// anything panics.
func BenchmarkMW_Recover(b *testing.B) {
	s, err := mwrecover.New(discardSink{})
	if err != nil {
		b.Fatalf("recover.New err = %v", err)
	}
	runSinkWrite(b, s)
}

// BenchmarkMW_Failover_FirstOK measures the happy path: the first branch
// accepts and the chain short-circuits. This is what a healthy deployment
// pays on every record, so it must be near-free.
func BenchmarkMW_Failover_FirstOK(b *testing.B) {
	s, err := failover.New(discardSink{}, discardSink{})
	if err != nil {
		b.Fatalf("failover.New err = %v", err)
	}
	runSinkWrite(b, s)
}

// BenchmarkMW_Failover_SecondOK measures the failed-over path: the primary
// refuses every record and the secondary accepts it.
func BenchmarkMW_Failover_SecondOK(b *testing.B) {
	s, err := failover.New(rejectSink{}, discardSink{})
	if err != nil {
		b.Fatalf("failover.New err = %v", err)
	}
	runSinkWrite(b, s)
}

// BenchmarkMW_Failover_FirstOK_5Branches measures the SAME happy path as
// BenchmarkMW_Failover_FirstOK — the first branch accepts, the other four are
// never called — over a five-branch chain. Any difference is the per-Write
// error scratchpad, whose size is the chain LENGTH rather than the work done.
func BenchmarkMW_Failover_FirstOK_5Branches(b *testing.B) {
	s, err := failover.New(discardSink{}, discardSink{}, discardSink{}, discardSink{}, discardSink{})
	if err != nil {
		b.Fatalf("failover.New err = %v", err)
	}
	runSinkWrite(b, s)
}

// BenchmarkMW_Async_PublishSaturated measures the PUBLISHER's cost under an
// unpaced producer: a caller emitting in a tight loop. The reported drops
// metric is the point of the line — it says how much of the measured work was
// the hand-off and how much was the drop policy.
func BenchmarkMW_Async_PublishSaturated(b *testing.B) {
	var drops int64
	s := async.New(discardSink{}, async.Config{
		BufferSize: benchAsyncRing,
		OnDrop:     func(missed int) { drops += int64(missed) },
	})
	b.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			b.Errorf("Close err = %v", cerr)
		}
	})
	runSinkWrite(b, s)
	//: a saturated ring silently changes what this line measures from
	//: "hand-off" to "hand-off plus drop policy", so publish the count.
	b.ReportMetric(float64(drops), "drops")
}

// BenchmarkMW_Async_PublishPaced measures the PUBLISHER's cost when the ring
// is not saturated: what the calling goroutine actually pays to hand a record
// off. The drainer's delivery to the downstream sink runs on another goroutine
// and is NOT in this number — it is the downstream sink's own cost, moved off
// the caller. The drain between bursts is outside the timer.
func BenchmarkMW_Async_PublishPaced(b *testing.B) {
	var drops int64
	s := async.New(discardSink{}, async.Config{
		BufferSize: benchAsyncRing,
		OnDrop:     func(missed int) { drops += int64(missed) },
	})
	b.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			b.Errorf("Close err = %v", cerr)
		}
	})
	ctx := b.Context()
	_, benchWriteErr = s.Write(ctx, benchRecord, benchPayload)
	if ferr := s.Flush(ctx); ferr != nil {
		b.Fatalf("warm Flush err = %v", ferr)
	}
	b.ReportAllocs()
	b.ResetTimer()
	n := 0
	for b.Loop() {
		_, benchWriteErr = s.Write(ctx, benchRecord, benchPayload)
		n++
		//: drain outside the timer so the ring never saturates and the drop
		//: policy never enters the measurement.
		if n%benchAsyncBurst == 0 {
			b.StopTimer()
			if ferr := s.Flush(ctx); ferr != nil {
				b.Fatalf("Flush err = %v", ferr)
			}
			b.StartTimer()
		}
	}
	//: zero here is the proof that the line above is the hand-off.
	b.ReportMetric(float64(drops), "drops")
}

// benchEncWriteKey is the fixed master key the encwrite benchmark derives its
// per-sink subkey from. Deterministic so the benchmark is reproducible.
func benchEncWriteKey(b *testing.B) corecrypto.Key {
	b.Helper()
	raw := make([]byte, corecrypto.KeyLen)
	for i := range raw {
		raw[i] = byte(i)
	}
	k, err := corecrypto.NewKey(raw)
	if err != nil {
		b.Fatalf("corecrypto.NewKey err = %v", err)
	}
	return k
}

// BenchmarkMW_EncWrite measures the encrypting middleware: an AES-256-GCM seal
// of the record's bytes plus the 4-byte length frame, under its own mutex.
func BenchmarkMW_EncWrite(b *testing.B) {
	w, err := encwrite.NewEncWriter(encwrite.Config{Sink: discardSink{}, Key: benchEncWriteKey(b)})
	if err != nil {
		b.Fatalf("encwrite.NewEncWriter err = %v", err)
	}
	runSinkWrite(b, w)
}

// -----------------------------------------------------------------------
// 3. THE HEADLINE — does a middleware-stacked emit still allocate once?
// -----------------------------------------------------------------------

// benchStackLogger wires a full emit path: text encoder, the supplied sink
// chain, and the chainable Builder on top.
func benchStackLogger(b *testing.B, s corelogger.Sink) corelogger.Logger {
	b.Helper()
	h, err := svclogger.NewHandler(encoder.NewText(clock.System), s, level.Debug)
	if err != nil {
		b.Fatalf("NewHandler err = %v", err)
	}
	lg, lerr := svclogger.New(h)
	if lerr != nil {
		b.Fatalf("New err = %v", lerr)
	}
	return lg
}

// runEmit is the shared timed loop for the stack-depth sweep: a full
// Build(...).Send(...) emit through the handler into the supplied sink chain.
func runEmit(b *testing.B, s corelogger.Sink) {
	b.Helper()
	lg := benchStackLogger(b, s)
	ctx := b.Context()
	//: warm the recordPool so the steady-state path — the one the one-alloc
	//: claim is about — is what the timed loop measures.
	svclogger.Build(lg, level.Info).Str("k", "v").Int("n", 7).Send(ctx, "warm")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		svclogger.Build(lg, level.Info).
			Str("k", "v").
			Int("n", 7).
			Send(ctx, "user login accepted")
	}
}

// BenchmarkEmit_Depth0 is the emit-level control: handler straight onto the
// discard sink, no middleware at all. This is the line the SDK's
// one-allocation-per-emit claim is measured against.
func BenchmarkEmit_Depth0(b *testing.B) {
	runEmit(b, discardSink{})
}

// BenchmarkEmit_Depth1 is one middleware deep — recover, the outermost of the
// recommended chain.
func BenchmarkEmit_Depth1(b *testing.B) {
	runEmit(b, benchChain(b, 1))
}

// BenchmarkEmit_Depth2 is two middlewares deep — recover over failover.
func BenchmarkEmit_Depth2(b *testing.B) {
	runEmit(b, benchChain(b, 2))
}

// BenchmarkEmit_Depth4 is four middlewares deep — the composition order
// middleware/CLAUDE.md recommends, minus async: recover over failover over
// sample over multi.
func BenchmarkEmit_Depth4(b *testing.B) {
	runEmit(b, benchChain(b, 4))
}

// benchChain builds the recommended chain to the requested depth, inner-most
// first: multi, sample, failover, recover. Depth 0 is the bare control.
func benchChain(b *testing.B, depth int) corelogger.Sink {
	b.Helper()
	var s corelogger.Sink
	s = discardSink{}
	//: depth 4 is the full recommended order; shallower depths peel it from
	//: the inside out so each step adds exactly one wrapper.
	if depth >= 4 {
		s = multi.New(s)
	}
	if depth >= 3 {
		var err error
		s, err = sample.New(s, 1)
		if err != nil {
			b.Fatalf("sample.New err = %v", err)
		}
	}
	if depth >= 2 {
		var err error
		s, err = failover.New(s)
		if err != nil {
			b.Fatalf("failover.New err = %v", err)
		}
	}
	if depth >= 1 {
		var err error
		s, err = mwrecover.New(s)
		if err != nil {
			b.Fatalf("recover.New err = %v", err)
		}
	}
	return s
}

// BenchmarkEmit_FanOut2 is a deployment shape rather than a depth: recover
// over failover(primary, fallback) over multi(console, file). Two branches
// each — the width at which multi's and failover's per-Write error
// scratchpads still fit the compiler's implicit stack budget.
func BenchmarkEmit_FanOut2(b *testing.B) {
	runEmit(b, benchFanOut(b, 2))
}

// BenchmarkEmit_FanOut4 is the same shape at four destinations, which is
// where the scratchpads stop fitting.
func BenchmarkEmit_FanOut4(b *testing.B) {
	runEmit(b, benchFanOut(b, 4))
}

// benchFanOut builds recover → failover(width) → multi(width) over discard
// sinks, so the only thing varying between the two lines is the width.
func benchFanOut(b *testing.B, width int) corelogger.Sink {
	b.Helper()
	branches := make([]corelogger.Sink, 0, width)
	for range width {
		branches = append(branches, discardSink{})
	}
	fan := multi.New(branches...)
	chain := make([]corelogger.Sink, 0, width)
	for range width {
		chain = append(chain, fan)
	}
	over, err := failover.New(chain...)
	if err != nil {
		b.Fatalf("failover.New err = %v", err)
	}
	guarded, rerr := mwrecover.New(over)
	if rerr != nil {
		b.Fatalf("recover.New err = %v", rerr)
	}
	return guarded
}

// BenchmarkEmit_Depth4_AllocFree is a four-deep chain built from DIFFERENT
// middlewares than BenchmarkEmit_Depth4 — tee for the fan-out and route for
// the hop, in place of multi and failover.
//
// It was the control that separated the two hypotheses when Emit_FanOut4
// measured 3 allocs/op: was an extra allocation inherent to stacking, or
// specific to those two implementations? `tee` and `route` had never
// allocated, this line was 1 alloc/op while the other was 3, and that is what
// pointed at multi.go:49 and failover_sink.go:52 rather than at the chain.
// Both are fixed now and the two lines agree, so it stands as the regression
// guard: if they ever diverge again, the difference is in a middleware and not
// in the depth.
func BenchmarkEmit_Depth4_AllocFree(b *testing.B) {
	fan := tee.NewTeeSink(tee.Config{Primaries: []corelogger.Sink{discardSink{}}})
	sampled, err := sample.New(fan, 1)
	if err != nil {
		b.Fatalf("sample.New err = %v", err)
	}
	routed := route.New(nil, route.Params{When: route.LevelAtLeast(level.Debug), Sink: sampled})
	wrapped, rerr := mwrecover.New(routed)
	if rerr != nil {
		b.Fatalf("recover.New err = %v", rerr)
	}
	runEmit(b, wrapped)
}
