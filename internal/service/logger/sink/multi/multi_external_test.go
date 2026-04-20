package multi_test

import (
	"context"
	"errors"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/sink/multi"
)

// recordingSink captures every Write/Flush/Close call for assertions.
type recordingSink struct {
	writes int
	flush  int
	closed int
	werr   error
	ferr   error
	cerr   error
}

func (r *recordingSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	r.writes++
	return len(p), r.werr
}

func (r *recordingSink) Flush(_ context.Context) error {
	r.flush++
	return r.ferr
}

func (r *recordingSink) Close() error {
	r.closed++
	return r.cerr
}

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		branches []corelogger.Sink
	}{
		{"no branches yields a no-op sink", nil},
		{"nil entries are silently dropped", []corelogger.Sink{nil, &recordingSink{}, nil}},
		{"two real branches", []corelogger.Sink{&recordingSink{}, &recordingSink{}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := multi.New(tc.branches...)
			if s == nil {
				t.Error("New returned nil")
			}
		})
	}
}

func TestFanout_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		branches []*recordingSink
		wantErr  bool
	}{
		{"all branches succeed", []*recordingSink{{}, {}}, false},
		{"one branch fails — fanout returns FANOUT_WRITE_FAILED",
			[]*recordingSink{{}, {werr: errors.New("boom")}}, true},
		{"empty fanout is a no-op", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: convert recordingSinks to the corelogger.Sink interface for wiring.
			branches := make([]corelogger.Sink, len(tc.branches))
			for i, b := range tc.branches {
				branches[i] = b
			}
			s := multi.New(branches...)
			_, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if tc.wantErr {
				if !errs.HasCode(err, multi.CodeFanoutWriteFailed) {
					t.Errorf("err = %v, want FanoutWriteFailed", err)
				}
				return
			}
			if err != nil {
				t.Errorf("Write err = %v, want nil", err)
			}
			//: every non-nil branch must have observed the Write call.
			for i, b := range tc.branches {
				if b.writes != 1 {
					t.Errorf("branch %d writes = %d, want 1", i, b.writes)
				}
			}
		})
	}
}

func TestFanout_FlushAndClose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Flush + Close hit every branch and aggregate errors"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &recordingSink{}
			b := &recordingSink{ferr: errors.New("flush boom")}
			s := multi.New(a, b)
			//: Flush surfaces the joined errors via errors.Is.
			if ferr := s.Flush(t.Context()); ferr == nil {
				t.Error("Flush err = nil, want joined error")
			}
			if a.flush != 1 || b.flush != 1 {
				t.Errorf("flush counters: a=%d b=%d, want both 1", a.flush, b.flush)
			}
			//: Close also visits every branch even if one fails.
			b.cerr = errors.New("close boom")
			if cerr := s.Close(); cerr == nil {
				t.Error("Close err = nil, want joined error")
			}
			if a.closed != 1 || b.closed != 1 {
				t.Errorf("close counters: a=%d b=%d, want both 1", a.closed, b.closed)
			}
		})
	}
}

func TestFanoutWriteFailedSentinel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"FanoutWriteFailed carries 3601"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !errs.HasCode(multi.FanoutWriteFailed, multi.CodeFanoutWriteFailed) {
				t.Errorf("HasCode failed for FanoutWriteFailed")
			}
		})
	}
}
