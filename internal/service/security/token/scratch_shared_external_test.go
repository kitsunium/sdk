//go:build !race

// The shared-scratch test runs in the race-off lane on purpose: it shares ONE
// VerifyScratch across goroutines, which the port documents as a misuse, to
// prove the misuse fails closed. The race detector would only report the
// sharing the test commits deliberately; what is under test is that no
// interleaving of two verifications through one keyed MAC can accept a
// forged token. The race-off allocation lane (tools/alloc-lane-targets.txt)
// runs this package.
package token_test

import (
	"bytes"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	coretoken "github.com/kitsunium/sdk/internal/core/security/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctoken "github.com/kitsunium/sdk/internal/service/security/token"
)

// TestASharedScratchNeverAcceptsAForgery shares one scratch across N
// goroutines verifying a mix of valid tokens and forged ones — each forgery a
// valid token's signature copied onto another payload, the exact input an
// interleaved MAC would wrongly accept. One accepted forgery fails the test.
func TestASharedScratchNeverAcceptsAForgery(t *testing.T) {
	rig := newIntoRig(t, svctoken.VerifierConfig{AllowMissingExpiry: true})
	valid := make([][]byte, 8)
	forged := make([][]byte, len(valid))
	//: valid tokens, and their signatures moved onto other claims.
	for i := range valid {
		valid[i] = rig.token(`{"sub":"user-` + itoaShared(i) + `"}`)
		sig := valid[i][bytes.LastIndexByte(valid[i], '.'):]
		other := rig.token(`{"sub":"admin-` + itoaShared(i) + `"}`)
		forged[i] = append(bytes.Clone(other[:bytes.LastIndexByte(other, '.')]), sig...)
	}
	shared := rig.scratch
	var accepted, refused, busy atomic.Int64
	workers := 4 * runtime.GOMAXPROCS(0)
	var wg sync.WaitGroup
	//: N goroutines, one scratch.
	for w := range workers {
		wg.Go(func() {
			buf, _ := coretoken.NewClaimsBuf()
			//: many rounds each, valid and forged interleaved.
			for round := range 2000 {
				i := (w + round) % len(valid)
				//: a valid token: accepted, or refused as busy.
				if err := rig.into.VerifyInto(shared, valid[i], buf); err == nil {
					accepted.Add(1)
				} else if errs.HasCode(err, coretoken.CodeScratchBusy) {
					busy.Add(1)
				} else {
					t.Errorf("a valid token was refused with %v", err)
				}
				err := rig.into.VerifyInto(shared, forged[i], buf)
				//: a forged token: never accepted.
				if err == nil {
					t.Errorf("a FORGED token was accepted through the shared scratch")
					return
				}
				//: busy or a signature refusal, nothing else.
				if !errs.HasCode(err, coretoken.CodeScratchBusy) && !errs.HasCode(err, coretoken.CodeSignatureInvalid) {
					t.Errorf("a forged token was refused with %v", err)
				}
				refused.Add(1)
			}
		})
	}
	wg.Wait()
	//: the test exercised both outcomes.
	if accepted.Load() == 0 || refused.Load() == 0 {
		t.Fatalf("accepted %d, refused %d: the test exercised nothing", accepted.Load(), refused.Load())
	}
	t.Logf("accepted %d valid, refused %d forged, %d busy refusals", accepted.Load(), refused.Load(), busy.Load())
}

// itoaShared renders a small non-negative integer.
func itoaShared(n int) string {
	//: one digit.
	if n < 10 {
		//: the digit.
		return string(rune('0' + n))
	}
	//: recursively.
	return itoaShared(n/10) + itoaShared(n%10)
}
