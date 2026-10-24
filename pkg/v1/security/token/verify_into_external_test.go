package token_test

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/crypto"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/security/token"
)

// TestVerifierIntoIsReachableThroughTheFacade walks the allocation-free path
// with the public names only: the type assertion, the scratch, the buffer,
// the states and the codes.
func TestVerifierIntoIsReachableThroughTheFacade(t *testing.T) {
	t.Parallel()
	secret, err := crypto.NewKey(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	issuer, err := token.NewHS256Issuer(secret, token.IssuerConfig{Lifetime: time.Hour})
	if err != nil {
		t.Fatalf("NewHS256Issuer: %v", err)
	}
	claims, err := token.SetPrivateClaim(token.NewClaims().WithSubject("7"), "username", "ada")
	if err != nil {
		t.Fatalf("SetPrivateClaim: %v", err)
	}
	minted, err := issuer.Issue(claims)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	verifier, err := token.NewHS256Verifier(secret, token.VerifierConfig{})
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	into, ok := verifier.(token.VerifierInto)
	if !ok {
		t.Fatalf("the HS256 verifier does not implement token.VerifierInto")
	}
	buf, err := token.NewClaimsBuf("username")
	if err != nil {
		t.Fatalf("NewClaimsBuf: %v", err)
	}
	scratch := into.NewScratch()
	if err := into.VerifyInto(scratch, []byte(minted), buf); err != nil {
		t.Fatalf("VerifyInto: %v", err)
	}
	if name, state := buf.Private("username"); state != token.ClaimOK || string(name) != "ada" {
		t.Fatalf("username = %q %v", name, state)
	}
	if _, _, state := buf.Unix(token.TimeExpiry); state != token.ClaimOK {
		t.Fatalf("exp state = %v", state)
	}
	other := into.NewScratch()
	var zero token.VerifyScratch
	if err := into.VerifyInto(&zero, []byte(minted), buf); !errs.HasCode(err, token.CodeScratchForeign) {
		t.Fatalf("zero scratch = %v", err)
	}
	if err := into.VerifyInto(other, []byte(minted), buf); err != nil {
		t.Fatalf("a second scratch of the same verifier: %v", err)
	}
}

// ExampleVerifierInto verifies a token into a reused buffer through a
// per-thread scratch, allocating nothing per request.
func ExampleVerifierInto() {
	secret, _ := crypto.NewKey(bytes.Repeat([]byte{1}, 32))
	issuer, _ := token.NewHS256Issuer(secret, token.IssuerConfig{Lifetime: time.Hour})
	claims, _ := token.SetPrivateClaim(token.NewClaims().WithSubject("42"), "username", "alice")
	minted, _ := issuer.Issue(claims)

	verifier, _ := token.NewHS256Verifier(secret, token.VerifierConfig{})
	into := verifier.(token.VerifierInto)
	// Once per thread: a scratch and a buffer, reused for every request.
	scratch := into.NewScratch()
	buf, _ := token.NewClaimsBuf("username")

	if err := into.VerifyInto(scratch, []byte(minted), buf); err != nil {
		fmt.Println("refused:", err)
		return
	}
	sub, _ := buf.Text(token.TextSubject)
	name, state := buf.Private("username")
	fmt.Println(string(sub), string(name), state)
	fmt.Println(buf)
	// Output:
	// 42 alice ok
	// token.ClaimsBuf{registered:3 aud:0 private:1/1}
}
