//go:build !race

// The fixture of encwrite's performance contract (design/sdk.yaml, budgets):
// it sets one call up and returns it, and the gen_perf_test.go kit gen writes
// beside this file counts its allocations against its budget. Race off.
package encwrite_test

import (
	"context"
	"slices"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/encwrite"
)

// perfDiscard is a downstream sink that keeps nothing, so the count is the
// middleware's alone.
type perfDiscard struct{}

func (perfDiscard) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}

func (perfDiscard) Flush(context.Context) error { return nil }

func (perfDiscard) Close() error { return nil }

// perfWriteErr keeps each call's result alive.
var perfWriteErr error

// perfWrite is Write's fixture (#813): a record of a log line's size sealed
// and framed for a downstream that keeps nothing — the cipher bound once at
// construction, the frame borrowed from the kernel's buffer pool.
func perfWrite(tb testing.TB) func() {
	key, err := corecrypto.NewKey(slices.Repeat([]byte{0x02}, corecrypto.KeyLen))
	if err != nil {
		tb.Fatalf("NewKey: %v", err)
	}
	w, err := encwrite.NewEncWriter(encwrite.Config{Sink: perfDiscard{}, Key: key})
	if err != nil {
		tb.Fatalf("NewEncWriter: %v", err)
	}
	ctx := context.Background()
	record := []byte(`time=2026-11-11T12:00:00Z level=INFO msg="request served" method=GET path=/v1/items status=200 dur=1.2ms`)
	return func() {
		_, perfWriteErr = w.Write(ctx, corelogger.RecordEvent{}, record)
	}
}
