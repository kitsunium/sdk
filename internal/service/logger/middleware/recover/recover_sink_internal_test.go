package recover

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// noopDown never panics — exercises the happy-path branch of every method.
type noopDown struct{}

func (noopDown) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}
func (noopDown) Flush(_ context.Context) error { return nil }
func (noopDown) Close() error                  { return nil }

func Test_recoverSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"happy path returns nil"},
		{"happy path delegates byte count"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &recoverSink{downstream: noopDown{}}
			n, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if err != nil {
				t.Errorf("Write err = %v, want nil", err)
			}
			//: the wrapper must forward the downstream byte count verbatim;
			//: a single-byte payload must surface as n == 1, not a swallowed 0.
			if n != 1 {
				t.Errorf("Write n = %d, want 1", n)
			}
		})
	}
}

func Test_recoverSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"happy path returns nil"},
		{"defer-recovery is harmless on the no-panic path"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &recoverSink{downstream: noopDown{}}
			if err := s.Flush(t.Context()); err != nil {
				t.Errorf("Flush err = %v, want nil", err)
			}
		})
	}
}

func Test_recoverSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"happy path returns nil"},
		{"defer-recovery is harmless on the no-panic path"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &recoverSink{downstream: noopDown{}}
			if err := s.Close(); err != nil {
				t.Errorf("Close err = %v, want nil", err)
			}
		})
	}
}

// errNoopBoom is a minimal error fed to noopOnPanic with a non-nil value;
// the test only asserts the call returns without panicking.
type errNoopBoom struct{}

// Error renders the diagnostic marker used by the noopOnPanic test.
func (errNoopBoom) Error() (msg string) {
	//: static marker — content is not asserted.
	return "boom"
}

// Test_noopOnPanic locks the V34 no-op observer's contract: feeding it either
// a nil or a non-nil error must return silently and never panic, so the
// recover block can call the substituted hook unconditionally.
func Test_noopOnPanic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{"nil error is silently dropped", nil},
		{"non-nil error is silently dropped", errNoopBoom{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			panicked := false
			func() {
				//: trap any panic so the no-op contract is asserted directly.
				defer func() {
					//: flip the flag when the no-op unexpectedly panics.
					if r := recover(); r != nil {
						//: record the contract breach for the assertion below.
						panicked = true
					}
				}()
				noopOnPanic(tc.err)
			}()
			//: the documented contract is a silent return — any panic fails it.
			if panicked {
				t.Errorf("noopOnPanic(%v) panicked; contract requires silent no-op", tc.err)
			}
		})
	}
}
