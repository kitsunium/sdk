package redis

import (
	"context"
	"errors"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// : compile-time proof the wrapper satisfies the Sink port (kept in the test
// : file per KTN-IFACE-ASSERT-PLACEMENT).
var _ corelogger.Sink = (*redisSink)(nil)

// errInnerBoom is a generic inner-Close failure used to exercise the wrapper's
// error-precedence path.
var errInnerBoom = errors.New("inner boom")

// errClientBoom is a generic client-close failure used to exercise the
// cmp.Or fallback when the inner chain drains cleanly.
var errClientBoom = errors.New("client boom")

// recordingSink is a fake inner Sink that counts delegated calls and returns a
// configurable Close error, so the wrapper is testable with no real client.
type recordingSink struct {
	writes   int
	flushes  int
	closed   bool
	closeErr error
}

func (s *recordingSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	s.writes++
	return len(p), nil
}

func (s *recordingSink) Flush(context.Context) error {
	s.flushes++
	return nil
}

func (s *recordingSink) Close() error {
	s.closed = true
	return s.closeErr
}

func Test_newRedisSink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"pairs the chain with its close seam"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newRedisSink(&recordingSink{}, func() error { return nil })
			//: the wrapper must be constructed and closeable.
			if cerr := s.Close(); cerr != nil {
				t.Errorf("%s: Close: %v", tc.name, cerr)
			}
		})
	}
}

func Test_redisSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"delegates to the inner chain"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inner := &recordingSink{}
			s := newRedisSink(inner, func() error { return nil })
			//: a Write must reach the inner chain and report the byte count.
			if n, err := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("hi")); err != nil || n != 2 || inner.writes != 1 {
				t.Errorf("%s: n=%d err=%v writes=%d", tc.name, n, err, inner.writes)
			}
		})
	}
}

func Test_redisSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"delegates to the inner chain"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inner := &recordingSink{}
			s := newRedisSink(inner, func() error { return nil })
			//: a Flush must reach the inner chain.
			if err := s.Flush(t.Context()); err != nil || inner.flushes != 1 {
				t.Errorf("%s: err=%v flushes=%d", tc.name, err, inner.flushes)
			}
		})
	}
}

func Test_redisSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		closeErr  error
		clientErr error
		wantErr   bool
	}{
		{"clean close releases both", nil, nil, false},
		{"inner error takes precedence", errInnerBoom, nil, true},
		{"closeFn error surfaces when inner succeeds", nil, errClientBoom, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inner := &recordingSink{closeErr: tc.closeErr}
			//: a per-case closeFn drives the cmp.Or second-argument arm.
			s := newRedisSink(inner, func() error { return tc.clientErr })
			err := s.Close()
			//: the inner chain must always be drained.
			if !inner.closed {
				t.Errorf("%s: inner not closed", tc.name)
			}
			//: the inner error (if any) must surface.
			if (err != nil) != tc.wantErr {
				t.Errorf("%s: Close err=%v wantErr=%v", tc.name, err, tc.wantErr)
			}
		})
	}
}
