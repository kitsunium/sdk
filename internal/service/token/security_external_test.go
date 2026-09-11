package token_test

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctoken "github.com/kitsunium/sdk/internal/service/token"
)

// b64 is the encoder every hand-built token in these tests uses.
var b64 = base64.RawURLEncoding

// testSecret returns a deterministic 256-bit symmetric key.
func testSecret(t *testing.T, fill byte) corecrypto.Key {
	t.Helper()
	raw := make([]byte, corecrypto.KeyLen)
	for i := range raw {
		raw[i] = fill + byte(i)
	}
	key, err := corecrypto.NewKey(raw)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

// testECKey returns a fresh P-256 keypair.
func testECKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return key
}

// testEdKey returns a fresh Ed25519 keypair.
func testEdKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

// laxConfig accepts a token with no expiry, so a test that is about STRUCTURE
// is not also a test about time.
func laxConfig() svctoken.VerifierConfig {
	return svctoken.VerifierConfig{AllowMissingExpiry: true}
}

// forge assembles a compact token from three already-encoded segments, so a
// test can present bytes no honest issuer would produce.
func forge(header, payload string, sign func(input string) []byte) string {
	input := b64.EncodeToString([]byte(header)) + "." + b64.EncodeToString([]byte(payload))
	return input + "." + b64.EncodeToString(sign(input))
}

// hmacSigner returns a forge signer producing a valid HS256 tag under secret,
// so a forged token fails for the reason under test and not for its signature.
func hmacSigner(secret corecrypto.Key) func(string) []byte {
	return func(input string) []byte {
		mac := hmac.New(sha256.New, secret.Bytes())
		mac.Write([]byte(input))
		return mac.Sum(nil)
	}
}

// TestAlgNoneIsRefusedInEveryCapitalisation is the unsecured-JWS guard. The
// "none" algorithm of RFC 7519 §6 is refused by NAME — not as a mismatch —
// so an operator reading the log sees the attack rather than a typo, and the
// case-folded comparison means "None" and "NONE" get the same treatment
// instead of falling through to a generic verdict.
func TestAlgNoneIsRefusedInEveryCapitalisation(t *testing.T) {
	t.Parallel()
	verifier, err := svctoken.NewHS256Verifier(testSecret(t, 1), laxConfig())
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	for _, spelling := range []string{"none", "None", "NONE", "nOnE"} {
		forged := forge(`{"alg":"`+spelling+`","typ":"JWT"}`, `{"sub":"attacker"}`,
			func(string) []byte { return nil })
		_, verr := verifier.Verify(forged)
		if !errs.HasCode(verr, coretoken.CodeAlgorithmNone) {
			t.Fatalf("alg=%q: got %v, want ALGORITHM_NONE", spelling, verr)
		}
	}
}

// TestTwoSegmentUnsecuredTokenIsRefused covers the other shape an unsecured
// JWS takes: the trailing dot with nothing after it is sometimes dropped
// entirely, leaving two segments.
func TestTwoSegmentUnsecuredTokenIsRefused(t *testing.T) {
	t.Parallel()
	verifier, err := svctoken.NewHS256Verifier(testSecret(t, 1), laxConfig())
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	two := b64.EncodeToString([]byte(`{"alg":"none"}`)) + "." + b64.EncodeToString([]byte(`{"sub":"a"}`))
	if _, verr := verifier.Verify(two); !errs.HasCode(verr, coretoken.CodeMalformed) {
		t.Fatalf("two-segment token: got %v, want MALFORMED", verr)
	}
}

// TestAlgorithmConfusionIsRefused is the test this whole domain is shaped
// around.
//
// The attack: a server publishes an EC public key and verifies ES256 tokens
// with it. The attacker mints an HS256 token using THAT PUBLIC KEY as the HMAC
// shared secret. A library that reads "alg" from the header to pick the
// verification path computes an HMAC with the public key, the tag matches, and
// the forgery is accepted as authentic.
//
// Here the ES256 verifier is bound to ES256 at construction. It compares the
// header against that binding and refuses before any key reaches any
// primitive. And the reason the mistake cannot be made in the first place is
// upstream of this test: NewHS256Verifier takes a core/crypto.Key, so
// "verify this with the EC public key as an HMAC secret" is not a call that
// compiles.
func TestAlgorithmConfusionIsRefused(t *testing.T) {
	t.Parallel()
	server := testECKey(t)
	//: exactly what a server publishes — the PKIX DER of its public key.
	published, err := x509.MarshalPKIXPublicKey(&server.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	forged := forge(`{"alg":"HS256","typ":"JWT"}`, `{"sub":"admin","aud":"api"}`,
		func(input string) []byte {
			mac := hmac.New(sha256.New, published)
			mac.Write([]byte(input))
			return mac.Sum(nil)
		})
	verifier, verr := svctoken.NewES256Verifier(&server.PublicKey, laxConfig())
	if verr != nil {
		t.Fatalf("NewES256Verifier: %v", verr)
	}
	claims, err := verifier.Verify(forged)
	if !errs.HasCode(err, coretoken.CodeAlgorithmMismatch) {
		t.Fatalf("confusion forgery: got %v, want ALGORITHM_MISMATCH", err)
	}
	if !claims.IsZero() {
		t.Fatal("a refused token must yield the zero claim set, never claims plus an error")
	}
}

// TestEdDSAAndPasetoDoNotSubstitute pins that two algorithms sharing a
// primitive still do not share a binding: the same Ed25519 keypair backs JOSE
// EdDSA and PASETO v4.public, but what gets signed differs, so a token of one
// must never satisfy a verifier of the other.
func TestEdDSAAndPasetoDoNotSubstitute(t *testing.T) {
	t.Parallel()
	pub, priv := testEdKey(t)
	jwsIssuer, err := svctoken.NewEdDSAIssuer(priv, svctoken.IssuerConfig{AllowMissingExpiry: true})
	if err != nil {
		t.Fatalf("NewEdDSAIssuer: %v", err)
	}
	jws, err := jwsIssuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	pasetoVerifier, err := svctoken.NewPasetoV4Verifier(pub,
		svctoken.PasetoVerifierConfig{AllowMissingExpiry: true})
	if err != nil {
		t.Fatalf("NewPasetoV4Verifier: %v", err)
	}
	if _, verr := pasetoVerifier.Verify(jws); verr == nil {
		t.Fatal("a JOSE EdDSA token verified as PASETO v4.public")
	}
	pasetoIssuer, err := svctoken.NewPasetoV4Issuer(priv,
		svctoken.PasetoIssuerConfig{AllowMissingExpiry: true})
	if err != nil {
		t.Fatalf("NewPasetoV4Issuer: %v", err)
	}
	paseto, err := pasetoIssuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	jwsVerifier, err := svctoken.NewEdDSAVerifier(pub, laxConfig())
	if err != nil {
		t.Fatalf("NewEdDSAVerifier: %v", err)
	}
	if _, verr := jwsVerifier.Verify(paseto); verr == nil {
		t.Fatal("a PASETO v4.public token verified as a JOSE EdDSA JWT")
	}
}

// TestSignatureIsCheckedBeforeClaims pins the ordering the Verifier contract
// promises. The token below is BOTH expired and forged; the answer must name
// the forgery. Reporting "expired" would tell an attacker what is inside a
// token that never authenticated, and would hand the application a claim set
// out of one.
func TestSignatureIsCheckedBeforeClaims(t *testing.T) {
	t.Parallel()
	secret, wrong := testSecret(t, 1), testSecret(t, 200)
	issuer, err := svctoken.NewHS256Issuer(wrong, svctoken.IssuerConfig{Lifetime: time.Minute})
	if err != nil {
		t.Fatalf("NewHS256Issuer: %v", err)
	}
	expired, err := issuer.Issue(coretoken.NewClaimsValue().
		WithExpiry(time.Now().Add(-time.Hour)).
		WithIssuedAt(time.Now().Add(-2 * time.Hour)))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	verifier, err := svctoken.NewHS256Verifier(secret, svctoken.VerifierConfig{})
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	if _, verr := verifier.Verify(expired); !errs.HasCode(verr, coretoken.CodeSignatureInvalid) {
		t.Fatalf("expired AND forged: got %v, want SIGNATURE_INVALID", verr)
	}
}

// TestOversizedInputIsRefusedBeforeSplitting is the CVE-2025-30204 regression.
// A megabyte of separators is refused on length alone; the split never runs,
// so there is no slice-per-separator to allocate.
func TestOversizedInputIsRefusedBeforeSplitting(t *testing.T) {
	t.Parallel()
	verifier, err := svctoken.NewHS256Verifier(testSecret(t, 1), laxConfig())
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	flood := strings.Repeat(".", 1<<20)
	if _, verr := verifier.Verify(flood); !errs.HasCode(verr, coretoken.CodeTooLarge) {
		t.Fatalf("separator flood: got %v, want TOO_LARGE", verr)
	}
}

// TestExtraSegmentsAreRefused pins that a token inside the size bound but with
// too many parts stops at the extra separator rather than being counted out.
func TestExtraSegmentsAreRefused(t *testing.T) {
	t.Parallel()
	verifier, err := svctoken.NewHS256Verifier(testSecret(t, 1), laxConfig())
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	for _, dots := range []int{3, 4, 40} {
		flood := strings.Repeat("aa.", dots) + "aa"
		if _, verr := verifier.Verify(flood); !errs.HasCode(verr, coretoken.CodeMalformed) {
			t.Fatalf("%d separators: got %v, want MALFORMED", dots, verr)
		}
	}
}

// TestDeeplyNestedClaimsAreRefused pins the depth bound. The payload here is
// small and perfectly valid JSON; what makes it hostile is its shape, which is
// why the check is a linear scan rather than a size limit.
func TestDeeplyNestedClaimsAreRefused(t *testing.T) {
	t.Parallel()
	secret := testSecret(t, 7)
	issuer, err := svctoken.NewHS256Issuer(secret, svctoken.IssuerConfig{AllowMissingExpiry: true})
	if err != nil {
		t.Fatalf("NewHS256Issuer: %v", err)
	}
	deep := strings.Repeat("[", 40) + strings.Repeat("]", 40)
	claims, err := coretoken.NewClaimsValue().WithPrivateRaw("nest", []byte(deep))
	if err != nil {
		t.Fatalf("WithPrivateRaw: %v", err)
	}
	nested, err := issuer.Issue(claims)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	verifier, err := svctoken.NewHS256Verifier(secret, laxConfig())
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	if _, verr := verifier.Verify(nested); !errs.HasCode(verr, coretoken.CodeTooDeep) {
		t.Fatalf("40-deep claim: got %v, want TOO_DEEP", verr)
	}
}

// TestDuplicateMembersAreRefused covers RFC 8725 §2.6. encoding/json keeps the
// last occurrence and reports success, so a token with two "aud" members could
// mean one thing here and another to a reader that keeps the first.
func TestDuplicateMembersAreRefused(t *testing.T) {
	t.Parallel()
	secret := testSecret(t, 11)
	verifier, err := svctoken.NewHS256Verifier(secret, laxConfig())
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	sign := func(input string) []byte {
		mac := hmac.New(sha256.New, secret.Bytes())
		mac.Write([]byte(input))
		return mac.Sum(nil)
	}
	dupPayload := forge(`{"alg":"HS256","typ":"JWT"}`, `{"aud":"a","aud":"b"}`, sign)
	if _, verr := verifier.Verify(dupPayload); !errs.HasCode(verr, svctoken.CodeDuplicateMember) {
		t.Fatalf("duplicate claim: got %v, want DUPLICATE_MEMBER", verr)
	}
	dupHeader := forge(`{"alg":"HS256","alg":"none"}`, `{"sub":"a"}`, sign)
	if _, verr := verifier.Verify(dupHeader); !errs.HasCode(verr, svctoken.CodeDuplicateMember) {
		t.Fatalf("duplicate header member: got %v, want DUPLICATE_MEMBER", verr)
	}
}

// TestTextThatIsNotUTF8IsRefusedOnVerify covers RFC 8725 §3.7 on the reading
// side. encoding/json does not refuse invalid UTF-8, it replaces each bad byte
// with U+FFFD, so two tokens a careless or hostile issuer signed with "a\xff"
// and "a\xfe" as their subject both verified — as the same subject. Seen
// failing without the check: both rows verified, with Subject "a\ufffd".
func TestTextThatIsNotUTF8IsRefusedOnVerify(t *testing.T) {
	t.Parallel()
	secret := testSecret(t, 12)
	verifier, err := svctoken.NewHS256Verifier(secret, laxConfig())
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	sign := hmacSigner(secret)
	type tc struct {
		name    string
		header  string
		payload string
	}
	tests := []tc{
		{"a subject ending in 0xff", `{"alg":"HS256","typ":"JWT"}`, "{\"sub\":\"a\xff\"}"},
		{"a subject ending in 0xfe", `{"alg":"HS256","typ":"JWT"}`, "{\"sub\":\"a\xfe\"}"},
		{"a header member", "{\"alg\":\"HS256\",\"typ\":\"J\xffT\"}", `{"sub":"a"}`},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		claims, verr := verifier.Verify(forge(c.header, c.payload, sign))
		if !errs.HasCode(verr, coretoken.CodeMalformed) {
			t.Fatalf("%s: Verify = (%q, %v), want MALFORMED", c.name, claims.Subject(), verr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestANullRegisteredClaimIsRefused pins that a registered claim sent as JSON
// null is malformed rather than absent. encoding/json decodes null into any
// target and leaves the zero there, so a signed token saying "aud": null was
// read as an audience of [""], "nbf": null as 1970 and "sub": null as an
// empty subject. Seen failing without the check: six rows verified, and
// "exp": null was read as 1970 and came back EXPIRED instead of MALFORMED.
func TestANullRegisteredClaimIsRefused(t *testing.T) {
	t.Parallel()
	secret := testSecret(t, 13)
	verifier, err := svctoken.NewHS256Verifier(secret, laxConfig())
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	sign := hmacSigner(secret)
	for _, payload := range []string{
		`{"iss":null}`, `{"sub":null}`, `{"aud":null}`, `{"jti":null}`,
		`{"nbf":null}`, `{"iat":null}`, `{"exp": null }`,
	} {
		t.Run(payload, func(t *testing.T) {
			t.Parallel()
			_, verr := verifier.Verify(forge(`{"alg":"HS256","typ":"JWT"}`, payload, sign))
			if !errs.HasCode(verr, coretoken.CodeMalformed) {
				t.Fatalf("Verify(%s) = %v, want MALFORMED", payload, verr)
			}
		})
	}
	//: a PRIVATE claim may be null: its meaning is the application's.
	if _, verr := verifier.Verify(forge(`{"alg":"HS256","typ":"JWT"}`, `{"tenant":null}`, sign)); verr != nil {
		t.Fatalf("a null private claim was refused: %v", verr)
	}
}

// TestCritHeaderIsRefused covers RFC 7515 §4.1.11: the parameters "crit" names
// MUST be understood. This package understands none, so its presence is a
// rejection — "ignore what you do not understand" is how a security-relevant
// extension gets silently dropped.
func TestCritHeaderIsRefused(t *testing.T) {
	t.Parallel()
	secret := testSecret(t, 13)
	verifier, err := svctoken.NewHS256Verifier(secret, laxConfig())
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	forged := forge(`{"alg":"HS256","crit":["exp"],"typ":"JWT"}`, `{"sub":"a"}`,
		func(input string) []byte {
			mac := hmac.New(sha256.New, secret.Bytes())
			mac.Write([]byte(input))
			return mac.Sum(nil)
		})
	if _, verr := verifier.Verify(forged); !errs.HasCode(verr, svctoken.CodeHeaderUnsupported) {
		t.Fatalf("crit header: got %v, want HEADER_UNSUPPORTED", verr)
	}
}

// TestBase64IsStrictAndUnpadded pins that one token has exactly one spelling.
// Accepting a padded or standard-alphabet segment would let two different
// strings decode to the same claims — and a token is a string somebody may
// have keyed a replay table on.
func TestBase64IsStrictAndUnpadded(t *testing.T) {
	t.Parallel()
	secret := testSecret(t, 17)
	issuer, err := svctoken.NewHS256Issuer(secret, svctoken.IssuerConfig{AllowMissingExpiry: true})
	if err != nil {
		t.Fatalf("NewHS256Issuer: %v", err)
	}
	honest, err := issuer.Issue(coretoken.NewClaimsValue().WithSubject("u"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	verifier, err := svctoken.NewHS256Verifier(secret, laxConfig())
	if err != nil {
		t.Fatalf("NewHS256Verifier: %v", err)
	}
	if _, verr := verifier.Verify(honest); verr != nil {
		t.Fatalf("the honest token must verify: %v", verr)
	}
	//: 28 bytes, so the standard encoding needs padding and the last quantum
	//: carries unused bits — one header exercises both refusals.
	header := `{"alg":"HS256","typ":"JWT" }`
	strict := b64.EncodeToString([]byte(header))
	for name, segment := range map[string]string{
		"padded":             base64.URLEncoding.EncodeToString([]byte(header)),
		"non-zero tail bits": strict[:len(strict)-1] + nextAlphabetChar(strict[len(strict)-1]),
	} {
		//: sign over the ALTERED segment, so the only thing that can refuse
		//: the token is the decoder's strictness.
		input := segment + "." + b64.EncodeToString([]byte(`{"sub":"u"}`))
		mac := hmac.New(sha256.New, secret.Bytes())
		mac.Write([]byte(input))
		forged := input + "." + b64.EncodeToString(mac.Sum(nil))
		if _, verr := verifier.Verify(forged); !errs.HasCode(verr, coretoken.CodeMalformed) {
			t.Errorf("%s segment: got %v, want MALFORMED", name, verr)
		}
	}
}

// nextAlphabetChar returns the base64url character one value above char, which
// keeps the decoded octets identical while making the unused trailing bits
// non-zero — the exact malleability strict decoding exists to refuse.
func nextAlphabetChar(char byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	return string(alphabet[strings.IndexByte(alphabet, char)+1])
}

// TestConstructorsRefuseUnsuitableKeys pins that a key is validated where it is
// bound, so a wrong key is a construction error and never a verifier that
// fails every request for a reason nobody can see.
func TestConstructorsRefuseUnsuitableKeys(t *testing.T) {
	t.Parallel()
	if _, err := svctoken.NewHS256Verifier(corecrypto.Key{}, laxConfig()); !errs.HasCode(err, coretoken.CodeKeyUnsuitable) {
		t.Fatalf("zero symmetric key: got %v, want KEY_UNSUITABLE", err)
	}
	if _, err := svctoken.NewES256Verifier(nil, laxConfig()); !errs.HasCode(err, coretoken.CodeKeyUnsuitable) {
		t.Fatalf("nil EC key: got %v, want KEY_UNSUITABLE", err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if _, verr := svctoken.NewES256Verifier(&p384.PublicKey, laxConfig()); !errs.HasCode(verr, coretoken.CodeKeyUnsuitable) {
		t.Fatalf("P-384 key for ES256: got %v, want KEY_UNSUITABLE", verr)
	}
	if _, verr := svctoken.NewEdDSAVerifier(make(ed25519.PublicKey, 31), laxConfig()); !errs.HasCode(verr, coretoken.CodeKeyUnsuitable) {
		t.Fatalf("short Ed25519 key: got %v, want KEY_UNSUITABLE", verr)
	}
}

// TestMisconfiguredPolicyIsRefusedAtConstruction covers ADR 0031: a knob whose
// value cannot be honoured fails where it was written, not on the first token.
func TestMisconfiguredPolicyIsRefusedAtConstruction(t *testing.T) {
	t.Parallel()
	secret := testSecret(t, 19)
	for _, tc := range []struct {
		name string
		cfg  svctoken.VerifierConfig
	}{
		{"negative leeway", svctoken.VerifierConfig{Leeway: -time.Second}},
		{"absurd leeway", svctoken.VerifierConfig{Leeway: svctoken.MaxLeeway + time.Second}},
		{"negative max age", svctoken.VerifierConfig{MaxLifetime: -time.Hour}},
		{"tiny token bound", svctoken.VerifierConfig{MaxTokenLen: 8}},
		{"huge token bound", svctoken.VerifierConfig{MaxTokenLen: svctoken.MaxTokenLenCeiling + 1}},
		{"absurd depth", svctoken.VerifierConfig{MaxClaimDepth: svctoken.MaxClaimDepthCeiling + 1}},
		{"absurd candidates", svctoken.VerifierConfig{MaxKeyCandidates: svctoken.MaxKeyCandidatesCeiling + 1}},
	} {
		if _, err := svctoken.NewHS256Verifier(secret, tc.cfg); !errs.HasCode(err, coretoken.CodePolicyMisconfigured) {
			t.Errorf("%s: got %v, want POLICY_MISCONFIGURED", tc.name, err)
		}
	}
	if _, err := svctoken.NewHS256Issuer(secret, svctoken.IssuerConfig{Lifetime: -time.Hour}); !errs.HasCode(err, coretoken.CodePolicyMisconfigured) {
		t.Errorf("negative lifetime: got %v, want POLICY_MISCONFIGURED", err)
	}
}
