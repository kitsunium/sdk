package cloudwatch

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
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
		//: the constructor must always yield a usable sink with a positive cap.
		if s == nil || s.maxBatchEvents <= 0 || s.onError == nil {
			t.Errorf("%s: newCWSink gave cap=%d onError-nil=%v", c.name, s.maxBatchEvents, s.onError == nil)
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
	fixed := time.Unix(1700000000, 0)
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

func Test_cwSink_flushOnce(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		lines       int
		wantBatches int
	}
	tests := []tc{
		{"empty buffer delivers nothing", 0, 0},
		{"buffered events deliver once", 1, 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		fp := &fakePutter{}
		s := newCWSink(fp.putEvents, 0, 0, nil)
		for range c.lines {
			writeLine(t, s, "x\n")
		}
		//: a direct flushOnce delivers one batch (or none when empty).
		if err := s.flushOnce(t.Context()); err != nil {
			t.Fatalf("%s: flushOnce: %v", c.name, err)
		}
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
		var gotErr error
		//: cap=1 makes the first Write cross the cap and deliver inline.
		fp := &fakePutter{err: boom}
		s := newCWSink(fp.putEvents, 1, 0, func(e error) { gotErr = e })
		writeLine(t, s, "x\n")
		//: the background routing must have observed the delivery failure.
		if !errors.Is(gotErr, boom) {
			t.Errorf("onError saw %v want %v", gotErr, boom)
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
		if err := s.Flush(t.Context()); !errors.Is(err, boom) {
			t.Errorf("Flush err=%v want %v", err, boom)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_cwSink_loop(t *testing.T) {
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
