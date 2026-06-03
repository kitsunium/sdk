package levelgate

import (
	"context"
	"errors"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// : the gate never originates errors; it only relays the inner sink's failures
// : verbatim, so plain stdlib sentinels model a transport-layer fault here. The
// : assertions use errors.Is so identity — not message text — is what is checked.
var (
	errInnerWrite = errors.New("inner write transport failure")
	errInnerFlush = errors.New("inner flush transport failure")
	errInnerClose = errors.New("inner close transport failure")
)

// errorSink is a hand-written Sink whose Write/Flush/Close outcomes are fully
// configurable so the gate's error-propagation seams can be driven directly.
type errorSink struct {
	// writeN is the byte count Write reports back to the gate.
	writeN int
	// writeErr is the error Write returns; nil means success.
	writeErr error
	// flushErr is the error Flush returns; nil means success.
	flushErr error
	// closeErr is the error Close returns; nil means success.
	closeErr error
	// writes counts how many times Write actually reached this sink.
	writes int
}

func (s *errorSink) Write(_ context.Context, _ corelogger.RecordEvent, _ []byte) (int, error) {
	//: record the delivery so drop-path tests can prove inner was never touched.
	s.writes++
	return s.writeN, s.writeErr
}
func (s *errorSink) Flush(_ context.Context) error { return s.flushErr }
func (s *errorSink) Close() error                  { return s.closeErr }

// countSink records how many writes/flushes/closes actually reached it.
type countSink struct {
	writes  int
	flushes int
	closes  int
}

func (s *countSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	//: count the delivery and accept the whole payload.
	s.writes++
	return len(p), nil
}
func (s *countSink) Flush(_ context.Context) error { s.flushes++; return nil }
func (s *countSink) Close() error                  { s.closes++; return nil }

func Test_gateSink_Write(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		min        level.Level
		recordLvl  level.Level
		wantPassed bool
	}
	tests := []tc{
		{"below floor drops", level.Error, level.Warn, false},
		{"at floor passes", level.Error, level.Error, true},
		{"above floor passes", level.Warn, level.Error, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		inner := &countSink{}
		//: white-box: build the gate directly to exercise the drop branch.
		gate := &gateSink{inner: inner, min: c.min}
		rec := corelogger.RecordEvent{Level: c.recordLvl}
		//: Write always succeeds; drops are a silent no-op.
		if n, err := gate.Write(t.Context(), rec, []byte("z")); err != nil || n != 1 {
			t.Fatalf("%s: Write=(%d,%v) want (1,nil)", c.name, n, err)
		}
		//: only at/above-floor records reach the wrapped sink.
		if (inner.writes == 1) != c.wantPassed {
			t.Errorf("%s: passed=%v want %v", c.name, inner.writes == 1, c.wantPassed)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_gateSink_Flush(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"flush forwards to inner"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		inner := &countSink{}
		gate := &gateSink{inner: inner, min: level.Error}
		//: the flush must reach the wrapped sink exactly once.
		if err := gate.Flush(t.Context()); err != nil || inner.flushes != 1 {
			t.Errorf("Flush: err=%v flushes=%d want nil,1", err, inner.flushes)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_gateSink_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"close forwards to inner"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		inner := &countSink{}
		gate := &gateSink{inner: inner, min: level.Error}
		//: the close must reach the wrapped sink exactly once.
		if err := gate.Close(); err != nil || inner.closes != 1 {
			t.Errorf("Close: err=%v closes=%d want nil,1", err, inner.closes)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_gateSink_Write_errorPassthrough(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		min        level.Level
		recordLvl  level.Level
		inner      *errorSink
		payload    []byte
		wantN      int
		wantErr    error
		wantWrites int
	}
	tests := []tc{
		{
			//: an at-floor record reaches inner, whose transport error must surface.
			name:       "inner write error propagates",
			min:        level.Warn,
			recordLvl:  level.Warn,
			inner:      &errorSink{writeN: 0, writeErr: errInnerWrite},
			payload:    []byte("abc"),
			wantN:      0,
			wantErr:    errInnerWrite,
			wantWrites: 1,
		},
		{
			//: a short count from inner must pass through unchanged, never padded.
			name:       "inner partial write propagates",
			min:        level.Warn,
			recordLvl:  level.Warn,
			inner:      &errorSink{writeN: 1, writeErr: nil},
			payload:    []byte("abc"),
			wantN:      1,
			wantErr:    nil,
			wantWrites: 1,
		},
		{
			//: a below-floor record is dropped before inner; inner stays untouched.
			name:       "drop path never calls inner",
			min:        level.Error,
			recordLvl:  level.Warn,
			inner:      &errorSink{writeN: 0, writeErr: errInnerWrite},
			payload:    []byte("abc"),
			wantN:      3,
			wantErr:    nil,
			wantWrites: 0,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: white-box: build the gate directly to exercise the drop branch.
		gate := &gateSink{inner: c.inner, min: c.min}
		rec := corelogger.RecordEvent{Level: c.recordLvl}
		n, err := gate.Write(t.Context(), rec, c.payload)
		//: the relayed byte count must match inner (or len(p) on the drop path).
		if n != c.wantN {
			t.Errorf("%s: n=%d want %d", c.name, n, c.wantN)
		}
		//: identity, not message text, decides whether the error matches.
		if !errors.Is(err, c.wantErr) {
			t.Errorf("%s: err=%v want %v", c.name, err, c.wantErr)
		}
		//: the drop path must not have delegated to the wrapped sink at all.
		if c.inner.writes != c.wantWrites {
			t.Errorf("%s: inner.writes=%d want %d", c.name, c.inner.writes, c.wantWrites)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_gateSink_Flush_errorPassthrough(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		wantErr error
	}
	tests := []tc{
		//: Flush buffers nothing, so inner's error must come back unaltered.
		{"inner flush error propagates", errInnerFlush},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		inner := &errorSink{flushErr: c.wantErr}
		gate := &gateSink{inner: inner, min: level.Error}
		//: identity check proves the gate relays inner's flush error verbatim.
		if err := gate.Flush(t.Context()); !errors.Is(err, c.wantErr) {
			t.Errorf("%s: Flush=%v want %v", c.name, err, c.wantErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_gateSink_Close_errorPassthrough(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		wantErr error
	}
	tests := []tc{
		//: Close owns no resources, so inner's error must come back unaltered.
		{"inner close error propagates", errInnerClose},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		inner := &errorSink{closeErr: c.wantErr}
		gate := &gateSink{inner: inner, min: level.Error}
		//: identity check proves the gate relays inner's close error verbatim.
		if err := gate.Close(); !errors.Is(err, c.wantErr) {
			t.Errorf("%s: Close=%v want %v", c.name, err, c.wantErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_gateSink_Write_debugFloor(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		recordLvl level.Level
	}
	tests := []tc{
		//: a record exactly at the Debug floor must pass the wrapped path.
		{"debug record at debug floor passes", level.Debug},
		//: an Info record sits above the Debug floor and must also pass.
		{"info record above debug floor passes", level.Info},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		inner := &errorSink{writeN: 2}
		//: min=Debug is below Info, so New wraps and the gate is exercised live.
		gate := &gateSink{inner: inner, min: level.Debug}
		rec := corelogger.RecordEvent{Level: c.recordLvl}
		//: the record clears the Debug floor, so inner must receive it once.
		if _, err := gate.Write(t.Context(), rec, []byte("xy")); err != nil {
			t.Fatalf("%s: Write err=%v want nil", c.name, err)
		}
		if inner.writes != 1 {
			t.Errorf("%s: inner.writes=%d want 1", c.name, inner.writes)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
