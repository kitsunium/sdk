package route_test

import (
	"context"
	"sync/atomic"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/route"
)

type recordingSink struct {
	writes atomic.Int64
}

func (r *recordingSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	r.writes.Add(1)
	return len(p), nil
}

func (r *recordingSink) Flush(_ context.Context) error { return nil }
func (r *recordingSink) Close() error                  { return nil }

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		recLevel level.Level
		wantHits string
	}{
		{"warn matches the warn route", level.Warn, "warn"},
		{"info falls through to fallback", level.Info, "fallback"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			warnSink := &recordingSink{}
			fallback := &recordingSink{}
			r := route.New(fallback, route.Params{
				When: route.LevelAtLeast(level.Warn),
				Sink: warnSink,
			})
			//: incomplete Params is silently skipped — asserting via the
			//: fallback hit count proves the entry was dropped (no match
			//: ⇒ fallback receives the write).
			incompleteSink := &recordingSink{}
			incompleteFallback := &recordingSink{}
			rIncomplete := route.New(incompleteFallback, route.Params{When: nil, Sink: warnSink})
			if _, err := rIncomplete.Write(t.Context(), corelogger.RecordEvent{Level: level.Warn}, []byte("y")); err != nil {
				t.Errorf("incomplete Params Write err = %v", err)
			}
			if incompleteFallback.writes.Load() != 1 {
				t.Errorf("incomplete Params: fallback writes = %d, want 1", incompleteFallback.writes.Load())
			}
			if incompleteSink.writes.Load() != 0 {
				t.Errorf("incomplete Params: warnSink should not have received writes via rIncomplete")
			}
			rec := corelogger.RecordEvent{Level: tc.recLevel}
			if _, err := r.Write(t.Context(), rec, []byte("x")); err != nil {
				t.Errorf("Write err = %v", err)
			}
			switch tc.wantHits {
			case "warn":
				if warnSink.writes.Load() != 1 || fallback.writes.Load() != 0 {
					t.Errorf("warn route hit count wrong: warn=%d fallback=%d", warnSink.writes.Load(), fallback.writes.Load())
				}
			case "fallback":
				if warnSink.writes.Load() != 0 || fallback.writes.Load() != 1 {
					t.Errorf("fallback hit count wrong: warn=%d fallback=%d", warnSink.writes.Load(), fallback.writes.Load())
				}
			}
		})
	}
}

func TestRouterNoMatchWithoutFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"empty router returns NoMatch sentinel"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := route.New(nil)
			_, err := r.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if !errs.HasCode(err, route.CodeRouteNoMatch) {
				t.Errorf("err = %v, want NoMatch", err)
			}
		})
	}
}

func TestRouter_FlushAndClose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Flush + Close hit every route + fallback"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &recordingSink{}
			b := &recordingSink{}
			r := route.New(b, route.Params{When: route.LevelAtLeast(level.Error), Sink: a})
			if err := r.Flush(t.Context()); err != nil {
				t.Errorf("Flush err = %v", err)
			}
			if err := r.Close(); err != nil {
				t.Errorf("Close err = %v", err)
			}
		})
	}
}

func TestNoMatchSentinel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"NoMatch carries 0.3.18.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !errs.HasCode(route.NoMatch, route.CodeRouteNoMatch) {
				t.Errorf("HasCode(NoMatch) = false")
			}
		})
	}
}
