package logger_test

import (
	"context"
	"testing"

	"github.com/kitsunium/sdk/internal/core/logger"
)

// batchStub satisfies both Sink and BatchSink — used by TestBatchSink to
// prove the interface composition compiles and a type assertion from Sink
// upgrades to BatchSink when implemented.
type batchStub struct{}

func (batchStub) Write(context.Context, logger.RecordEvent, []byte) (int, error) {
	//: stub returns a sentinel byte count so a future test can distinguish
	//: it from the WriteBatch path.
	return 0, nil
}

func (batchStub) Flush(context.Context) error {
	//: synchronous sink — Flush is a no-op.
	return nil
}

func (batchStub) Close() error {
	//: stateless stub — Close is a no-op.
	return nil
}

func (batchStub) WriteBatch(context.Context, []logger.RecordEvent, [][]byte) (int, error) {
	//: stub returns a recognisable batch-count sentinel.
	return 1, nil
}

// syncStub satisfies both Sink and SyncSink — used by TestSyncSink to prove
// the type assertion path.
type syncStub struct{}

func (syncStub) Write(context.Context, logger.RecordEvent, []byte) (int, error) {
	//: stub: zero bytes written so the result is unambiguous.
	return 0, nil
}

func (syncStub) Flush(context.Context) error {
	//: stub flush is a no-op.
	return nil
}

func (syncStub) Close() error {
	//: stub close is a no-op.
	return nil
}

func (syncStub) Sync(context.Context) error {
	//: stub durability call — always succeeds.
	return nil
}

// TestBatchSink verifies the capability sub-interface composes with Sink
// and a type assertion from Sink upgrades cleanly when the implementer
// satisfies BatchSink.
func TestBatchSink(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{
		{"batchStub satisfies BatchSink"},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the type assertion must succeed and WriteBatch is callable.
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		var s logger.Sink = batchStub{}
		bs, ok := s.(logger.BatchSink)
		//: assertion must succeed when the underlying type satisfies the
		//: capability sub-interface.
		if !ok {
			t.Fatalf("type assert Sink → BatchSink failed for batchStub")
		}
		//: WriteBatch must call through; the stub returns 1.
		n, err := bs.WriteBatch(t.Context(), nil, nil)
		if err != nil {
			t.Fatalf("WriteBatch err = %v", err)
		}
		if n != 1 {
			t.Fatalf("WriteBatch n = %d, want 1", n)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestSyncSink verifies SyncSink composes with Sink and Sync is callable
// via the upgraded interface.
func TestSyncSink(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{
		{"syncStub satisfies SyncSink"},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the type assertion must succeed and Sync is callable.
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		var s logger.Sink = syncStub{}
		ss, ok := s.(logger.SyncSink)
		//: assertion must succeed when the underlying type satisfies the
		//: capability sub-interface.
		if !ok {
			t.Fatalf("type assert Sink → SyncSink failed for syncStub")
		}
		//: Sync must return nil for the stub.
		if err := ss.Sync(t.Context()); err != nil {
			t.Fatalf("Sync err = %v", err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
