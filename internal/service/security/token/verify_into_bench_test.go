package token_test

import (
	"testing"

	coretoken "github.com/kitsunium/sdk/internal/core/security/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// BenchmarkVerifyInto prices the allocation-free sibling of Verify on the
// same tokens BenchmarkVerify/HS256 verifies: one scratch and one buffer,
// reused across calls as an event loop reuses them, the buffer declaring the
// three private claims the realistic token carries. The pair of rows is the
// one to read against BenchmarkVerify/HS256 — same bytes, same policy, same
// refusal set.
func BenchmarkVerifyInto(b *testing.B) {
	ring := newBenchKeyring(b)
	for _, shape := range []string{"minimal", "realistic"} {
		b.Run("HS256/"+shape, func(b *testing.B) {
			clk := clock.NewManualClock(benchEpoch)
			minted := []byte(benchMint(b, ring.issuerFor(b, "HS256", clk), benchClaims(b, shape)))
			into, ok := ring.verifierFor(b, "HS256", clk).(coretoken.VerifierInto)
			if !ok {
				b.Fatalf("the HS256 verifier does not implement VerifierInto")
			}
			scratch := into.NewScratch()
			buf, err := coretoken.NewClaimsBuf("scope", "roles", "tenant")
			if err != nil {
				b.Fatalf("NewClaimsBuf: %v", err)
			}
			if err := into.VerifyInto(scratch, minted, buf); err != nil {
				b.Fatalf("token does not verify, so this row would measure a refusal: %v", err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := into.VerifyInto(scratch, minted, buf); err != nil {
					b.Fatalf("VerifyInto: %v", err)
				}
			}
		})
	}
}
