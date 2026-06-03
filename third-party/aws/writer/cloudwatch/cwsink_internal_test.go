package cloudwatch

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fakePutter records delivered batches so the batching tests need no network.
type fakePutter struct {
	mu      sync.Mutex
	batches [][]cwEvent
	err     error
}

func (f *fakePutter) putEvents(_ context.Context, events []cwEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	//: a configured error simulates a delivery failure.
	if f.err != nil {
		return f.err
	}
	//: snapshot the batch so later reuse cannot alias a recorded delivery.
	f.batches = append(f.batches, slices.Clone(events))
	return nil
}

func (f *fakePutter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.batches)
}

func writeLine(t *testing.T, s *cwSink, line string) {
	t.Helper()
	//: a Write must always report the full payload accepted.
	if n, err := s.Write(t.Context(), corelogger.RecordEvent{}, []byte(line)); err != nil || n != len(line) {
		t.Fatalf("Write(%q)=(%d,%v) want (%d,nil)", line, n, err, len(line))
	}
}

func Test_newCWSink(t *testing.T) {
	t.Parallel()
	type tc struct {
		name           string
		maxBatchEvents int
		onError        func(error)
	}
	tests := []tc{
		{"zero cap + nil onError apply defaults", 0, nil},
		{"explicit cap + hook are honoured", 4, func(error) {}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s := newCWSink((&fakePutter{}).putEvents, c.maxBatchEvents, 0, c.onError)
		//: the constructor must always yield a usable sink with its batcher + hook wired.
		if s == nil || s.batch == nil || s.deliver == nil || s.onError == nil {
			t.Errorf("%s: newCWSink gave batch-nil=%v deliver-nil=%v onError-nil=%v", c.name, s.batch == nil, s.deliver == nil, s.onError == nil)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_cwSink_Write(t *testing.T) {
	t.Parallel()
	type tc struct {
		name           string
		maxBatchEvents int
		lines          int
		wantMinBatches int
	}
	tests := []tc{
		{"reaching the event cap forces a delivery", 2, 2, 1},
		{"below the cap defers the delivery", 100, 1, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		fp := &fakePutter{}
		s := newCWSink(fp.putEvents, c.maxBatchEvents, 0, nil)
		for range c.lines {
			writeLine(t, s, "x\n")
		}
		//: the cap-trigger arm delivers inline; the under-cap arm defers.
		if fp.count() < c.wantMinBatches {
			t.Errorf("%s: batches=%d want >= %d", c.name, fp.count(), c.wantMinBatches)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_cwSink_WriteTimestamp(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		ts   time.Time
	}
	//: a recent explicit time stays inside the PutLogEvents 14d/2h window, so the
	//: V85 filter keeps it; an ancient fixed time would now be dropped.
	fixed := time.Now().Add(-time.Hour)
	tests := []tc{
		{"explicit record time is kept", fixed},
		{"zero record time falls back to now", time.Time{}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		fp := &fakePutter{}
		s := newCWSink(fp.putEvents, 0, 0, nil)
		//: write one record carrying the row's timestamp.
		if _, err := s.Write(t.Context(), corelogger.RecordEvent{Time: c.ts}, []byte("m\n")); err != nil {
			t.Fatalf("%s: Write: %v", c.name, err)
		}
		if err := s.Flush(t.Context()); err != nil {
			t.Fatalf("%s: Flush: %v", c.name, err)
		}
		//: the delivered event must carry a non-zero timestamp either way.
		if fp.batches[0][0].ts.IsZero() {
			t.Errorf("%s: delivered event has a zero timestamp", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_cwSink_Flush(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		lines       int
		wantBatches int
	}
	tests := []tc{
		{"buffered events deliver as one batch", 3, 1},
		{"empty buffer flush is a no-op", 0, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		fp := &fakePutter{}
		s := newCWSink(fp.putEvents, 0, 0, nil)
		for range c.lines {
			writeLine(t, s, "x\n")
		}
		if err := s.Flush(t.Context()); err != nil {
			t.Fatalf("%s: Flush: %v", c.name, err)
		}
		//: a flush delivers exactly one batch (or none when empty).
		if fp.count() != c.wantBatches {
			t.Errorf("%s: batches=%d want %d", c.name, fp.count(), c.wantBatches)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_cwSink_deliverBatch(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"out-of-order record times deliver chronologically (PutLogEvents requires it)"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		fp := &fakePutter{}
		//: large cap so the three writes buffer into a single Flush batch.
		s := newCWSink(fp.putEvents, 0, 0, nil)
		//: a recent base keeps every event inside the V85 14d/2h window.
		base := time.Now().Add(-time.Minute)
		//: write the records OUT of chronological order (t+2, t+0, t+1).
		for _, off := range []time.Duration{2 * time.Second, 0, 1 * time.Second} {
			if _, err := s.Write(t.Context(), corelogger.RecordEvent{Time: base.Add(off)}, []byte("m\n")); err != nil {
				t.Fatalf("Write: %v", err)
			}
		}
		if err := s.Flush(t.Context()); err != nil {
			t.Fatalf("Flush: %v", err)
		}
		//: the delivered batch must be sorted ascending by timestamp.
		batch := fp.batches[0]
		if !slices.IsSortedFunc(batch, func(a, b cwEvent) int { return a.ts.Compare(b.ts) }) {
			t.Errorf("delivered batch not chronological: %v", batch)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_cwSink_deliverBatchSortRace(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"concurrent writers + ticker keep every delivered batch chronological"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		fp := &fakePutter{}
		//: a fast ticker races the producers so batches flush concurrently.
		s := newCWSink(fp.putEvents, 4, time.Millisecond, nil)
		//: a recent base keeps every ms-offset event inside the V85 window.
		base := time.Now().Add(-time.Second)
		ctx := t.Context()
		// produce writes 50 descending-timestamp events for one producer; taking
		// off as a parameter keeps the loop var off the closure's escape path.
		produce := func(off int) {
			for i := range 50 {
				ts := base.Add(time.Duration(off*100-i) * time.Millisecond)
				//: out-of-order arrival times exercise the closure's reorder.
				if _, err := s.Write(ctx, corelogger.RecordEvent{Time: ts}, []byte("m\n")); err != nil {
					t.Errorf("Write: %v", err)
				}
			}
		}
		// Producer goroutines append out-of-order events concurrently; each
		// exits after 50 writes and wg.Wait joins them before Close, so none
		// outlive the test.
		var wg sync.WaitGroup
		for p := range 6 {
			wg.Go(func() { produce(p) })
		}
		wg.Wait()
		//: Close drains the tail and joins the ticker goroutine.
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		//: every delivered batch must be chronological — the closure sort holds
		//: even under concurrent Add + ticker flushing.
		fp.mu.Lock()
		defer fp.mu.Unlock()
		for i, b := range fp.batches {
			//: each delivered batch must be ascending by timestamp.
			if !slices.IsSortedFunc(b, func(a, c cwEvent) int { return a.ts.Compare(c.ts) }) {
				t.Errorf("batch %d not chronological: %v", i, b)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_cwSink_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		flushEvery time.Duration
	}
	tests := []tc{
		{"close flushes the final batch (no ticker)", 0},
		{"close stops the ticker and flushes", 5 * time.Millisecond},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		fp := &fakePutter{}
		s := newCWSink(fp.putEvents, 0, c.flushEvery, nil)
		writeLine(t, s, "final\n")
		//: Close must deliver the buffered events and join any ticker.
		if err := s.Close(); err != nil {
			t.Fatalf("%s: Close: %v", c.name, err)
		}
		if fp.count() == 0 {
			t.Errorf("%s: Close did not flush the final batch", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_cwSink_onError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a cap-triggered delivery failure is routed to onError"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		boom := errors.New("put boom")
		var mu sync.Mutex
		var gotErr error
		//: cap=1 makes the first Write cross the cap and deliver inline.
		fp := &fakePutter{err: boom}
		s := newCWSink(fp.putEvents, 1, 0, func(e error) {
			mu.Lock()
			gotErr = e
			mu.Unlock()
		})
		writeLine(t, s, "x\n")
		//: the background routing must have observed the delivery failure.
		mu.Lock()
		seen := gotErr
		mu.Unlock()
		if !errors.Is(seen, boom) {
			t.Errorf("onError saw %v want %v", seen, boom)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_cwSink_flushError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"Flush propagates the delivery error to the caller"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		boom := errors.New("flush boom")
		fp := &fakePutter{err: boom}
		s := newCWSink(fp.putEvents, 0, 0, nil)
		writeLine(t, s, "x\n")
		//: the caller-facing Flush propagates rather than routing to onError.
		err := s.Flush(t.Context())
		if !errors.Is(err, boom) {
			t.Errorf("Flush err=%v want %v", err, boom)
		}
		//: errors.Is must reach the typed PutFailed sentinel too.
		if !errors.Is(err, PutFailed) {
			t.Errorf("Flush err=%v does not match PutFailed", err)
		}
		//: the wrapped error must carry PutFailed's I/O exit code (74), not the
		//: default 70 — proves WrapParams.ExitCode is honoured on the wrap path.
		if got := errs.ExitCodeOf(err); got != exitIOErr {
			t.Errorf("ExitCodeOf=%d want %d", got, exitIOErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_cwSink_CloseError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"Close propagates the delivery error to the caller"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		boom := errors.New("close boom")
		fp := &fakePutter{err: boom}
		s := newCWSink(fp.putEvents, 0, 0, nil)
		writeLine(t, s, "x\n")
		//: Close flushes the final batch; the failed delivery must reach the caller
		//: (like Flush) rather than being routed to onError.
		err := s.Close()
		if !errors.Is(err, boom) {
			t.Errorf("Close err=%v want %v", err, boom)
		}
		//: errors.Is must reach the typed PutFailed sentinel too.
		if !errors.Is(err, PutFailed) {
			t.Errorf("Close err=%v does not match PutFailed", err)
		}
		//: the wrapped error must carry PutFailed's I/O exit code (74), not the
		//: default 70 — Close shares deliverBatch's wrap with Flush.
		if got := errs.ExitCodeOf(err); got != exitIOErr {
			t.Errorf("ExitCodeOf=%d want %d", got, exitIOErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// batchWeight sums a delivered batch's PutLogEvents payload weight (the same
// message+26/event accounting the byte cap uses), so a test can assert no
// delivered batch exceeds the 1 MiB ceiling.
func batchWeight(b []cwEvent) int64 {
	var w int64
	for _, e := range b {
		w += eventWeight(e)
	}
	return w
}

// Test_eventWeight pins the per-event byte accounting: message length plus the
// fixed 26-byte CloudWatch overhead (V84).
func Test_eventWeight(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		msg  string
		want int64
	}
	tests := []tc{
		{"empty message is pure overhead", "", int64(eventOverheadBytes)},
		{"message length adds to the overhead", "abcd", int64(4 + eventOverheadBytes)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the weight must be the message bytes plus the fixed overhead.
		if got := eventWeight(cwEvent{msg: c.msg}); got != c.want {
			t.Errorf("%s: eventWeight=%d want %d", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_batchFull checks each PutLogEvents ceiling independently forces a cut:
// the event count, the summed byte weight, and the 24h span (V84/V85).
func Test_batchFull(t *testing.T) {
	t.Parallel()
	base := time.Unix(1700000000, 0)
	type tc struct {
		name  string
		count int
		bytes int64
		next  int64
		first time.Time
		cand  time.Time
		want  bool
	}
	tests := []tc{
		{"under every ceiling stays open", 1, 10, 10, base, base.Add(time.Hour), false},
		{"reaching the event count cuts", defaultMaxBatchEvents, 0, 1, base, base, true},
		{"crossing the byte cap cuts", 1, maxBatchBytes, 1, base, base, true},
		{"crossing the 24h span cuts", 1, 0, 0, base, base.Add(maxBatchSpan + time.Second), true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: batchFull must report true exactly when a ceiling would be breached.
		if got := batchFull(c.count, c.bytes, c.next, c.first, c.cand); got != c.want {
			t.Errorf("%s: batchFull=%v want %v", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_splitBatch verifies the sorted-batch partitioner emits sub-batches that
// each honour the count, byte, and 24h-span ceilings while preserving order.
func Test_splitBatch(t *testing.T) {
	t.Parallel()
	base := time.Unix(1700000000, 0)
	type tc struct {
		name      string
		events    []cwEvent
		wantParts int
	}
	tests := []tc{
		{"a single in-bounds event is one sub-batch", []cwEvent{{ts: base, msg: "a"}}, 1},
		{
			"a >24h span is cut into two sub-batches",
			[]cwEvent{{ts: base, msg: "a"}, {ts: base.Add(25 * time.Hour), msg: "b"}},
			2,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		parts := splitBatch(c.events)
		//: the partition count must match the expected number of sub-batches.
		if len(parts) != c.wantParts {
			t.Fatalf("%s: parts=%d want %d", c.name, len(parts), c.wantParts)
		}
		//: every sub-batch must stay within the span ceiling.
		for i, p := range parts {
			if span := p[len(p)-1].ts.Sub(p[0].ts); span > maxBatchSpan {
				t.Errorf("%s: part %d span=%v exceeds %v", c.name, i, span, maxBatchSpan)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_cwSink_filterWindow checks the per-event window filter keeps in-bounds
// events and drops out-of-bounds ones (routing each to onError) (V85).
func Test_cwSink_filterWindow(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		age      time.Duration
		wantKept int
	}
	tests := []tc{
		{"in-window event is kept", -time.Hour, 1},
		{"too-old event is dropped", -20 * 24 * time.Hour, 0},
		{"too-future event is dropped", 5 * time.Hour, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var drops int
		s := newCWSink((&fakePutter{}).putEvents, 0, 0, func(error) { drops++ })
		now := time.Now()
		kept := s.filterWindow([]cwEvent{{ts: now.Add(c.age), msg: "m"}})
		//: the survivor count must match the window expectation.
		if len(kept) != c.wantKept {
			t.Errorf("%s: kept=%d want %d", c.name, len(kept), c.wantKept)
		}
		//: every dropped event must have been surfaced to onError.
		if drops != 1-c.wantKept {
			t.Errorf("%s: drops=%d want %d", c.name, drops, 1-c.wantKept)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_cwSink_byteCapSplit is the V84 regression: a burst of large records that
// stays under the event-COUNT cap but exceeds the 1 MiB byte ceiling must be
// split into multiple PutLogEvents calls, each within the byte cap. Before the
// WeightOf/MaxWeight fix the count-only batcher coalesced them into ONE oversized
// batch (count==1) that AWS would reject wholesale; after the fix every delivered
// batch is byte-bounded.
func Test_cwSink_byteCapSplit(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"large records under the count cap still split on the 1 MiB byte ceiling (V84)"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		fp := &fakePutter{}
		//: default count cap (1000) — far above the 4 records below, so only the
		//: byte cap can force a split. ~400 KiB each → 4 records ≈ 1.6 MiB > 1 MiB.
		s := newCWSink(fp.putEvents, 0, 0, nil)
		big := string(make([]byte, 400*1024))
		now := time.Now()
		for i := range 4 {
			//: in-window timestamps so the V85 filter keeps every record.
			if _, err := s.Write(t.Context(), corelogger.RecordEvent{Time: now.Add(time.Duration(i) * time.Second)}, []byte(big)); err != nil {
				t.Fatalf("Write: %v", err)
			}
		}
		if err := s.Flush(t.Context()); err != nil {
			t.Fatalf("Flush: %v", err)
		}
		//: a count-only batcher delivers exactly one oversized batch — the V84 bug.
		if fp.count() < 2 {
			t.Errorf("delivered %d batches, want >= 2 (byte cap must split)", fp.count())
		}
		//: no delivered batch may exceed the 1 MiB PutLogEvents byte ceiling.
		fp.mu.Lock()
		defer fp.mu.Unlock()
		for i, b := range fp.batches {
			if w := batchWeight(b); w > maxBatchBytes {
				t.Errorf("batch %d weight=%d exceeds cap %d", i, w, maxBatchBytes)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_cwSink_spanSplit is the V85 regression for the 24h-span ceiling: a single
// buffered batch whose events span more than 24h must be split so each delivered
// sub-batch spans at most 24h. Before the splitter, deliverBatch sorted but
// delivered one batch spanning >24h that AWS rejects wholesale.
func Test_cwSink_spanSplit(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a batch spanning more than 24h is split at 24h boundaries (V85)"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		fp := &fakePutter{}
		//: huge caps so neither count nor bytes can force the split — only span.
		s := newCWSink(fp.putEvents, 0, 0, nil)
		now := time.Now()
		//: three events at now-40h, now-10h, now-1h: the first two span 30h, so the
		//: batch must be cut; every event stays within the 14d past window.
		for _, age := range []time.Duration{-40 * time.Hour, -10 * time.Hour, -1 * time.Hour} {
			if _, err := s.Write(t.Context(), corelogger.RecordEvent{Time: now.Add(age)}, []byte("m\n")); err != nil {
				t.Fatalf("Write: %v", err)
			}
		}
		if err := s.Flush(t.Context()); err != nil {
			t.Fatalf("Flush: %v", err)
		}
		//: a non-splitting deliverBatch delivers one batch spanning 39h — the bug.
		if fp.count() < 2 {
			t.Errorf("delivered %d batches, want >= 2 (span cap must split)", fp.count())
		}
		//: no delivered batch may span more than 24h between its first and last event.
		fp.mu.Lock()
		defer fp.mu.Unlock()
		for i, b := range fp.batches {
			if span := b[len(b)-1].ts.Sub(b[0].ts); span > maxBatchSpan {
				t.Errorf("batch %d span=%v exceeds %v", i, span, maxBatchSpan)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_cwSink_windowDrop is the V85 regression for the per-event timestamp
// window: an event older than 14 days OR more than 2 hours in the future is
// dropped and routed to OnError as a typed EventRejected, while in-window events
// still ship. Before the filter, deliverBatch delivered every event and AWS
// would reject the whole batch.
func Test_cwSink_windowDrop(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		age      time.Duration
		wantDrop bool
	}
	tests := []tc{
		{"an event older than 14 days is dropped", -15 * 24 * time.Hour, true},
		{"an event more than 2h in the future is dropped", 3 * time.Hour, true},
		{"a recent in-window event is kept", -time.Hour, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		fp := &fakePutter{}
		var mu sync.Mutex
		var dropped []error
		s := newCWSink(fp.putEvents, 0, 0, func(e error) {
			mu.Lock()
			dropped = append(dropped, e)
			mu.Unlock()
		})
		now := time.Now()
		//: one always-valid anchor event proves in-window events still deliver.
		writeAt := func(age time.Duration) {
			if _, err := s.Write(t.Context(), corelogger.RecordEvent{Time: now.Add(age)}, []byte("m\n")); err != nil {
				t.Fatalf("%s: Write: %v", c.name, err)
			}
		}
		writeAt(-2 * time.Hour)
		writeAt(c.age)
		if err := s.Flush(t.Context()); err != nil {
			t.Fatalf("%s: Flush: %v", c.name, err)
		}
		mu.Lock()
		gotDrops := dropped
		mu.Unlock()
		//: the out-of-window arm must have surfaced exactly one typed EventRejected.
		if c.wantDrop {
			if len(gotDrops) != 1 || !errs.HasCode(gotDrops[0], CodeCWEventRejected) {
				t.Errorf("%s: drops=%v want one EventRejected", c.name, gotDrops)
			}
			//: the routed error must also match by Reason (no .Error() matching).
			if len(gotDrops) == 1 && !errs.HasReason(gotDrops[0], "EVENT_REJECTED") {
				t.Errorf("%s: drop reason mismatch", c.name)
			}
		}
		//: an in-window event must never be dropped.
		if !c.wantDrop && len(gotDrops) != 0 {
			t.Errorf("%s: in-window event dropped: %v", c.name, gotDrops)
		}
		//: the always-valid anchor must always ship, regardless of the second event.
		fp.mu.Lock()
		total := 0
		for _, b := range fp.batches {
			total += len(b)
		}
		fp.mu.Unlock()
		//: the in-window count is the anchor (1) plus the second event when kept.
		wantKept := 1
		if !c.wantDrop {
			wantKept = 2
		}
		if total != wantKept {
			t.Errorf("%s: delivered %d events, want %d", c.name, total, wantKept)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_cwSink_ticker(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"the ticker delivers a buffered batch without an explicit Flush"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		fp := &fakePutter{}
		s := newCWSink(fp.putEvents, 0, 2*time.Millisecond, nil)
		t.Cleanup(func() {
			//: surface a close failure rather than discarding it.
			if err := s.Close(); err != nil {
				t.Errorf("cleanup close: %v", err)
			}
		})
		writeLine(t, s, "tick\n")
		//: poll until the background ticker has delivered the batch.
		deadline := 0
		for fp.count() == 0 && deadline < 200 {
			time.Sleep(2 * time.Millisecond)
			deadline++
		}
		if fp.count() == 0 {
			t.Errorf("ticker did not flush within the deadline")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
