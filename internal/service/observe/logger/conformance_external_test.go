package logger_test

// The conformance table of the logger family (#1149): every terminal sink,
// every middleware and every writer under sink/, middleware/ and writer/ is a
// row, and each row runs the same checks — a Write after Close refused, Flush
// twice, concurrent emits all delivered, a record past the sink's bound
// refused and counted — or says, in the row, why one does not apply. The
// failover chain's switch and heal on a ManualClock is its own check.
// TestConformanceCoversEveryPackage fails when a package is added under one of
// the three directories without a row: this table is the family's contract
// (CLAUDE.md), the per-package suites test the rest.

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	coreasync "github.com/kitsunium/sdk/internal/core/observe/logger/middleware/async"
	corefile "github.com/kitsunium/sdk/internal/core/observe/logger/sink/file"
	corewriter "github.com/kitsunium/sdk/internal/core/observe/logger/writer"
	corerotfile "github.com/kitsunium/sdk/internal/core/observe/logger/writer/rotfile"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/async"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/encwrite"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/failover"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/multi"
	logrecover "github.com/kitsunium/sdk/internal/service/observe/logger/middleware/recover"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/route"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/sample"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/tee"
	"github.com/kitsunium/sdk/internal/service/observe/logger/sink/console"
	"github.com/kitsunium/sdk/internal/service/observe/logger/sink/file"
	"github.com/kitsunium/sdk/internal/service/observe/logger/sink/memory"
	"github.com/kitsunium/sdk/internal/service/observe/logger/sink/syslog"
	consolewriter "github.com/kitsunium/sdk/internal/service/observe/logger/writer/console"
	"github.com/kitsunium/sdk/internal/service/observe/logger/writer/dbsink"
	filewriter "github.com/kitsunium/sdk/internal/service/observe/logger/writer/file"
	"github.com/kitsunium/sdk/internal/service/observe/logger/writer/journald"
	"github.com/kitsunium/sdk/internal/service/observe/logger/writer/levelgate"
	"github.com/kitsunium/sdk/internal/service/observe/logger/writer/nettransport"
	"github.com/kitsunium/sdk/internal/service/observe/logger/writer/otlpjson"
	"github.com/kitsunium/sdk/internal/service/observe/logger/writer/rotfile"
)

// countExporter counts the log records of every OTLP/JSON document the
// "otlpjson" writer hands it (#867): one timeUnixNano per LogRecord.
type countExporter struct {
	records atomic.Int64
}

// ExportDocument counts doc's records.
func (e *countExporter) ExportDocument(_ context.Context, doc []byte) error {
	e.records.Add(int64(bytes.Count(doc, []byte(`"timeUnixNano"`))))
	return nil
}

// errCountClosed is what countSink answers a Write after its Close with.
var errCountClosed = errors.New("conformance: count sink closed")

// countSink is the downstream every middleware row wraps: it counts records,
// fails on demand (the failover check) and refuses after Close.
type countSink struct {
	n      atomic.Int64
	closed atomic.Bool
	fail   atomic.Bool
}

func (c *countSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	if c.closed.Load() {
		return 0, errCountClosed
	}
	if c.fail.Load() {
		return 0, errors.New("conformance: count sink told to fail")
	}
	c.n.Add(1)
	return len(p), nil
}
func (*countSink) Flush(context.Context) error { return nil }
func (c *countSink) Close() error {
	c.closed.Store(true)
	return nil
}

// lockedBuffer is a bytes.Buffer safe for concurrent writers, counting lines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) lines() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), "\n")
}

// countConn is a net.Conn counting the frames written to it.
type countConn struct {
	net.Conn
	frames atomic.Int64
}

func (c *countConn) Write(p []byte) (int, error) {
	c.frames.Add(1)
	return len(p), nil
}
func (*countConn) Close() error { return nil }

// fileLines counts the lines of path.
func fileLines(path string) int {
	b, _ := os.ReadFile(path)
	return strings.Count(string(b), "\n")
}

// isRefusal reports whether err is one of codes or wraps errCountClosed.
func isRefusal(err error, codes ...errs.Code) bool {
	for _, c := range codes {
		if errs.HasCode(err, c) {
			return true
		}
	}
	return errors.Is(err, errCountClosed)
}

// conformanceRow is one package of the family: how to build it, how to count
// what it delivered, and what its checks expect — or why a check does not
// apply to it.
type conformanceRow struct {
	// pkg is the package directory under internal/service/observe/logger.
	pkg string
	// build returns the sink under test and the count of records it delivered.
	build func(t *testing.T) (corelogger.Sink, func() int)
	// refused reports whether a Write after Close was refused as the package
	// documents; nil with skipClose saying why it is not.
	refused   func(err error) bool
	skipClose string
	// skipCount says why delivered records cannot be counted, when they cannot.
	skipCount string
	// bound drives a record past the package's bound and checks it was refused
	// and counted; nil with skipBound saying why the package has none.
	bound     func(t *testing.T)
	skipBound string
}

// overCount builds a middleware over a fresh countSink.
func overCount(wrap func(t *testing.T, down *countSink) corelogger.Sink) func(t *testing.T) (corelogger.Sink, func() int) {
	return func(t *testing.T) (corelogger.Sink, func() int) {
		down := &countSink{}
		return wrap(t, down), func() int { return int(down.n.Load()) }
	}
}

// must fails the test on err and returns s.
func must(t *testing.T, s corelogger.Sink, err error) corelogger.Sink {
	t.Helper()
	if err != nil {
		t.Fatalf("building the sink: %v", err)
	}
	return s
}

// noBound is the reason shared by the packages with no bound of their own.
const noBound = "no bound of its own: every record is delivered, or refused by its downstream"

// forwardsClose is the reason shared by the middlewares whose Close is their
// downstream's: the refusal checked is the downstream's, surfaced unchanged.
var conformanceRows = []conformanceRow{
	{
		pkg: "sink/console",
		build: func(t *testing.T) (corelogger.Sink, func() int) {
			buf := &lockedBuffer{}
			s, err := console.New(buf)
			return must(t, s, err), buf.lines
		},
		skipClose: "a console sink does not own its writer: Close releases nothing and a later Write still lands",
		skipBound: noBound,
	},
	{
		pkg: "sink/memory",
		build: func(*testing.T) (corelogger.Sink, func() int) {
			m := memory.NewMemory()
			return m, func() int { return len(m.Records()) }
		},
		skipClose: "the test sink records after Close too, so a test can read what a closing pipeline flushed",
		skipBound: "unbounded by design: it is the test sink, reset by the test",
	},
	{
		pkg: "sink/file",
		build: func(t *testing.T) (corelogger.Sink, func() int) {
			path := filepath.Join(t.TempDir(), "conf.log")
			s, err := file.New(path)
			return must(t, s, err), func() int { return fileLines(path) }
		},
		refused:   func(err error) bool { return errs.HasCode(err, corefile.CodeWriteFailed) },
		skipBound: noBound,
	},
	{
		pkg: "sink/syslog",
		build: func(t *testing.T) (corelogger.Sink, func() int) {
			conn := &countConn{}
			s, err := syslog.NewWithConfig("udp", "127.0.0.1:514", syslog.Config{Dialer: func(string, string) (net.Conn, error) { return conn, nil }})
			return must(t, s, err), func() int { return int(conn.frames.Load()) }
		},
		skipClose: "its transport is the caller's connection here; the refusal after Close is the connection's and syslog_sink_external_test pins it",
		skipBound: noBound,
	},
	{
		pkg: "middleware/async",
		build: overCount(func(_ *testing.T, down *countSink) corelogger.Sink {
			return async.New(down, async.Config{BufferSize: 1024, BatchMax: 8})
		}),
		refused: func(err error) bool { return errs.HasCode(err, coreasync.CodeAsyncStopped) },
		bound:   asyncBound,
	},
	{
		pkg: "middleware/encwrite",
		build: overCount(func(t *testing.T, down *countSink) corelogger.Sink {
			s, err := encwrite.NewEncWriter(encwrite.Config{Sink: down, Key: conformanceKey(t), Info: "conformance"})
			return must(t, s, err)
		}),
		refused:   func(err error) bool { return isRefusal(err) },
		skipBound: noBound,
	},
	{
		pkg: "middleware/failover",
		build: overCount(func(t *testing.T, down *countSink) corelogger.Sink {
			s, err := failover.New(down, &countSink{})
			return must(t, s, err)
		}),
		refused:   func(err error) bool { return err != nil },
		skipBound: noBound + "; its switch and heal are TestConformanceFailoverSwitchesAndHeals",
	},
	{
		pkg: "middleware/multi",
		build: overCount(func(_ *testing.T, down *countSink) corelogger.Sink {
			return multi.New(down)
		}),
		refused:   func(err error) bool { return isRefusal(err) },
		skipBound: noBound,
	},
	{
		pkg: "middleware/recover",
		build: overCount(func(t *testing.T, down *countSink) corelogger.Sink {
			s, err := logrecover.New(down)
			return must(t, s, err)
		}),
		refused:   func(err error) bool { return isRefusal(err) },
		skipBound: noBound,
	},
	{
		pkg: "middleware/route",
		build: overCount(func(_ *testing.T, down *countSink) corelogger.Sink {
			return route.New(down)
		}),
		refused:   func(err error) bool { return isRefusal(err) },
		skipBound: noBound,
	},
	{
		pkg: "middleware/sample",
		build: overCount(func(t *testing.T, down *countSink) corelogger.Sink {
			s, err := sample.NewKeyed(down, &sample.KeyedConfig{First: 1 << 20})
			return must(t, s, err)
		}),
		refused: func(err error) bool { return isRefusal(err) },
		bound:   sampleBound,
	},
	{
		pkg: "middleware/tee",
		build: overCount(func(_ *testing.T, down *countSink) corelogger.Sink {
			return tee.NewTeeSink(tee.Config{Primaries: []corelogger.Sink{down}})
		}),
		refused:   func(err error) bool { return isRefusal(err) },
		skipBound: noBound,
	},
	{
		pkg: "writer/console",
		build: func(t *testing.T) (corelogger.Sink, func() int) {
			s, err := consolewriter.Writer.Open(corewriter.ConsoleConfig{Stream: corewriter.ConsoleStdout, MinLevel: level.Error})
			return must(t, s, err), nil
		},
		skipClose: "the console writer is a console sink over the process's stream, which Close leaves open",
		skipCount: "the console writer writes to the process's stdout, which the test does not read; at MinLevel Error the Info records are gated",
		skipBound: noBound,
	},
	{
		pkg: "writer/dbsink",
		build: func(t *testing.T) (corelogger.Sink, func() int) {
			var rows atomic.Int64
			s := dbsink.Compose(func(_ context.Context, batch []corelogger.RecordEvent) error {
				rows.Add(int64(len(batch)))
				return nil
			}, dbsink.Config{MaxRows: 16, BufferSize: 1024})
			return s, func() int { return int(rows.Load()) }
		},
		refused:   func(err error) bool { return errs.HasCode(err, coreasync.CodeAsyncStopped) },
		skipBound: "its bound is the async ring in front of it — the middleware/async row's",
	},
	{
		pkg: "writer/file",
		build: func(t *testing.T) (corelogger.Sink, func() int) {
			path := filepath.Join(t.TempDir(), "writer.log")
			s, err := filewriter.Writer.Open(corewriter.FileConfig{Path: path})
			return must(t, s, err), func() int { return fileLines(path) }
		},
		refused:   func(err error) bool { return errs.HasCode(err, corefile.CodeWriteFailed) },
		skipBound: noBound,
	},
	{
		pkg: "writer/journald",
		build: func(t *testing.T) (corelogger.Sink, func() int) {
			conn := &countConn{}
			s, err := journald.Writer.Open(journald.Config{SocketPath: "/conformance/journal", Dialer: func(string, string) (net.Conn, error) { return conn, nil }, BufferSize: 1024})
			return must(t, s, err), func() int { return int(conn.frames.Load()) }
		},
		refused:   func(err error) bool { return errs.HasCode(err, coreasync.CodeAsyncStopped) },
		skipBound: "its bound is the async ring in front of it — the middleware/async row's",
	},
	{
		pkg: "writer/levelgate",
		build: overCount(func(_ *testing.T, down *countSink) corelogger.Sink {
			return levelgate.Floor(down, level.Debug)
		}),
		refused:   func(err error) bool { return isRefusal(err) },
		skipBound: noBound,
	},
	{
		pkg: "writer/nettransport",
		build: func(t *testing.T) (corelogger.Sink, func() int) {
			conn := &countConn{}
			s, err := nettransport.WriterTCP.Open(nettransport.NetConfig{Address: "conformance:1", Dialer: func(string, string) (net.Conn, error) { return conn, nil }, BufferSize: 1024})
			return must(t, s, err), nil
		},
		refused:   func(err error) bool { return errs.HasCode(err, coreasync.CodeAsyncStopped) },
		skipCount: "the tcp writer drains in batches (one writev per batch, #807), so frames at the connection are not records",
		skipBound: "its bound is the async ring in front of it — the middleware/async row's",
	},
	{
		pkg: "writer/otlpjson",
		build: func(t *testing.T) (corelogger.Sink, func() int) {
			exp := &countExporter{}
			s, err := otlpjson.Writer.Open(otlpjson.Config{Exporter: exp, BufferSize: 1024})
			return must(t, s, err), func() int { return int(exp.records.Load()) }
		},
		refused:   func(err error) bool { return errs.HasCode(err, coreasync.CodeAsyncStopped) },
		skipBound: "its bound is the async ring in front of it — the middleware/async row's",
	},
	{
		pkg: "writer/rotfile",
		build: func(t *testing.T) (corelogger.Sink, func() int) {
			path := filepath.Join(t.TempDir(), "rot.log")
			s, err := rotfile.Writer.Open(rotfile.Config{Path: path})
			return must(t, s, err), func() int { return fileLines(path) }
		},
		refused:   func(err error) bool { return errs.HasCode(err, corerotfile.CodeRotFileWriteFailed) },
		skipBound: "a size cap rotates rather than refuses: rotate_internal_test pins it",
	},
}

// conformanceKey is a fresh 32-byte key for the encwrite row.
func conformanceKey(t *testing.T) corecrypto.Key {
	t.Helper()
	k, err := corecrypto.NewKey(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return k
}

// conformanceRecord is the record every check emits.
var conformanceRecord = corelogger.RecordEvent{Time: time.Unix(1_700_000_000, 0), Level: level.Info, Message: "conformance"}

// TestConformance runs every row of the table.
func TestConformance(t *testing.T) {
	t.Parallel()
	for _, row := range conformanceRows {
		t.Run(row.pkg, func(t *testing.T) {
			t.Parallel()
			t.Run("flush-twice", func(t *testing.T) { flushTwice(t, row) })
			t.Run("concurrent-delivery", func(t *testing.T) { concurrentDelivery(t, row) })
			t.Run("refuses-after-close", func(t *testing.T) { refusesAfterClose(t, row) })
			t.Run("bound", func(t *testing.T) {
				if row.bound == nil {
					t.Skip(row.skipBound)
				}
				row.bound(t)
			})
		})
	}
}

// flushTwice: Flush is idempotent — twice in a row, both nil.
func flushTwice(t *testing.T, row conformanceRow) {
	s, _ := row.build(t)
	defer func() { _ = s.Close() }()
	for i := range 2 {
		if err := s.Flush(context.Background()); err != nil {
			t.Fatalf("Flush %d: %v", i+1, err)
		}
	}
}

// concurrentDelivery: 8 goroutines × 50 records, then Flush and Close: every
// record delivered, none twice.
func concurrentDelivery(t *testing.T, row conformanceRow) {
	const goroutines, each = 8, 50
	s, delivered := row.build(t)
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range each {
				if _, err := s.Write(context.Background(), conformanceRecord, []byte("conformance line\n")); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if delivered == nil {
		t.Skip(row.skipCount)
	}
	if got := delivered(); got != goroutines*each {
		t.Fatalf("delivered %d records, want %d", got, goroutines*each)
	}
}

// refusesAfterClose: a Write after Close is refused, as the row documents.
func refusesAfterClose(t *testing.T, row conformanceRow) {
	if row.refused == nil {
		t.Skip(row.skipClose)
	}
	s, _ := row.build(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.Write(context.Background(), conformanceRecord, []byte("late\n")); !row.refused(err) {
		t.Fatalf("Write after Close = %v, not the documented refusal", err)
	}
}

// asyncBound: a ring of one slot behind a blocked downstream refuses the
// record past its bound with BUFFER_FULL and counts it through OnDrop.
func asyncBound(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	var dropped atomic.Int64
	blocked := &funcSink{write: func() {
		once.Do(func() { close(started) })
		<-release
	}}
	s := async.New(blocked, async.Config{BufferSize: 1, OnDrop: func(n int) { dropped.Add(int64(n)) }})
	ctx := context.Background()
	_, _ = s.Write(ctx, conformanceRecord, []byte("a\n"))
	<-started
	_, _ = s.Write(ctx, conformanceRecord, []byte("b\n"))
	_, err := s.Write(ctx, conformanceRecord, []byte("c\n"))
	close(release)
	_ = s.Close()
	if !errs.HasCode(err, coreasync.CodeAsyncBufferFull) || dropped.Load() != 1 {
		t.Fatalf("past the bound: %v with %d drops counted, want BUFFER_FULL and 1", err, dropped.Load())
	}
}

// sampleBound: the keyed sampler past First drops — counted by Dropped.
func sampleBound(t *testing.T) {
	down := &countSink{}
	s, err := sample.NewKeyed(down, &sample.KeyedConfig{First: 2, Thereafter: 1 << 20, Clock: clock.NewManualClock(time.Unix(1, 0))})
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		_, _ = s.Write(context.Background(), conformanceRecord, []byte("x\n"))
	}
	if down.n.Load() != 2 || s.Dropped() != 3 {
		t.Fatalf("delivered %d, dropped %d; want 2 and 3", down.n.Load(), s.Dropped())
	}
}

// funcSink calls write on every Write.
type funcSink struct{ write func() }

func (f *funcSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	f.write()
	return len(p), nil
}
func (*funcSink) Flush(context.Context) error { return nil }
func (*funcSink) Close() error                { return nil }

// TestConformanceFailoverSwitchesAndHeals: a failing primary sends records to
// the secondary and is not tried again within the cooldown; once it is healthy
// and the cooldown has passed on the ManualClock, records return to it.
func TestConformanceFailoverSwitchesAndHeals(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Unix(1_000, 0))
	primary, secondary := &countSink{}, &countSink{}
	s, err := failover.NewWithConfig(failover.Config{Cooldown: time.Minute, Clock: clk}, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	write := func() {
		if _, err := s.Write(ctx, conformanceRecord, []byte("x\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	write()
	primary.fail.Store(true)
	write()
	primary.fail.Store(false)
	write()
	if primary.n.Load() != 1 || secondary.n.Load() != 2 {
		t.Fatalf("within the cooldown: primary %d, secondary %d; want 1 and 2", primary.n.Load(), secondary.n.Load())
	}
	clk.Advance(time.Minute)
	write()
	if primary.n.Load() != 2 {
		t.Fatalf("after the cooldown the healed primary has %d records, want 2", primary.n.Load())
	}
}

// TestConformanceCoversEveryPackage: every package of sink/, middleware/ and
// writer/ that holds production Go code has a row; a package added without
// one fails here. It reads the package's directory tree, which Bazel's
// sandbox does not hold: it skips there, and the go test lanes (test
// linux/386, the cross-platform jobs) hold the table to the tree.
func TestConformanceCoversEveryPackage(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("under Bazel: the logger's directory tree is not the test's runfiles")
	}
	rows := make(map[string]bool, len(conformanceRows))
	for _, r := range conformanceRows {
		rows[r.pkg] = true
	}
	for _, dir := range []string{"sink", "middleware", "writer"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir %s: %v", dir, err)
		}
		for _, e := range entries {
			if !e.IsDir() || !hasProductionGo(filepath.Join(dir, e.Name())) {
				continue
			}
			if pkg := dir + "/" + e.Name(); !rows[pkg] {
				t.Errorf("%s has no row in the conformance table", pkg)
			}
		}
	}
}

// hasProductionGo reports whether dir holds a .go file that is not a test.
func hasProductionGo(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			return true
		}
	}
	return false
}
