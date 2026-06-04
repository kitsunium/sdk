package route

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// dummyCtx silences the unused import warning when context is only used
// inside the noopRouteSink interface signatures.
var _ context.Context

// noopRouteSink is the trivial Sink used by the internal tests below.
type noopRouteSink struct{}

func (noopRouteSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}
func (noopRouteSink) Flush(_ context.Context) error { return nil }
func (noopRouteSink) Close() error                  { return nil }

func Test_routerSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		hasFb   bool
		wantErr bool
	}{
		{"empty router with nil fallback returns NoMatch", false, true},
		{"empty router with fallback delegates to it", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &routerSink{}
			if tc.hasFb {
				s.fallback = noopRouteSink{}
			}
			_, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if (err != nil) != tc.wantErr {
				t.Errorf("Write err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func Test_routerSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: nEntries seeds that many distinct entry sinks before Flush.
		nEntries int
		//: hasFb wires a distinct fallback sink.
		hasFb bool
		//: shareEntryFb reuses entry[0]'s sink as the fallback (V33 dedup path).
		shareEntryFb bool
	}{
		{"empty router Flush returns nil", 0, false, false},
		{"router with fallback Flush returns nil", 0, true, false},
		{"single entry Flush returns nil", 1, false, false},
		{"entry + distinct fallback Flush returns nil", 1, true, false},
		{"two distinct entries Flush returns nil", 2, false, false},
		{"two entries + fallback Flush returns nil", 2, true, false},
		{"entry shared with fallback flushes once (V33)", 1, false, true},
		{"two entries with shared fallback Flush returns nil (V33)", 2, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := buildRouter(tc.nEntries, tc.hasFb, tc.shareEntryFb)
			//: a healthy topology Flush must never surface an error.
			if err := s.Flush(t.Context()); err != nil {
				t.Errorf("Flush err = %v, want nil", err)
			}
		})
	}
}

func Test_routerSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: nEntries seeds that many distinct entry sinks before Close.
		nEntries int
		//: hasFb wires a distinct fallback sink.
		hasFb bool
		//: shareEntryFb reuses entry[0]'s sink as the fallback (V33 dedup path).
		shareEntryFb bool
	}{
		{"empty router Close returns nil", 0, false, false},
		{"router with fallback Close returns nil", 0, true, false},
		{"single entry Close returns nil", 1, false, false},
		{"entry + distinct fallback Close returns nil", 1, true, false},
		{"two distinct entries Close returns nil", 2, false, false},
		{"two entries + fallback Close returns nil", 2, true, false},
		{"entry shared with fallback closes once (V33)", 1, false, true},
		{"two entries with shared fallback Close returns nil (V33)", 2, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := buildRouter(tc.nEntries, tc.hasFb, tc.shareEntryFb)
			//: a healthy topology Close must never surface a double-close error.
			if err := s.Close(); err != nil {
				t.Errorf("Close err = %v, want nil", err)
			}
		})
	}
}

// buildRouter assembles a routerSink with nEntries noop entries plus optional
// fallback wiring, used by the Flush/Close table tests. When shareEntryFb is
// set, the first entry's sink doubles as the fallback to exercise the V33
// dedup branch.
func buildRouter(nEntries int, hasFb, shareEntryFb bool) *routerSink {
	s := &routerSink{}
	//: seed nEntries distinct entry sinks behind a match-all predicate.
	for range nEntries {
		s.entries = append(s.entries, Params{When: alwaysTrue, Sink: &countingRouteSink{}})
	}
	//: reuse entry[0]'s sink as the fallback to drive the dedup path.
	if shareEntryFb && nEntries > 0 {
		s.fallback = s.entries[0].Sink
		return s
	}
	//: otherwise wire a distinct fallback when requested.
	if hasFb {
		s.fallback = &countingRouteSink{}
	}
	return s
}

func Test_routerSink_zeroValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"routerSink zero value has empty entries and nil fallback"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &routerSink{}
			if len(s.entries) != 0 {
				t.Errorf("entries len = %d, want 0", len(s.entries))
			}
			if s.fallback != nil {
				t.Errorf("fallback = %v, want nil", s.fallback)
			}
		})
	}
}

// countingRouteSink is a Sink that records how many times Flush and Close were
// forwarded to it, so the V33 dedup regression can assert a shared instance is
// touched exactly once.
type countingRouteSink struct {
	//: flushes counts Flush forwards — proves dedup forwards once (V33).
	flushes int
	//: closes counts Close forwards — proves dedup forwards once (V33).
	closes int
}

func (c *countingRouteSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}
func (c *countingRouteSink) Flush(_ context.Context) error { c.flushes++; return nil }
func (c *countingRouteSink) Close() error                  { c.closes++; return nil }

// alwaysTrue is a predicate matching every record — used so entries are valid
// (a nil When would be dropped at construction).
func alwaysTrue(_ corelogger.RecordEvent) bool { return true }

// Test_routerSink_Close_dedupSharedSink is the V33 regression: a sink instance
// wired into several routes (or a route and the fallback) must be closed exactly
// once so a non-idempotent terminal sink does not surface a double-close error.
func Test_routerSink_Close_dedupSharedSink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		shareInBoth bool
	}{
		{"sink shared across two entries is closed once (V33)", false},
		{"sink shared between an entry and the fallback is closed once (V33)", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: one instance reused across slots — the V33 double-close hazard.
			shared := &countingRouteSink{}
			s := &routerSink{entries: []Params{{When: alwaysTrue, Sink: shared}}}
			//: either wire the same sink into a second entry or into the fallback.
			if tc.shareInBoth {
				s.fallback = shared
			} else {
				s.entries = append(s.entries, Params{When: alwaysTrue, Sink: shared})
			}
			//: a clean Close must not surface a double-close error.
			if err := s.Close(); err != nil {
				t.Errorf("Close err = %v, want nil", err)
			}
			//: dedup forwards Close to the shared instance exactly once.
			if shared.closes != 1 {
				t.Errorf("shared.closes = %d, want 1", shared.closes)
			}
		})
	}
}

// Test_routerSink_Flush_dedupSharedSink is the V33 regression for Flush: a sink
// reused across routes or shared with the fallback is flushed exactly once.
func Test_routerSink_Flush_dedupSharedSink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		shareInBoth bool
	}{
		{"sink shared across two entries is flushed once (V33)", false},
		{"sink shared between an entry and the fallback is flushed once (V33)", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: one instance reused across slots — the V33 double-fsync hazard.
			shared := &countingRouteSink{}
			s := &routerSink{entries: []Params{{When: alwaysTrue, Sink: shared}}}
			//: either wire the same sink into a second entry or into the fallback.
			if tc.shareInBoth {
				s.fallback = shared
			} else {
				s.entries = append(s.entries, Params{When: alwaysTrue, Sink: shared})
			}
			//: a clean Flush must not surface an error and must dedup the sink.
			if err := s.Flush(t.Context()); err != nil {
				t.Errorf("Flush err = %v, want nil", err)
			}
			//: dedup forwards Flush to the shared instance exactly once.
			if shared.flushes != 1 {
				t.Errorf("shared.flushes = %d, want 1", shared.flushes)
			}
		})
	}
}
