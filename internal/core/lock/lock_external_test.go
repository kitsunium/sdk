package lock_test

import (
	"context"
	"testing"
	"time"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// twoMethodLocker is a hand-written Locker carrying EXACTLY the two methods
// the port declares, and nothing else. It exists to fail compilation the day
// someone adds a third method to Locker.
type twoMethodLocker struct{}

func (twoMethodLocker) Acquire(context.Context, string) (corelock.Lease, error) {
	return nil, nil
}

func (twoMethodLocker) TryAcquire(context.Context, string) (corelock.Lease, bool, error) {
	return nil, false, nil
}

// threeMethodLease is the same guard for the Lease port.
type threeMethodLease struct{}

func (threeMethodLease) Fence() uint64 { return 1 }

func (threeMethodLease) Extend(context.Context) error { return nil }

func (threeMethodLease) Release(context.Context) error { return nil }

// TestTwoMethodDoubleStillSatisfiesLocker is the ADR 0039 guard for Locker,
// executable rather than documentary.
//
// pkg/v1/lock aliases Locker, and Go interfaces are structural: any downstream
// type with these two methods satisfies it today without importing anything or
// declaring intent. Adding a third method breaks every one of them at compile
// time with no deprecation window. A contributor who "tidies up" by folding a
// TTL parameter or a Deadline accessor into the port fails this named test
// before they reach review — which a comment saying "do not" would not have
// achieved.
func TestTwoMethodDoubleStillSatisfiesLocker(t *testing.T) {
	t.Parallel()
	locker := corelock.Locker(twoMethodLocker{})
	if _, held, err := locker.TryAcquire(t.Context(), "n"); held || err != nil {
		t.Fatalf("the double answered (%t, %v), want (false, nil)", held, err)
	}
}

// TestThreeMethodDoubleStillSatisfiesLease is the same guard for Lease. Fence,
// Extend and Release are the irreducible triple — identity, renewal, and
// giving it back. Deadline is deliberately NOT among them; see
// TestDeadlinerIsASiblingAndNotPartOfLease.
func TestThreeMethodDoubleStillSatisfiesLease(t *testing.T) {
	t.Parallel()
	lease := corelock.Lease(threeMethodLease{})
	if got := lease.Fence(); got != 1 {
		t.Fatalf("Fence = %d, want 1", got)
	}
}

// TestDeadlinerIsASiblingAndNotPartOfLease pins the ADR 0039 shape that
// carries the domain's most important distinction.
//
// Whether a lease can be taken away from a live holder is the one property
// that changes how a caller must be written, and it is answered by a type
// assertion rather than by a method returning a zero time. threeMethodLease
// has no Deadline method, so it must NOT satisfy Deadliner — if it ever did,
// the assertion would stop distinguishing anything.
func TestDeadlinerIsASiblingAndNotPartOfLease(t *testing.T) {
	t.Parallel()
	lease := corelock.Lease(threeMethodLease{})
	if _, ok := lease.(corelock.Deadliner); ok {
		t.Fatal("a three-method lease satisfies Deadliner — the sibling has been folded into the port")
	}
	//: and the positive half: a lease that DOES carry a deadline is reachable.
	expiring := corelock.Lease(expiringLease{})
	deadliner, ok := expiring.(corelock.Deadliner)
	if !ok {
		t.Fatal("a lease with a Deadline method does not satisfy Deadliner")
	}
	if deadliner.Deadline().IsZero() {
		t.Fatal("Deadline is the zero time — the sibling exists precisely to avoid that value")
	}
}

// expiringLease is a Lease that also carries a deadline, i.e. one that CAN be
// taken from a live holder.
type expiringLease struct{ threeMethodLease }

func (expiringLease) Deadline() time.Time { return time.Unix(1, 0).UTC() }

func TestSentinelsCarryTheirDocumentedCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		err      error
		code     errs.Code
		reason   string
		exitCode int
	}{
		{"misconfigured", corelock.LockMisconfigured, corelock.CodeLockMisconfigured, "LOCK_MISCONFIGURED", 78},
		{"not held", corelock.LockNotHeld, corelock.CodeLockNotHeld, "LOCK_NOT_HELD", 70},
		{"backend", corelock.LockBackendFailed, corelock.CodeLockBackendFailed, "LOCK_BACKEND_FAILED", 70},
		{"name", corelock.LockNameRejected, corelock.CodeLockNameRejected, "LOCK_NAME_REJECTED", 65},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, ok := errs.CodeOf(tc.err)
			if !ok || code != tc.code {
				t.Fatalf("CodeOf = (%v, %t), want (%v, true)", code, ok, tc.code)
			}
			if !errs.HasReason(tc.err, tc.reason) {
				t.Fatalf("reason is not %q", tc.reason)
			}
			if got := errs.ExitCodeOf(tc.err); got != tc.exitCode {
				t.Fatalf("ExitCodeOf = %d, want %d", got, tc.exitCode)
			}
		})
	}
}

// TestEveryCodeSitsInTheAllocatedRange guards the ADR 0052 block: 0.2.21.* is
// this package's slot, and a constant drifting outside it would collide with
// whoever owns the neighbouring range.
func TestEveryCodeSitsInTheAllocatedRange(t *testing.T) {
	t.Parallel()
	const wantPrefix uint32 = 0x00_02_15_00
	codes := []errs.Code{
		corelock.CodeLockMisconfigured,
		corelock.CodeLockNotHeld,
		corelock.CodeLockBackendFailed,
		corelock.CodeLockNameRejected,
	}
	for _, code := range codes {
		if uint32(code)&0xFF_FF_FF_00 != wantPrefix {
			t.Fatalf("code %v is outside the 0.2.21.* block", code)
		}
	}
}
