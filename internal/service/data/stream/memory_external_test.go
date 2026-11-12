package stream_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	svcstream "github.com/kitsunium/sdk/internal/service/data/stream"
)

// TestNewMemoryRefusesAPolicyItCannotHonour: the core's guard, run at
// construction, so the double refuses what every durable engine refuses.
func TestNewMemoryRefusesAPolicyItCannotHonour(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		policy corestream.PolicyValue
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := svcstream.NewMemory(svcstream.MemoryConfig{Policy: c.policy})
		if !errs.HasCode(err, corestream.CodeStreamMisconfigured) {
			t.Fatalf("NewMemory(%+v) = %v, want STREAM_MISCONFIGURED", c.policy, err)
		}
	}
	for _, c := range []tc{
		{name: "negative partitions", policy: corestream.PolicyValue{Partitions: -1}},
		{name: "a wait with no ceiling", policy: corestream.PolicyValue{BlockTimeout: time.Second}},
		{name: "a ceiling below one record", policy: corestream.PolicyValue{MaxBytes: 1, MaxRecordBytes: 2}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestNewMemoryWorksAtTheZeroPolicyAndTheSystemClock: one partition, kept
// forever, stamped by the wall clock when no clock is given.
func TestNewMemoryWorksAtTheZeroPolicyAndTheSystemClock(t *testing.T) {
	t.Parallel()
	s, err := svcstream.NewMemory(svcstream.MemoryConfig{})
	if err != nil {
		t.Fatalf("NewMemory(zero) = %v, want nil", err)
	}
	if s.Partitions() != 1 {
		t.Fatalf("Partitions() = %d, want 1", s.Partitions())
	}
	before := time.Now()
	appendOK(t, s, record("x"))
	if at := readOK(t, s, 0, 0, 1)[0].AppendedAt; at.Before(before) || at.After(time.Now()) {
		t.Fatalf("AppendedAt = %s, want the wall clock's now", at)
	}
}

// TestAnAppendOnACancelledContextWritesNothing: the caller who gave up keeps
// its cause, and the partition keeps its next offset.
func TestAnAppendOnACancelledContextWritesNothing(t *testing.T) {
	t.Parallel()
	s := makeMemory(t, nil, corestream.PolicyValue{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Append(ctx, []corestream.RecordValue{record("x")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Append(cancelled) = %v, want context.Canceled", err)
	}
	if bounds := boundsOK(t, s, 0); bounds.Next != 0 {
		t.Fatalf("next = %d, want 0", bounds.Next)
	}
}
