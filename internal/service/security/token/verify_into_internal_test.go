package token

import (
	"hash"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coretoken "github.com/kitsunium/sdk/internal/core/security/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/crypto/mac/hmacsha2"
)

// countingHash is a keyed MAC that counts the bytes written into it.
type countingHash struct {
	// Hash is the real MAC.
	hash.Hash
	// written counts the bytes Write absorbed.
	written int
}

// Write counts, then forwards.
func (c *countingHash) Write(p []byte) (int, error) {
	c.written += len(p)
	//: the real MAC still runs.
	return c.Hash.Write(p)
}

// TestABusyScratchComputesNoMAC enters a scratch as another verification
// would, then verifies through it: the call must refuse with ScratchBusy
// without a byte reaching the MAC.
func TestABusyScratchComputesNoMAC(t *testing.T) {
	t.Parallel()
	raw := make([]byte, corecrypto.KeyLen)
	key, err := corecrypto.NewKey(raw)
	//: a zero key is still a key of the right length.
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	verifier, verr := newJWSVerifier(hs256Binding{secret: key}, VerifierConfig{AllowMissingExpiry: true})
	//: the HS256 verifier.
	if verr != nil {
		t.Fatalf("newJWSVerifier: %v", verr)
	}
	hs := verifier.(*hs256Verifier)
	counting := &countingHash{Hash: hmacsha2.MAC.New(key)}
	scratch := coretoken.NewVerifyScratch(hs.owner, counting)
	buf, _ := coretoken.NewClaimsBuf()
	//: another verification holds it.
	if eerr := scratch.Enter(hs.owner); eerr != nil {
		t.Fatalf("Enter: %v", eerr)
	}
	token := []byte("eyJhbGciOiJIUzI1NiJ9.e30.AAAA")
	//: refused as busy.
	if berr := hs.VerifyInto(scratch, token, buf); !errs.HasCode(berr, coretoken.CodeScratchBusy) {
		t.Fatalf("VerifyInto on a held scratch = %v, want SCRATCH_BUSY", berr)
	}
	//: and no MAC was computed.
	if counting.written != 0 {
		t.Fatalf("a busy scratch absorbed %d bytes into its MAC", counting.written)
	}
	scratch.Leave()
	//: released, the same scratch verifies — and computes a MAC.
	if rerr := hs.VerifyInto(scratch, token, buf); !errs.HasCode(rerr, coretoken.CodeSignatureInvalid) || counting.written == 0 {
		t.Fatalf("VerifyInto on a released scratch = %v after %d MAC bytes", rerr, counting.written)
	}
}
