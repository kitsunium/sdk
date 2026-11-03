//go:build !race

// The fixtures of the sample package's performance contracts (design/sdk.yaml,
// budgets): each sets one call up and returns it, and the gen_perf_test.go
// kit gen writes beside this file counts its allocations against its budget.
// Race off: the race detector allocates on its own; the alloc lane runs it.
package sample_test

import (
	"context"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/sample"
)

// discard drops every record and allocates nothing.
type discard struct{}

func (discard) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}
func (discard) Flush(context.Context) error { return nil }
func (discard) Close() error                { return nil }

// perfKeyedWrite is (*Keyed).Write's fixture: with Thereafter 2 the calls
// alternate between a kept record and a dropped one — both paths,
// on a key already in the table.
func perfKeyedWrite(tb testing.TB) func() {
	s, err := sample.NewKeyed(discard{}, &sample.KeyedConfig{First: 1, Thereafter: 2, Clock: clock.NewManualClock(time.Unix(1, 0))})
	if err != nil {
		tb.Fatalf("NewKeyed: %v", err)
	}
	ctx := context.Background()
	rec := corelogger.RecordEvent{Level: level.Info, Message: "connection refused"}
	payload := []byte("connection refused\n")
	//: the key enters the table outside the window.
	_, _ = s.Write(ctx, rec, payload)
	return func() {
		_, _ = s.Write(ctx, rec, payload)
	}
}
