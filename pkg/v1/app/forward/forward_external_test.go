package forward_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/app/forward"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
)

// TestTheFacadeKeepsABatchAndRefusesWhatCannotFit pins the public surface a
// consumer types: the zero policy is DropOldest, a batch is kept and counted,
// and a refusal is the facade's sentinel under its code.
func TestTheFacadeKeepsABatchAndRefusesWhatCannotFit(t *testing.T) {
	if forward.FullPolicy(0) != forward.DropOldest {
		t.Fatal("the zero FullPolicy is not DropOldest")
	}
	broker, err := queue.NewMemory(queue.MemoryConfig{Policy: queue.Policy{VisibilityTimeout: 60e9, MaxDeliveries: 1000}})
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	fw, err := forward.New(context.Background(), forward.Config{
		Broker:     broker,
		Send:       func(context.Context, []byte) error { return nil },
		Retryable:  trace.OTLPRetryable,
		MaxBytes:   4,
		WhenFull:   forward.DropNewest,
		MaxBatches: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := fw.Enqueue(context.Background(), []byte("abc")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := fw.Enqueue(context.Background(), []byte("d")); !errors.Is(err, forward.Full) {
		t.Errorf("Enqueue past the bound = %v, want Full", err)
	}
	err = fw.Enqueue(context.Background(), []byte("abcde"))
	if code, _ := errs.CodeOf(err); !errors.Is(err, forward.BatchTooLarge) || code.String() != "0.3.93.3" {
		t.Errorf("an oversized batch = %v, want BatchTooLarge", err)
	}
	stats := fw.Stats()
	if stats.Pending != 1 || stats.Dropped != 2 {
		t.Errorf("Stats = %+v, want one pending and two dropped", stats)
	}
	if _, err := forward.New(context.Background(), forward.Config{}); !errors.Is(err, forward.Misconfigured) {
		t.Errorf("New(Config{}) = %v, want Misconfigured", err)
	}
	if _, err := forward.NewSpanExporter("spans", fw); err != nil {
		t.Errorf("NewSpanExporter: %v", err)
	}
	if _, err := forward.NewMetricExporter("metrics", nil); !errors.Is(err, forward.Misconfigured) {
		t.Errorf("NewMetricExporter(nil) = %v, want Misconfigured", err)
	}
}
