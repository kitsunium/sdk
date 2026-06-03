package failover

import (
	"context"
	"errors"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// noopBranch is the trivial always-succeed Sink used by the internal tests.
type noopBranch struct{}

func (noopBranch) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}
func (noopBranch) Flush(_ context.Context) error { return nil }
func (noopBranch) Close() error                  { return nil }

// countingBranch is a Sink whose Write/Flush/Close return configurable
// errors and record how many times each seam was hit, so a test can assert
// short-circuit behaviour without a third-party mock framework.
type countingBranch struct {
	//: writes counts Write invocations to prove short-circuit traversal.
	writes int
	//: flushes / closes mirror that proof for the aggregate seams.
	flushes int
	closes  int
	//: werr / ferr / cerr drive the failure arm of each seam when non-nil.
	werr error
	ferr error
	cerr error
}

func (c *countingBranch) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	c.writes++
	//: a configured Write error exercises the cascade-to-next-branch path.
	if c.werr != nil {
		return 0, c.werr
	}
	return len(p), nil
}

func (c *countingBranch) Flush(_ context.Context) error {
	c.flushes++
	return c.ferr
}

func (c *countingBranch) Close() error {
	c.closes++
	return c.cerr
}

func Test_failoverSink_Write(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("branch boom")
	tests := []struct {
		name string
		//: chain is the ordered branch list under test.
		chain []corelogger.Sink
		//: second points at the branch expected to absorb the write so the
		//: test can assert its hit count after a leading failure.
		second *countingBranch
		//: wantSecondHits is the expected Write count on second (0 = skipped).
		wantSecondHits int
	}{
		{
			name:           "single happy branch succeeds without touching a fallback",
			chain:          []corelogger.Sink{noopBranch{}},
			second:         nil,
			wantSecondHits: 0,
		},
		{
			name: "leading failure cascades to the second branch and stops there",
			//: first branch always fails so traversal must reach index 1.
			chain:          nil, // built below to share the second pointer.
			second:         &countingBranch{},
			wantSecondHits: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			chain := tc.chain
			//: the cascade row builds its chain here to wire the shared pointer.
			if chain == nil {
				chain = []corelogger.Sink{&countingBranch{werr: errBoom}, tc.second}
			}
			s := &failoverSink{chain: chain}
			payload := []byte("hello")
			n, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, payload)
			//: the first success (or fallback success) must yield a clean error.
			if err != nil {
				t.Errorf("Write err = %v, want nil", err)
			}
			//: a clean Write returns the exact byte count the winning branch wrote.
			if n != len(payload) {
				t.Errorf("Write n = %d, want %d", n, len(payload))
			}
			//: the fallback branch must be hit exactly once when traversal reaches it.
			if tc.second != nil && tc.second.writes != tc.wantSecondHits {
				t.Errorf("second.writes = %d, want %d", tc.second.writes, tc.wantSecondHits)
			}
		})
	}
}

func Test_failoverSink_Flush(t *testing.T) {
	t.Parallel()
	errFlush := errors.New("flush boom")
	tests := []struct {
		name string
		//: ferr drives the second branch into the failure arm when non-nil.
		ferr error
		//: wantErr is the sentinel the joined result must wrap (nil = clean).
		wantErr error
	}{
		{"single-branch chain flushes cleanly", nil, nil},
		{"a failing branch surfaces its error after every branch flushed", errFlush, errFlush},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &countingBranch{}
			b := &countingBranch{ferr: tc.ferr}
			s := &failoverSink{chain: []corelogger.Sink{a, b}}
			err := s.Flush(t.Context())
			//: the failure arm must surface the joined branch error via errors.Is.
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("Flush err = %v, want it to wrap %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Errorf("Flush err = %v, want nil", err)
			}
			//: Flush never short-circuits — both branches are hit exactly once.
			if a.flushes != 1 || b.flushes != 1 {
				t.Errorf("flush counts: a=%d b=%d, want both 1", a.flushes, b.flushes)
			}
		})
	}
}

func Test_failoverSink_Close(t *testing.T) {
	t.Parallel()
	errClose := errors.New("close boom")
	tests := []struct {
		name string
		//: cerr drives the second branch into the failure arm when non-nil.
		cerr error
		//: wantErr is the sentinel the joined result must wrap (nil = clean).
		wantErr error
	}{
		{"single-branch chain closes cleanly", nil, nil},
		{"a failing branch surfaces its error after every branch closed", errClose, errClose},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &countingBranch{}
			b := &countingBranch{cerr: tc.cerr}
			s := &failoverSink{chain: []corelogger.Sink{a, b}}
			err := s.Close()
			//: the failure arm must surface the joined branch error via errors.Is.
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("Close err = %v, want it to wrap %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Errorf("Close err = %v, want nil", err)
			}
			//: Close never short-circuits — both branches are hit exactly once.
			if a.closes != 1 || b.closes != 1 {
				t.Errorf("close counts: a=%d b=%d, want both 1", a.closes, b.closes)
			}
		})
	}
}

func Test_failoverSink_zeroValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"failoverSink zero value has empty chain"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &failoverSink{}
			if len(s.chain) != 0 {
				t.Errorf("chain len = %d, want 0", len(s.chain))
			}
		})
	}
}

func Test_failoverSink_Write_byteCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: payload's length is the byte count the winning branch must echo.
		payload []byte
	}{
		{"a known-length payload round-trips its byte count", []byte("seventeen bytes!!")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a lone always-succeed branch echoes len(payload) as its byte count.
			s := &failoverSink{chain: []corelogger.Sink{noopBranch{}}}
			n, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, tc.payload)
			if err != nil {
				t.Fatalf("Write err = %v, want nil", err)
			}
			//: the returned count must equal the payload length, not a constant.
			if n != len(tc.payload) {
				t.Errorf("Write n = %d, want %d", n, len(tc.payload))
			}
		})
	}
}

func Test_failoverSink_Write_shortCircuitMiddle(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("primary boom")
	tests := []struct {
		name string
	}{
		{"a mid-chain success stops traversal before the third branch"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: first fails, second succeeds, third must never be reached.
			first := &countingBranch{werr: errBoom}
			second := &countingBranch{}
			third := &countingBranch{}
			s := &failoverSink{chain: []corelogger.Sink{first, second, third}}
			if _, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x")); err != nil {
				t.Fatalf("Write err = %v, want nil", err)
			}
			//: the middle success must short-circuit — the tail stays untouched.
			if third.writes != 0 {
				t.Errorf("third.writes = %d, want 0 (mid-chain success must short-circuit)", third.writes)
			}
			//: sanity: the winning middle branch was indeed reached once.
			if second.writes != 1 {
				t.Errorf("second.writes = %d, want 1", second.writes)
			}
		})
	}
}

// Test_failoverSink_Close_dedupSharedSink is the V33 regression: a sink instance
// reused across multiple chain slots must be closed exactly once so a
// non-idempotent terminal sink does not surface a spurious double-close error.
// Before the dedup fix this asserted closes == 1 and failed (closes == 2).
func Test_failoverSink_Close_dedupSharedSink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"shared sink across two chain slots is closed once (V33)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: one instance referenced by two chain slots — the V33 hazard.
			shared := &countingBranch{}
			s := &failoverSink{chain: []corelogger.Sink{shared, shared}}
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

// Test_failoverSink_Flush_dedupSharedSink is the V33 regression for Flush: a
// sink reused across chain slots must be flushed exactly once (no double fsync).
func Test_failoverSink_Flush_dedupSharedSink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"shared sink across two chain slots is flushed once (V33)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: one instance referenced by two chain slots — the V33 hazard.
			shared := &countingBranch{}
			s := &failoverSink{chain: []corelogger.Sink{shared, shared}}
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
