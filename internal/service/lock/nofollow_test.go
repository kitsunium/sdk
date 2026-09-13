package lock_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// This file carries NO build constraint, because the two halves of the
// indirection suite — nofollow_unix_test.go and nofollow_windows_test.go — are
// mutually exclusive and would otherwise each need their own copy of what they
// share. A second copy of an assertion is a second place for it to stop
// asserting.

// victimName is the lock every indirection case contends for. It is fixed so
// the digest that becomes the filename is fixed too, which is the property the
// attack rests on.
const victimName string = "victim"

// assertRefused pins a refusal: no lease, and the sentinel an operator acts on.
func assertRefused(t *testing.T, what string, lease corelock.Lease, held bool, err error) {
	t.Helper()
	//: held and lease are asserted apart from the code because they fail
	//: apart — a locker returning a lease AND an error is a different bug from
	//: one refusing with the wrong sentinel, and held is what a caller
	//: actually branches on.
	if held || lease != nil {
		t.Fatalf("TryAcquire over a %s = (%v, %v), want no lease", what, lease, held)
	}
	//: LOCK_BACKEND_FAILED would tell an operator to retry, which is the
	//: single wrong response to a deliberate substitution.
	if !errs.HasCode(err, svclock.CodeLockPathRedirected) {
		t.Fatalf("TryAcquire over a %s = %v, want LOCK_PATH_REDIRECTED", what, err)
	}
}
