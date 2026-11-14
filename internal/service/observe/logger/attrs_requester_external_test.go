package logger_test

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svclogger "github.com/kitsunium/sdk/internal/service/observe/logger"
	"github.com/kitsunium/sdk/internal/service/observe/logger/encoder"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/async"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/encwrite"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/failover"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/multi"
	mwrecover "github.com/kitsunium/sdk/internal/service/observe/logger/middleware/recover"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/route"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/sample"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/tee"
	"github.com/kitsunium/sdk/internal/service/observe/logger/sink/console"
	"github.com/kitsunium/sdk/internal/service/observe/logger/sink/file"
	"github.com/kitsunium/sdk/internal/service/observe/logger/sink/memory"
	"github.com/kitsunium/sdk/internal/service/observe/logger/writer/levelgate"
)

// attrsAt is the instant every record of this file carries, so two handlers
// write the same line.
var attrsAt = time.Date(2026, 11, 13, 9, 30, 0, 0, time.UTC)

// boundAttrsOdd is the number of bound attributes the sharing tests bind: 12
// AttrValues are 768 bytes, plus the 8-byte header of an object with
// pointers past 512 bytes, which the allocator rounds up to its 896-byte size
// class, so the array WithAttrs copies them into has room for a thirteenth
// (measured with go1.27: 12 gives cap 13, 32 gives 35) — the spare capacity a
// sink's append would write into if the handler did not cap the slice it
// shares.
const boundAttrsOdd int = 12

// appendingSink appends an attribute of its own to every record it is handed
// and keeps the keys it then holds — a sink that changes nothing but its own
// copy, as a tagging middleware would. It counts the records whose Attrs had
// room past their length.
type appendingSink struct {
	mu     sync.Mutex
	lines  []string
	keys   [][]string
	spared int
}

func (s *appendingSink) Write(_ context.Context, r corelogger.RecordEvent, p []byte) (int, error) {
	spare := cap(r.Attrs) > len(r.Attrs)
	r.Attrs = append(r.Attrs, corelogger.AttrValue{Key: "added", Value: corelogger.StringValue("by the sink")})
	keys := make([]string, 0, len(r.Attrs))
	for _, a := range r.Attrs {
		keys = append(keys, a.Key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	//: a shared array with room left is one a later append writes into.
	if spare {
		s.spared++
	}
	s.lines = append(s.lines, string(p))
	s.keys = append(s.keys, keys)
	return len(p), nil
}
func (*appendingSink) Flush(context.Context) error { return nil }
func (*appendingSink) Close() error                { return nil }

// bytesSink is lineSink that reads only the bytes and says so
// (corelogger.AttrsRequester).
type bytesSink struct{ lineSink }

func (*bytesSink) WantsAttrs() bool { return false }

// readingSink reads RecordEvent.Attrs and says nothing, as every Sink
// written before the port does.
type readingSink struct{}

func (readingSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}
func (readingSink) Flush(context.Context) error { return nil }
func (readingSink) Close() error                { return nil }

// attrKeys is the keys of attrs, in order.
func attrKeys(attrs []corelogger.AttrValue) []string {
	keys := make([]string, 0, len(attrs))
	for _, a := range attrs {
		keys = append(keys, a.Key)
	}
	return keys
}

// ownAttrs is n attributes of the record's own.
func ownAttrs(n int) []corelogger.AttrValue {
	attrs := make([]corelogger.AttrValue, n)
	for i := range attrs {
		attrs[i] = corelogger.AttrValue{Key: "own_" + strconv.Itoa(i), Value: corelogger.IntValue(i)}
	}
	return attrs
}

// TestASinkAppendingToTheSharedBoundAttrsLeavesThemIntact (#1016): a record
// with no attribute of its own carries the handler's bound array itself,
// capped at its length, so a sink appending to it — from several goroutines
// at once, under the race detector — reallocates and never writes into the
// array every later record carries. Every line stays the line a handler
// rendering the bound set per record writes, and every record reaches the
// sink with the bound keys, its own, and the sink's one addition.
func TestASinkAppendingToTheSharedBoundAttrsLeavesThemIntact(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		enc     corelogger.Encoder
		own     int
		grouped bool
	}
	tests := []tc{
		{name: "text, no attribute of its own", enc: encoder.NewText(clock.System)},
		{name: "json, no attribute of its own", enc: encoder.NewJSON(clock.System)},
		{name: "text, under a group", enc: encoder.NewText(clock.System), grouped: true},
		{name: "json, two of its own", enc: encoder.NewJSON(clock.System), own: 2},
		{name: "a plain encoder, no attribute of its own", enc: plainEncoder{encoder.NewText(clock.System)}},
	}
	const writers, perWriter = 4, 200
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink, want := &appendingSink{}, &lineSink{}
		h, err := svclogger.NewHandler(c.enc, sink, level.Debug)
		if err != nil {
			t.Fatal(err)
		}
		ref, err := svclogger.NewHandler(plainEncoder{c.enc}, want, level.Debug)
		if err != nil {
			t.Fatal(err)
		}
		bound := boundAttrs(boundAttrsOdd)
		h, ref = h.WithAttrs(bound), ref.WithAttrs(bound)
		//: a group makes the handler render its prefix again, sharing the same array.
		if c.grouped {
			h, ref = h.WithGroup("g"), ref.WithGroup("g")
		}
		r := corelogger.RecordEvent{Time: attrsAt, Level: level.Info, Message: "request served", Attrs: ownAttrs(c.own)}
		if err := ref.Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range writers {
			wg.Go(func() {
				for range perWriter {
					if herr := h.Handle(context.Background(), r); herr != nil {
						t.Error(herr)
						return
					}
				}
			})
		}
		wg.Wait()
		wantKeys := append(append(attrKeys(bound), attrKeys(r.Attrs)...), "added")
		//: every record, whichever goroutine wrote it, saw the same line and keys.
		for i, line := range sink.lines {
			if line != want.lines[0] {
				t.Fatalf("record %d:\ngot  %q\nwant %q", i, line, want.lines[0])
			}
			if !slices.Equal(sink.keys[i], wantKeys) {
				t.Fatalf("record %d: the sink held keys %v, want %v", i, sink.keys[i], wantKeys)
			}
		}
		//: the shared array is handed capped: no room for an append to land in.
		if c.own == 0 && sink.spared != 0 {
			t.Fatalf("%d of %d records shared the bound array with room past its length", sink.spared, len(sink.lines))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestASinkThatReadsOnlyTheBytesIsHandedTheRecordsOwnAttrs (#1016): a Sink
// answering false to corelogger.AttrsRequester gets the line a reading Sink
// gets and the record's own attributes alone — neither the bound ones nor a
// copy; a reading Sink still gets the bound ones first. An encoder that is no
// PrefixEncoder renders the merged set itself, which is then what the Sink is
// handed.
func TestASinkThatReadsOnlyTheBytesIsHandedTheRecordsOwnAttrs(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		enc        corelogger.Encoder
		bound, own int
		merged     bool
	}
	tests := []tc{
		{name: "text, bound and own", enc: encoder.NewText(clock.System), bound: 8, own: 3},
		{name: "json, bound and own", enc: encoder.NewJSON(clock.System), bound: 8, own: 3},
		{name: "text, bound alone", enc: encoder.NewText(clock.System), bound: 32},
		{name: "json, own alone", enc: encoder.NewJSON(clock.System), own: 3},
		{name: "nothing at all", enc: encoder.NewText(clock.System)},
		{name: "a plain encoder merges for itself", enc: plainEncoder{encoder.NewText(clock.System)}, bound: 8, own: 3, merged: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		bytes, reading := &bytesSink{}, &lineSink{}
		hb, err := svclogger.NewHandler(c.enc, bytes, level.Debug)
		if err != nil {
			t.Fatal(err)
		}
		hr, err := svclogger.NewHandler(c.enc, reading, level.Debug)
		if err != nil {
			t.Fatal(err)
		}
		bound := boundAttrs(c.bound)
		hb, hr = hb.WithAttrs(bound), hr.WithAttrs(bound)
		r := corelogger.RecordEvent{Time: attrsAt, Level: level.Info, Message: "request served", Attrs: ownAttrs(c.own)}
		if err := hb.Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		if err := hr.Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		//: the bytes do not depend on what the sink reads.
		if bytes.lines[0] != reading.lines[0] {
			t.Fatalf("line:\nbytes   %q\nreading %q", bytes.lines[0], reading.lines[0])
		}
		merged := append(attrKeys(bound), attrKeys(r.Attrs)...)
		want := attrKeys(r.Attrs)
		//: the encoder needed the merge, so the sink is handed it.
		if c.merged {
			want = merged
		}
		if !slices.Equal(bytes.keys[0], want) {
			t.Fatalf("the bytes sink got keys %v, want %v", bytes.keys[0], want)
		}
		if !slices.Equal(reading.keys[0], merged) {
			t.Fatalf("the reading sink got keys %v, want %v", reading.keys[0], merged)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// attrsTestKey is a fixed key for the encwrite middleware's case.
func attrsTestKey(t *testing.T) corecrypto.Key {
	t.Helper()
	raw := make([]byte, corecrypto.KeyLen)
	for i := range raw {
		raw[i] = byte(i)
	}
	k, err := corecrypto.NewKey(raw)
	if err != nil {
		t.Fatalf("corecrypto.NewKey err = %v", err)
	}
	return k
}

// TestEverySinkSaysWhetherItReadsTheAttrs (#1016): the SDK's byte sinks answer
// false to corelogger.AttrsRequester, a decorator answers for the sink it
// wraps, a fan-out true when any branch reads them, and a sink that reads
// them — or says nothing — true.
func TestEverySinkSaysWhetherItReadsTheAttrs(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		sink func(t *testing.T) corelogger.Sink
		want bool
	}
	bytesOnly := func(*testing.T) corelogger.Sink { return &bytesSink{} }
	must := func(t *testing.T, s corelogger.Sink, err error) corelogger.Sink {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	tests := []tc{
		{name: "a sink that says nothing", sink: func(*testing.T) corelogger.Sink { return readingSink{} }, want: true},
		{name: "console", sink: func(t *testing.T) corelogger.Sink {
			s, err := console.New(io.Discard)
			return must(t, s, err)
		}},
		{name: "file", sink: func(t *testing.T) corelogger.Sink {
			s, err := file.New(filepath.Join(t.TempDir(), "attrs.log"))
			s = must(t, s, err)
			t.Cleanup(func() { _ = s.Close() })
			return s
		}},
		{name: "memory", sink: func(*testing.T) corelogger.Sink { return memory.NewMemory() }, want: true},
		{name: "route", sink: func(*testing.T) corelogger.Sink { return route.New(&bytesSink{}) }, want: true},
		{name: "levelgate over bytes", sink: func(*testing.T) corelogger.Sink { return levelgate.New(&bytesSink{}, level.Info) }},
		{name: "levelgate over a reader", sink: func(*testing.T) corelogger.Sink { return levelgate.New(readingSink{}, level.Info) }, want: true},
		{name: "sample over bytes", sink: func(t *testing.T) corelogger.Sink {
			s, err := sample.New(&bytesSink{}, 2)
			return must(t, s, err)
		}},
		{name: "recover over a reader", sink: func(t *testing.T) corelogger.Sink {
			s, err := mwrecover.New(readingSink{})
			return must(t, s, err)
		}, want: true},
		{name: "recover over bytes", sink: func(t *testing.T) corelogger.Sink {
			s, err := mwrecover.New(&bytesSink{})
			return must(t, s, err)
		}},
		{name: "async over bytes", sink: func(t *testing.T) corelogger.Sink {
			s := async.New(&bytesSink{}, async.Config{BufferSize: 8})
			t.Cleanup(func() { _ = s.Close() })
			return s
		}},
		{name: "encwrite over bytes", sink: func(t *testing.T) corelogger.Sink {
			w, err := encwrite.NewEncWriter(encwrite.Config{Sink: &bytesSink{}, Key: attrsTestKey(t)})
			if err != nil {
				t.Fatal(err)
			}
			return w
		}},
		{name: "multi over bytes", sink: func(*testing.T) corelogger.Sink { return multi.New(&bytesSink{}, &bytesSink{}) }},
		{name: "multi with one reader", sink: func(*testing.T) corelogger.Sink { return multi.New(&bytesSink{}, readingSink{}) }, want: true},
		{name: "failover over bytes", sink: func(t *testing.T) corelogger.Sink {
			s, err := failover.New(&bytesSink{}, &bytesSink{})
			return must(t, s, err)
		}},
		{name: "failover with a reader behind", sink: func(t *testing.T) corelogger.Sink {
			s, err := failover.New(&bytesSink{}, readingSink{})
			return must(t, s, err)
		}, want: true},
		{name: "tee over bytes", sink: func(*testing.T) corelogger.Sink {
			return tee.NewTeeSink(tee.Config{Primaries: []corelogger.Sink{&bytesSink{}}, Spill: &bytesSink{}})
		}},
		{name: "tee spilling to a reader", sink: func(*testing.T) corelogger.Sink {
			return tee.NewTeeSink(tee.Config{Primaries: []corelogger.Sink{&bytesSink{}}, Spill: readingSink{}})
		}, want: true},
		{name: "tee with a reading primary", sink: func(*testing.T) corelogger.Sink {
			return tee.NewTeeSink(tee.Config{Primaries: []corelogger.Sink{readingSink{}}})
		}, want: true},
		{name: "a bytes sink", sink: bytesOnly},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := corelogger.SinkWantsAttrs(c.sink(t)); got != c.want {
			t.Fatalf("SinkWantsAttrs = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
