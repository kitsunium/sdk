package clickhouse

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	chdriver "github.com/ClickHouse/clickhouse-go/v2"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// : compile-time proof the wrapper satisfies the Sink port (kept in the test
// : file per KTN-IFACE-ASSERT-PLACEMENT).
var _ corelogger.Sink = (*chSink)(nil)

// errInnerBoom is a generic inner-Close failure used to exercise the wrapper's
// error-precedence path.
var errInnerBoom = errors.New("inner boom")

// recordingSink is a fake inner Sink that counts delegated calls and returns a
// configurable Close error, so the wrapper is testable without a real database.
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

// offlineDB opens a lazy ClickHouse database/sql handle (no connection) for the
// wrapper tests.
func offlineDB(t *testing.T) *sql.DB {
	t.Helper()
	//: OpenDB is lazy — it builds the pool without connecting.
	return chdriver.OpenDB(&chdriver.Options{Addr: []string{"localhost:9000"}})
}

func Test_newCHSink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"pairs the chain with its handle"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newCHSink(&recordingSink{}, offlineDB(t))
			//: the wrapper must be constructed and closeable.
			if cerr := s.Close(); cerr != nil {
				t.Errorf("%s: Close: %v", tc.name, cerr)
			}
		})
	}
}

func Test_chSink_Write(t *testing.T) {
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
			s := newCHSink(inner, offlineDB(t))
			t.Cleanup(func() {
				//: release the wrapper (and its lazy pool) after the case.
				if cerr := s.Close(); cerr != nil {
					t.Errorf("%s: cleanup Close: %v", tc.name, cerr)
				}
			})
			//: a Write must reach the inner chain and report the byte count.
			if n, err := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("hi")); err != nil || n != 2 || inner.writes != 1 {
				t.Errorf("%s: n=%d err=%v writes=%d", tc.name, n, err, inner.writes)
			}
		})
	}
}

func Test_chSink_Flush(t *testing.T) {
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
			s := newCHSink(inner, offlineDB(t))
			t.Cleanup(func() {
				//: release the wrapper after the case.
				if cerr := s.Close(); cerr != nil {
					t.Errorf("%s: cleanup Close: %v", tc.name, cerr)
				}
			})
			//: a Flush must reach the inner chain.
			if err := s.Flush(t.Context()); err != nil || inner.flushes != 1 {
				t.Errorf("%s: err=%v flushes=%d", tc.name, err, inner.flushes)
			}
		})
	}
}

func Test_chSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		closeErr error
		wantErr  bool
	}{
		{"clean close releases both", nil, false},
		{"inner error takes precedence", errInnerBoom, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inner := &recordingSink{closeErr: tc.closeErr}
			s := newCHSink(inner, offlineDB(t))
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
