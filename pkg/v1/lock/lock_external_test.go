package lock_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/lock"
)

// TestZeroTTLIsRefusedAtTheFacade pins, at the public edge, the refusal the
// whole domain is built on.
//
// This is the assertion a consumer's own test would make, so it is worth
// making here too: no combination of public calls produces a locker whose
// leases are born expired.
func TestZeroTTLIsRefusedAtTheFacade(t *testing.T) {
	t.Parallel()
	locker, err := lock.NewMemory(lock.MemoryConfig{})
	if locker != nil {
		t.Fatal("the facade built a locker from a zero TTL")
	}
	if !errs.HasReason(err, "LOCK_MISCONFIGURED") {
		t.Fatalf("NewMemory = %v, want LOCK_MISCONFIGURED", err)
	}
}

// TestTheAliasesAreTheInternalTypes is the ADR 0017/pkg-v1 contract: the
// public names are ALIASES, not new types, so a Lease produced by the service
// layer satisfies the public interface without conversion.
func TestTheAliasesAreTheInternalTypes(t *testing.T) {
	t.Parallel()
	locker, err := lock.NewMemory(lock.MemoryConfig{TTL: time.Minute})
	if err != nil {
		t.Fatalf("NewMemory = %v", err)
	}
	var lease lock.Lease
	lease, err = locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	if lease.Fence() == 0 {
		t.Fatal("Fence is 0 — the zero value must never name a live acquisition")
	}
	//: a memory lease CAN expire, so it must answer the Deadliner question.
	if _, ok := lease.(lock.Deadliner); !ok {
		t.Fatal("a memory lease does not satisfy the public Deadliner alias")
	}
	if relErr := lease.Release(t.Context()); relErr != nil {
		t.Fatalf("Release = %v", relErr)
	}
}

// TestAFileLeaseDoesNotAnswerDeadliner is the same assertion from the other
// side, at the public edge, because the difference between the two backends is
// the one thing a consumer must be able to discover without reading the SDK.
func TestAFileLeaseDoesNotAnswerDeadliner(t *testing.T) {
	t.Parallel()
	locker, err := lock.NewFileLocker(lock.FileConfig{Dir: t.TempDir()})
	if err != nil {
		t.Skipf("no file locker on this platform: %v", err)
	}
	lease, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	defer func() {
		if relErr := lease.Release(t.Context()); relErr != nil {
			t.Errorf("Release = %v, want nil", relErr)
		}
	}()
	if _, ok := lease.(lock.Deadliner); ok {
		t.Fatal("a file lease answers Deadliner — but a file lock has no deadline, so the value would be a fiction")
	}
}
