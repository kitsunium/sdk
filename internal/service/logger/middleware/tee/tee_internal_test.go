// Package tee internal (white-box) tests.
package tee

import (
	"context"
	"errors"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// stubSink is a corelogger.Sink test double for white-box tests.
type stubSink struct {
	// n is the byte count Write reports on success.
	n int
	// writeErr drives the Write failure path.
	writeErr error
}

func (s *stubSink) Write(_ context.Context, _ corelogger.RecordEvent, _ []byte) (int, error) {
	//: a configured error simulates a primary failure.
	if s.writeErr != nil {
		//: report zero bytes on failure.
		return 0, s.writeErr
	}
	return s.n, nil
}

func (s *stubSink) Flush(_ context.Context) error { return nil }
func (s *stubSink) Close() error                  { return nil }

// fanCase drives a single fanOut scenario.
type fanCase struct {
	name         string
	primaries    []corelogger.Sink
	wantN        int
	wantAccepted bool
	wantCauses   int
}

func runFanCase(t *testing.T, primaries []corelogger.Sink, wantN int, wantAccepted bool, wantCauses int) {
	t.Helper()
	ts := NewTeeSink(Config{Primaries: primaries})
	n, accepted, causes := ts.fanOut(t.Context(), corelogger.RecordEvent{}, nil)
	//: the first accepted byte count must match.
	if n != wantN {
		t.Fatalf("n = %d, want %d", n, wantN)
	}
	//: the acceptance flag reflects whether any primary succeeded.
	if accepted != wantAccepted {
		t.Fatalf("accepted = %v, want %v", accepted, wantAccepted)
	}
	//: the collected cause count must match the failing primaries.
	if len(causes) != wantCauses {
		t.Fatalf("causes = %d, want %d", len(causes), wantCauses)
	}
}

func Test_TeeSink_fanOut(t *testing.T) {
	t.Parallel()
	//: table of fan-out scenarios over the primary list.
	tests := []fanCase{
		{
			name:         "all succeed",
			primaries:    []corelogger.Sink{&stubSink{n: 3}, &stubSink{n: 7}},
			wantN:        3,
			wantAccepted: true,
			wantCauses:   0,
		},
		{
			name:         "partial failure",
			primaries:    []corelogger.Sink{&stubSink{writeErr: errors.New("a")}, &stubSink{n: 5}},
			wantN:        5,
			wantAccepted: true,
			wantCauses:   1,
		},
		{
			name:         "all fail",
			primaries:    []corelogger.Sink{&stubSink{writeErr: errors.New("a")}, &stubSink{writeErr: errors.New("b")}},
			wantN:        0,
			wantAccepted: false,
			wantCauses:   2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runFanCase(t, tc.primaries, tc.wantN, tc.wantAccepted, tc.wantCauses)
		})
	}
}

// handleCase drives a single handleAllFailed scenario.
type handleCase struct {
	name    string
	spill   corelogger.Sink
	wantErr errs.Code
}

func runHandleCase(t *testing.T, spill corelogger.Sink, wantErr errs.Code) {
	t.Helper()
	ts := NewTeeSink(Config{Spill: spill})
	joined := errors.Join(errors.New("p1"), errors.New("p2"))
	err := ts.handleAllFailed(t.Context(), corelogger.RecordEvent{}, nil, joined)
	//: the typed code must match the expected outcome.
	if !errs.HasCode(err, wantErr) {
		t.Fatalf("err = %v, want code %v", err, wantErr)
	}
}

func Test_TeeSink_handleAllFailed(t *testing.T) {
	t.Parallel()
	//: table of spill outcomes for an all-primaries-failed record.
	tests := []handleCase{
		{
			name:    "no spill",
			spill:   nil,
			wantErr: CodeTeeAllBranchesFailed,
		},
		{
			name:    "spill ok",
			spill:   &stubSink{n: 1},
			wantErr: CodeTeeAllBranchesFailed,
		},
		{
			name:    "spill fails",
			spill:   &stubSink{writeErr: errors.New("spill down")},
			wantErr: CodeSpillFailed,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runHandleCase(t, tc.spill, tc.wantErr)
		})
	}
}

// wrapCase drives a single wrap-helper scenario.
type wrapCase struct {
	name    string
	wrap    func(error) error
	wantErr errs.Code
}

func runWrapCase(t *testing.T, wrap func(error) error, wantErr errs.Code) {
	t.Helper()
	cause := errors.New("cause")
	err := wrap(cause)
	//: the helper must stamp the expected dotted-quad code.
	if !errs.HasCode(err, wantErr) {
		t.Fatalf("err = %v, want code %v", err, wantErr)
	}
	//: the original cause must remain reachable through the wrap.
	if !errors.Is(err, cause) {
		t.Fatalf("cause not preserved by %v", err)
	}
}

func Test_wrapAllBranchesFailed(t *testing.T) {
	t.Parallel()
	//: table covering both wrap helpers via the function-value field.
	tests := []wrapCase{
		{
			name:    "all branches failed",
			wrap:    wrapAllBranchesFailed,
			wantErr: CodeTeeAllBranchesFailed,
		},
		{
			name:    "spill failed",
			wrap:    wrapSpillFailed,
			wantErr: CodeSpillFailed,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runWrapCase(t, tc.wrap, tc.wantErr)
		})
	}
}

// Test_wrapSpillFailed exercises the spill wrap helper directly so the
// per-function test synchronisation rule is satisfied.
func Test_wrapSpillFailed(t *testing.T) {
	t.Parallel()
	//: table over the single spill-wrap scenario.
	tests := []wrapCase{
		{
			name:    "spill failed direct",
			wrap:    wrapSpillFailed,
			wantErr: CodeSpillFailed,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runWrapCase(t, tc.wrap, tc.wantErr)
		})
	}
}
