package token_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
	"unsafe"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coretoken "github.com/kitsunium/sdk/internal/core/security/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctoken "github.com/kitsunium/sdk/internal/service/security/token"
)

// intoRig is one HS256 verifier reached as a VerifierInto, its scratch, and a
// buffer declaring the claim a login service would read.
type intoRig struct {
	// secret signs the forged tokens.
	secret corecrypto.Key
	// verifier is the plain port.
	verifier coretoken.Verifier
	// into is the same verifier through the sibling port.
	into coretoken.VerifierInto
	// scratch is the rig's one scratch.
	scratch *coretoken.VerifyScratch
	// buf is the rig's one buffer.
	buf *coretoken.ClaimsBuf
}

// intoNow is the instant every rig verifies at.
var intoNow = time.Unix(1_700_000_000, 0)

// newIntoRig builds the rig under cfg, its clock pinned to intoNow.
func newIntoRig(t *testing.T, cfg svctoken.VerifierConfig) intoRig {
	t.Helper()
	secret := testSecret(t, 7)
	cfg.Clock = clock.NewManualClock(intoNow)
	verifier, err := svctoken.NewHS256Verifier(secret, cfg)
	//: the verifier is valid by construction.
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	into, ok := verifier.(coretoken.VerifierInto)
	//: the HS256 verifier implements the sibling port.
	if !ok {
		t.Fatalf("the HS256 verifier does not implement VerifierInto")
	}
	buf, berr := coretoken.NewClaimsBuf("username")
	//: one declared private claim.
	if berr != nil {
		t.Fatalf("NewClaimsBuf: %v", berr)
	}
	//: ready.
	return intoRig{secret: secret, verifier: verifier, into: into, scratch: into.NewScratch(), buf: buf}
}

// token signs payload under the rig's key with the standard header.
func (r intoRig) token(payload string) []byte {
	//: a well-signed token around arbitrary claims.
	return []byte(forge(`{"alg":"HS256","typ":"JWT"}`, payload, hmacSigner(r.secret)))
}

// TestVerifyIntoAgreesWithVerify holds the two ports to one verdict per
// token, and to the same claims when both accept.
func TestVerifyIntoAgreesWithVerify(t *testing.T) {
	t.Parallel()
	rig := newIntoRig(t, svctoken.VerifierConfig{Issuer: "auth", Audience: "api"})
	exp := `,"exp":1700000100`
	payloads := []string{
		`{"iss":"auth","aud":"api","sub":"42","username":"alice"` + exp + `}`,
		`{"iss":"auth","aud":["web","api"],"sub":"42"` + exp + `}`,
		`{"iss":"auth","aud":"api","sub":"42"}`,
		`{"iss":"auth","aud":"api","sub":"42","exp":1600000000}`,
		`{"iss":"other","aud":"api","sub":"42"` + exp + `}`,
		`{"iss":"auth","aud":"web","sub":"42"` + exp + `}`,
		`{"iss":"auth","aud":"api","sub":null` + exp + `}`,
		`{"iss":"auth","aud":"api","sub":"42","sub":"43"` + exp + `}`,
		`{"iss":"auth","aud":"api","nbf":1800000000` + exp + `}`,
		`not json`,
	}
	//: each payload through both ports.
	for _, payload := range payloads {
		tok := rig.token(payload)
		claims, verr := rig.verifier.Verify(string(tok))
		ierr := rig.into.VerifyInto(rig.scratch, tok, rig.buf)
		//: one verdict.
		if code(verr) != code(ierr) {
			t.Fatalf("%s: Verify = %v, VerifyInto = %v", payload, verr, ierr)
		}
		//: on acceptance, the same subject and audience.
		if verr == nil {
			sub, _ := rig.buf.Text(coretoken.TextSubject)
			//: the subject, decoded.
			if string(sub) != claims.Subject() || rig.buf.AudienceLen() != len(claims.Audience()) {
				t.Fatalf("%s: VerifyInto read sub %q aud %d, Verify %q %d", payload, sub, rig.buf.AudienceLen(),
					claims.Subject(), len(claims.Audience()))
			}
		}
	}
}

// TestVerifyIntoChecksInOrder pins the order: structure, then the algorithm,
// then the MAC, then the policy, then the caller's claims.
func TestVerifyIntoChecksInOrder(t *testing.T) {
	t.Parallel()
	rig := newIntoRig(t, svctoken.VerifierConfig{Issuer: "auth"})
	long := strings.Repeat("u", coretoken.ClaimsBufTextCap+1)
	good := rig.token(`{"iss":"auth","exp":1700000100}`)
	badSig := append(bytes.Clone(good[:bytes.LastIndexByte(good, '.')+1]), "AAAA"...)
	cases := []struct {
		name string
		tok  []byte
		want errs.Code
	}{
		{"structure", []byte("a.b"), coretoken.CodeMalformed},
		{"alg before MAC", []byte(forge(`{"alg":"ES256"}`, `{}`, func(string) []byte { return []byte{1} })), coretoken.CodeAlgorithmMismatch},
		{"MAC before claims", badSig, coretoken.CodeSignatureInvalid},
		{"policy before overflow", rig.token(`{"iss":"other","username":"` + long + `","exp":1700000100}`), coretoken.CodeIssuerMismatch},
		{"expiry before overflow", rig.token(`{"iss":"auth","username":"` + long + `","exp":1600000000}`), coretoken.CodeExpired},
		{"overflow last", rig.token(`{"iss":"auth","username":"` + long + `","exp":1700000100}`), coretoken.CodeClaimsBufOverflow},
		{"registered overflow", rig.token(`{"iss":"auth","sub":"` + long + `","exp":1700000100}`), coretoken.CodeClaimsBufOverflow},
		{"audience overflow", rig.token(`{"iss":"auth","aud":["a","b","c","d","e"],"exp":1700000100}`), coretoken.CodeClaimsBufOverflow},
	}
	//: each step refuses with its own verdict.
	for _, tc := range cases {
		err := rig.into.VerifyInto(rig.scratch, tc.tok, rig.buf)
		//: the code of the step.
		if !errs.HasCode(err, tc.want) {
			t.Fatalf("%s: VerifyInto = %v, want code %v", tc.name, err, tc.want)
		}
		//: a refusal leaves the buffer reset.
		if got := rig.buf.String(); got != "token.ClaimsBuf{registered:0 aud:0 private:0/1}" {
			t.Fatalf("%s: buffer after refusal = %s", tc.name, got)
		}
	}
}

// TestClaimsBufReuseLeavesNothingBehind reuses ONE buffer across the sequence
// a login service sees — valid, no name, a bad sub, a numeric username, an
// empty one, an oversized one — and after each request scans the buffer's
// whole memory for the bytes of every earlier request.
func TestClaimsBufReuseLeavesNothingBehind(t *testing.T) {
	t.Parallel()
	rig := newIntoRig(t, svctoken.VerifierConfig{})
	exp := `,"exp":1700000100`
	steps := []struct {
		name, payload string
		markers       []string
		state         coretoken.ClaimState
		want          errs.Code
	}{
		{"valid", `{"sub":"1001","username":"alice-marker"` + exp + `}`, []string{"1001", "alice-marker"}, coretoken.ClaimOK, 0},
		{"no_name", `{"sub":"1002"` + exp + `}`, []string{"1002"}, coretoken.ClaimAbsent, 0},
		{"bad_sub", `{"sub":"zzz-bad-sub","username":"bob-marker"` + exp + `}`, []string{"zzz-bad-sub", "bob-marker"}, coretoken.ClaimOK, 0},
		{"username:123", `{"sub":"1004","username":123` + exp + `}`, []string{"1004"}, coretoken.ClaimWrongType, 0},
		{`username:""`, `{"sub":"1005","username":""` + exp + `}`, []string{"1005"}, coretoken.ClaimInvalid, 0},
		{
			"overflow", `{"sub":"1006","username":"` + strings.Repeat("o", coretoken.ClaimsBufTextCap+1) + `"` + exp + `}`,
			[]string{"1006"},
			coretoken.ClaimAbsent, coretoken.CodeClaimsBufOverflow,
		},
		{"valid again", `{"sub":"1007","username":"carol-marker"` + exp + `}`, []string{"1007", "carol-marker"}, coretoken.ClaimOK, 0},
	}
	var earlier []string
	//: one buffer, every request.
	for _, step := range steps {
		err := rig.into.VerifyInto(rig.scratch, rig.token(step.payload), rig.buf)
		//: the step's verdict.
		if code(err) != step.want {
			t.Fatalf("%s: VerifyInto = %v, want code %v", step.name, err, step.want)
		}
		//: the declared claim's state.
		if _, state := rig.buf.Private("username"); state != step.state {
			t.Fatalf("%s: username state = %v, want %v", step.name, state, step.state)
		}
		memory := bufMemory(rig.buf)
		//: no byte of an earlier request survives.
		for _, marker := range earlier {
			//: scanned over the whole struct, padding included.
			if bytes.Contains(memory, []byte(marker)) {
				t.Fatalf("%s: %q from an earlier request is still in the buffer", step.name, marker)
			}
		}
		//: the overflowing request left nothing either.
		if bytes.Contains(memory, []byte("oooooooo")) {
			t.Fatalf("%s: the refused username is in the buffer", step.name)
		}
		earlier = append(earlier, step.markers...)
	}
}

// bufMemory returns a copy of every byte of buf's struct.
func bufMemory(buf *coretoken.ClaimsBuf) []byte {
	//: the struct is a flat value: no pointer reaches out of it but the
	//: declared names, which are configuration.
	return bytes.Clone(unsafe.Slice((*byte)(unsafe.Pointer(buf)), unsafe.Sizeof(*buf)))
}

// TestScratchIsBoundToItsVerifier refuses a scratch from another verifier, a
// nil one and the zero one, with ScratchForeign, and leaves the buffer reset.
func TestScratchIsBoundToItsVerifier(t *testing.T) {
	t.Parallel()
	rig := newIntoRig(t, svctoken.VerifierConfig{AllowMissingExpiry: true})
	other := newIntoRig(t, svctoken.VerifierConfig{AllowMissingExpiry: true})
	tok := rig.token(`{"sub":"1"}`)
	//: each foreign scratch.
	for name, scratch := range map[string]*coretoken.VerifyScratch{
		"other verifier": other.scratch, "nil": nil, "zero": {},
	} {
		//: refused before any MAC.
		if err := rig.into.VerifyInto(scratch, tok, rig.buf); !errs.HasCode(err, coretoken.CodeScratchForeign) {
			t.Fatalf("%s: VerifyInto = %v, want SCRATCH_FOREIGN", name, err)
		}
	}
	//: and its own scratch is accepted.
	if err := rig.into.VerifyInto(rig.scratch, tok, rig.buf); err != nil {
		t.Fatalf("own scratch: %v", err)
	}
}

// TestOnlyHS256VerifiesInto pins which verifiers implement the sibling port:
// the HS256 ones, however reached, and no asymmetric one.
func TestOnlyHS256VerifiesInto(t *testing.T) {
	t.Parallel()
	pub, _ := testEdKey(t)
	ed, err := svctoken.NewEdDSAVerifier(pub, laxConfig())
	//: an EdDSA verifier.
	if err != nil {
		t.Fatalf("NewEdDSAVerifier: %v", err)
	}
	//: no MAC, no scratch, no sibling port.
	if _, ok := ed.(coretoken.VerifierInto); ok {
		t.Fatalf("an EdDSA verifier implements VerifierInto")
	}
}

// TestVerifyIntoRefusesANilBuffer names the argument rather than panicking.
func TestVerifyIntoRefusesANilBuffer(t *testing.T) {
	t.Parallel()
	rig := newIntoRig(t, laxConfig())
	//: a construction-site mistake.
	if err := rig.into.VerifyInto(rig.scratch, rig.token(`{}`), nil); !errs.HasCode(err, coretoken.CodePolicyMisconfigured) {
		t.Fatalf("VerifyInto(nil buffer) = %v, want POLICY_MISCONFIGURED", err)
	}
}

// TestIntoFormattingLeaksNothing renders the buffer and the scratch under
// every verb and requires that no claim and no key byte appears.
func TestIntoFormattingLeaksNothing(t *testing.T) {
	t.Parallel()
	rig := newIntoRig(t, laxConfig())
	//: a buffer holding a recognisable subject and username.
	if err := rig.into.VerifyInto(rig.scratch, rig.token(`{"sub":"secret-sub","username":"secret-user","aud":"secret-aud"}`), rig.buf); err != nil {
		t.Fatalf("VerifyInto: %v", err)
	}
	key := fmt.Sprintf("%x", rig.secret.Bytes())
	//: every verb, on the value and on the pointer.
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		rendered := fmt.Sprintf(verb+" "+verb+" "+verb, rig.buf, *rig.buf, rig.scratch)
		//: no claim value, not even the declared name.
		for _, secret := range []string{"secret-sub", "secret-user", "secret-aud", "username", key} {
			//: redacted.
			if strings.Contains(rendered, secret) {
				t.Fatalf("%s renders %q: %s", verb, secret, rendered)
			}
		}
	}
	//: the shape is what is shown.
	if got := fmt.Sprintf("%v", rig.buf); got != "token.ClaimsBuf{registered:1 aud:1 private:1/1}" {
		t.Fatalf("%%v = %s", got)
	}
}

// code is err's code, 0 for nil.
func code(err error) errs.Code {
	c, _ := errs.CodeOf(err)
	//: the code alone.
	return c
}
