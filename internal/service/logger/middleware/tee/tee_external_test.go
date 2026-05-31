// Package tee_test holds black-box tests for the tee middleware.
package tee_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/tee"
)

// fakeSink is a corelogger.Sink double for black-box tests.
type fakeSink struct {
	// gotBytes is a copy of the payload passed to the most recent Write.
	gotBytes []byte
	// wrote records whether Write was ever invoked.
	wrote bool
	// writeErr drives the Write failure path.
	writeErr error
	// flushErr drives the Flush failure path.
	flushErr error
	// closeErr drives the Close failure path.
	closeErr error
}

func (f *fakeSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	f.gotBytes = slices.Clone(p)
	f.wrote = true
	return len(p), f.writeErr
}

func (f *fakeSink) Flush(_ context.Context) error { return f.flushErr }
func (f *fakeSink) Close() error                  { return f.closeErr }

// writeCase drives a single end-to-end Write scenario.
type writeCase struct {
	name        string
	primaries   []*fakeSink
	spill       *fakeSink
	wantErr     errs.Code
	wantSpilled bool
}

func runWriteCase(t *testing.T, in []*fakeSink, spill *fakeSink, wantErr errs.Code, wantSpilled bool) {
	t.Helper()
	primaries := make([]corelogger.Sink, len(in))
	//: adapt the concrete doubles to the Sink interface slice.
	for i, p := range in {
		primaries[i] = p
	}
	cfg := tee.Config{Primaries: primaries}
	//: a nil spill double must stay nil on the Config.
	if spill != nil {
		cfg.Spill = spill
	}
	ts := tee.NewTeeSink(cfg)
	_, err := ts.Write(t.Context(), corelogger.RecordEvent{}, []byte("rec"))
	assertWriteErr(t, err, wantErr)
	//: the spill double must reflect whether the record was dead-lettered.
	if spill != nil && spill.wrote != wantSpilled {
		t.Fatalf("spilled = %v, want %v", spill.wrote, wantSpilled)
	}
}

func assertWriteErr(t *testing.T, err error, want errs.Code) {
	t.Helper()
	//: a zero want means the call must succeed.
	if want == 0 {
		//: the happy path must return a clean error.
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	//: otherwise the typed code must match.
	if !errs.HasCode(err, want) {
		t.Fatalf("code = %v, want %v", err, want)
	}
}

func Test_TeeSink_Write(t *testing.T) {
	t.Parallel()
	//: table of spill semantics over the primary + spill outcomes.
	tests := []writeCase{
		{
			name:      "one primary accepts no spill",
			primaries: []*fakeSink{{writeErr: errors.New("a")}, {}},
			spill:     &fakeSink{},
		},
		{
			name:        "all fail spilled",
			primaries:   []*fakeSink{{writeErr: errors.New("a")}, {writeErr: errors.New("b")}},
			spill:       &fakeSink{},
			wantErr:     tee.CodeTeeAllBranchesFailed,
			wantSpilled: true,
		},
		{
			name:      "all fail no spill",
			primaries: []*fakeSink{{writeErr: errors.New("a")}},
			spill:     nil,
			wantErr:   tee.CodeTeeAllBranchesFailed,
		},
		{
			name:        "all fail spill also fails",
			primaries:   []*fakeSink{{writeErr: errors.New("a")}},
			spill:       &fakeSink{writeErr: errors.New("spill down")},
			wantErr:     tee.CodeSpillFailed,
			wantSpilled: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runWriteCase(t, tc.primaries, tc.spill, tc.wantErr, tc.wantSpilled)
		})
	}
}

// newCase drives a single NewTeeSink / fan-out delivery scenario.
type newCase struct {
	name    string
	payload string
}

func runNewCase(t *testing.T, payload string) {
	t.Helper()
	a := &fakeSink{}
	b := &fakeSink{}
	ts := tee.NewTeeSink(tee.Config{Primaries: []corelogger.Sink{a, b}})
	_, err := ts.Write(t.Context(), corelogger.RecordEvent{}, []byte(payload))
	//: a successful fan-out must not error.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	//: both primaries must have received the same bytes.
	if string(a.gotBytes) != payload || string(b.gotBytes) != payload {
		t.Fatalf("fan-out mismatch: a=%q b=%q", a.gotBytes, b.gotBytes)
	}
}

// Test_NewTeeSink checks a record reaches every accepting primary.
func Test_NewTeeSink(t *testing.T) {
	t.Parallel()
	//: table of payloads fanned out to every primary.
	tests := []newCase{
		{name: "non-empty", payload: "hi"},
		{name: "empty", payload: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runNewCase(t, tc.payload)
		})
	}
}

// lifecycleCase drives a single Flush / Close aggregation scenario.
type lifecycleCase struct {
	name      string
	primErr   error
	spillErr  error
	wantError bool
}

func runFlushCase(t *testing.T, primErr, spillErr error, wantError bool) {
	t.Helper()
	p := &fakeSink{flushErr: primErr}
	s := &fakeSink{flushErr: spillErr}
	ts := tee.NewTeeSink(tee.Config{Primaries: []corelogger.Sink{p}, Spill: s})
	//: the aggregated error presence must match expectation.
	if (ts.Flush(t.Context()) != nil) != wantError {
		t.Fatalf("Flush wantError=%v", wantError)
	}
}

func Test_TeeSink_Flush(t *testing.T) {
	t.Parallel()
	//: table of flush aggregation scenarios.
	tests := []lifecycleCase{
		{name: "clean", primErr: nil, spillErr: nil, wantError: false},
		{name: "primary fails", primErr: errors.New("p"), spillErr: nil, wantError: true},
		{name: "both fail", primErr: errors.New("p"), spillErr: errors.New("s"), wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runFlushCase(t, tc.primErr, tc.spillErr, tc.wantError)
		})
	}
}

func runCloseCase(t *testing.T, primErr, spillErr error, wantError bool) {
	t.Helper()
	p := &fakeSink{closeErr: primErr}
	s := &fakeSink{closeErr: spillErr}
	ts := tee.NewTeeSink(tee.Config{Primaries: []corelogger.Sink{p}, Spill: s})
	//: the aggregated error presence must match expectation.
	if (ts.Close() != nil) != wantError {
		t.Fatalf("Close wantError=%v", wantError)
	}
}

func Test_TeeSink_Close(t *testing.T) {
	t.Parallel()
	//: table of close aggregation scenarios.
	tests := []lifecycleCase{
		{name: "clean", primErr: nil, spillErr: nil, wantError: false},
		{name: "primary fails", primErr: errors.New("p"), spillErr: nil, wantError: true},
		{name: "both fail", primErr: errors.New("p"), spillErr: errors.New("s"), wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCloseCase(t, tc.primErr, tc.spillErr, tc.wantError)
		})
	}
}

// sentinelCase drives a single sentinel-code assertion.
type sentinelCase struct {
	name string
	err  error
	code errs.Code
}

func runSentinelCase(t *testing.T, err error, code errs.Code) {
	t.Helper()
	//: HasCode must report the expected code on the sentinel.
	if !errs.HasCode(err, code) {
		t.Fatalf("HasCode(%v) failed", code)
	}
}

func Test_Sentinels(t *testing.T) {
	t.Parallel()
	//: table asserting each exported sentinel carries its dotted-quad code.
	tests := []sentinelCase{
		{name: "all-branches", err: tee.AllBranchesFailed, code: tee.CodeTeeAllBranchesFailed},
		{name: "spill", err: tee.SpillFailed, code: tee.CodeSpillFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runSentinelCase(t, tc.err, tc.code)
		})
	}
}
