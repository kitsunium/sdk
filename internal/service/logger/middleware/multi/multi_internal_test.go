package multi

import (
	"context"
	"errors"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// stubSink is a controllable Sink for white-box branch coverage. Each method
// returns the error stored in its corresponding field, letting individual
// table cases drive every fanout decision branch.
type stubSink struct {
	// writeErr is returned by Write — drives the per-branch failure capture.
	writeErr error
	// flushErr is returned by Flush — drives the per-branch failure capture.
	flushErr error
	// closeErr is returned by Close — drives the per-branch failure capture.
	closeErr error
	// writes counts the Write calls — proves no short-circuit on failure.
	writes int
}

func (s *stubSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	s.writes++
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return len(p), nil
}

func (s *stubSink) Flush(_ context.Context) error { return s.flushErr }
func (s *stubSink) Close() error                  { return s.closeErr }

func Test_fanoutSink_zeroValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"fanoutSink with nil branches has length 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &fanoutSink{}
			if len(s.branches) != 0 {
				t.Errorf("fresh fanoutSink branches len = %d, want 0", len(s.branches))
			}
		})
	}
}

func Test_fanoutSink_Write(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name     string
		branches []corelogger.Sink
		wantN    int
		wantErr  bool
	}{
		{"empty fanout returns nil error and zero bytes accepted", nil, 0, false},
		{"single branch happy path returns its byte count", []corelogger.Sink{&stubSink{}}, 1, false},
		{"single branch error wraps via FanoutWriteFailed", []corelogger.Sink{&stubSink{writeErr: boom}}, 0, true},
		{"mixed branches: failure does not short-circuit success branches", []corelogger.Sink{&stubSink{writeErr: boom}, &stubSink{}}, 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &fanoutSink{branches: tc.branches}
			n, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if (err != nil) != tc.wantErr {
				t.Errorf("Write err = %v, wantErr = %v", err, tc.wantErr)
			}
			if n != tc.wantN {
				t.Errorf("Write n = %d, want %d", n, tc.wantN)
			}
		})
	}
}

func Test_fanoutSink_Write_NoShortCircuit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"failure on branch[0] still calls branch[1]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &stubSink{writeErr: errors.New("a-failed")}
			b := &stubSink{}
			s := &fanoutSink{branches: []corelogger.Sink{a, b}}
			_, _ = s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if a.writes != 1 || b.writes != 1 {
				t.Errorf("writes a=%d b=%d, want 1/1", a.writes, b.writes)
			}
		})
	}
}

func Test_fanoutSink_Flush(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name     string
		branches []corelogger.Sink
		wantErr  bool
	}{
		{"empty fanout Flush returns nil", nil, false},
		{"all-clean fanout Flush returns nil", []corelogger.Sink{&stubSink{}, &stubSink{}}, false},
		{"single failing branch surfaces the joined error", []corelogger.Sink{&stubSink{flushErr: boom}}, true},
		{"mixed branches: failure does not short-circuit", []corelogger.Sink{&stubSink{flushErr: boom}, &stubSink{}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &fanoutSink{branches: tc.branches}
			err := s.Flush(t.Context())
			if (err != nil) != tc.wantErr {
				t.Errorf("Flush err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func Test_fanoutSink_Close(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name     string
		branches []corelogger.Sink
		wantErr  bool
	}{
		{"empty fanout Close returns nil", nil, false},
		{"all-clean fanout Close returns nil", []corelogger.Sink{&stubSink{}, &stubSink{}}, false},
		{"single failing branch surfaces the joined error", []corelogger.Sink{&stubSink{closeErr: boom}}, true},
		{"mixed branches: failure does not short-circuit", []corelogger.Sink{&stubSink{closeErr: boom}, &stubSink{}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &fanoutSink{branches: tc.branches}
			err := s.Close()
			if (err != nil) != tc.wantErr {
				t.Errorf("Close err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
