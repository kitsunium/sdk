package recover_test

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	recoversink "github.com/kitsunium/sdk/internal/service/logger/middleware/recover"
)

// panickingSink panics on every method to exercise the recovery branches.
type panickingSink struct{}

func (panickingSink) Write(_ context.Context, _ corelogger.RecordEvent, _ []byte) (int, error) {
	panic("write boom")
}
func (panickingSink) Flush(_ context.Context) error { panic("flush boom") }
func (panickingSink) Close() error                  { panic("close boom") }

// quietSink never panics — used to assert the happy path.
type quietSink struct{}

func (quietSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}
func (quietSink) Flush(_ context.Context) error { return nil }
func (quietSink) Close() error                  { return nil }

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		nilDown  bool
		wantCode errs.Code
	}{
		{"non-nil downstream succeeds", false, 0},
		{"nil downstream yields DownstreamNil", true, recoversink.CodeRecoverDownstreamNil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var down corelogger.Sink
			if !tc.nilDown {
				down = quietSink{}
			}
			s, err := recoversink.New(down)
			if tc.wantCode == 0 {
				if err != nil {
					t.Errorf("New err = %v, want nil", err)
				}
				if s == nil {
					t.Error("New returned nil sink on happy path")
				}
				return
			}
			if !errs.HasCode(err, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", err, tc.wantCode)
			}
		})
	}
}

func TestRecover_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		panic    bool
		wantCode errs.Code
	}{
		{"happy path delegates to downstream", false, 0},
		{"panic surfaces as Panicked", true, recoversink.CodeRecoverPanicked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var down corelogger.Sink = quietSink{}
			if tc.panic {
				down = panickingSink{}
			}
			s, err := recoversink.New(down)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			_, werr := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if tc.wantCode == 0 {
				if werr != nil {
					t.Errorf("Write err = %v, want nil", werr)
				}
				return
			}
			if !errs.HasCode(werr, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", werr, tc.wantCode)
			}
		})
	}
}

func TestRecover_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		panic    bool
		wantCode errs.Code
	}{
		{"happy path delegates to downstream", false, 0},
		{"panic surfaces as Panicked", true, recoversink.CodeRecoverPanicked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var down corelogger.Sink = quietSink{}
			if tc.panic {
				down = panickingSink{}
			}
			s, err := recoversink.New(down)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			ferr := s.Flush(t.Context())
			if tc.wantCode == 0 {
				if ferr != nil {
					t.Errorf("Flush err = %v, want nil", ferr)
				}
				return
			}
			if !errs.HasCode(ferr, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", ferr, tc.wantCode)
			}
		})
	}
}

func TestRecover_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		panic    bool
		wantCode errs.Code
	}{
		{"happy path delegates to downstream", false, 0},
		{"panic surfaces as Panicked", true, recoversink.CodeRecoverPanicked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var down corelogger.Sink = quietSink{}
			if tc.panic {
				down = panickingSink{}
			}
			s, err := recoversink.New(down)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			cerr := s.Close()
			if tc.wantCode == 0 {
				if cerr != nil {
					t.Errorf("Close err = %v, want nil", cerr)
				}
				return
			}
			if !errs.HasCode(cerr, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", cerr, tc.wantCode)
			}
		})
	}
}

func TestRecoverSentinels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"Panicked carries 0.3.21.1", recoversink.Panicked, recoversink.CodeRecoverPanicked},
		{"DownstreamNil carries 0.3.21.2", recoversink.DownstreamNil, recoversink.CodeRecoverDownstreamNil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !errs.HasCode(tc.err, tc.code) {
				t.Errorf("HasCode(%v, %d) = false", tc.err, tc.code)
			}
		})
	}
}
