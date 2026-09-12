package entitlement

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// internalKeyBits matches the smallest modulus the verifier accepts, so these
// tests exercise the signature path rather than the size guard.
const internalKeyBits int = 2048

// signFixture signs a signing input the way GitHub does, so a test exercises
// the real verification path rather than a stub of it.
func signFixture(t *testing.T, priv *rsa.PrivateKey, signingInput string) []byte {
	t.Helper()

	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(nil, priv, crypto.SHA256, digest[:])
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("signing fixture: %v", err)
	}
	//: Return the signature the verifier will check.
	return signature
}

// Test_jwtSegments pins the structural refusals. Everything after this point
// assumes three decodable segments, so a shape that slips through here is a
// shape the signature check never gets to see.
func Test_jwtSegments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr bool
		reason  string
	}{
		{name: "a well-formed compact JWS", raw: "aGVhZGVy.cGF5bG9hZA.c2ln"},
		{name: "two segments", raw: "aGVhZGVy.cGF5bG9hZA", wantErr: true, reason: "not a JWS"},
		{name: "four segments", raw: "a.b.c.d", wantErr: true, reason: "JWE shape, not JWS"},
		{
			name:    "an empty signature segment",
			raw:     "aGVhZGVy.cGF5bG9hZA.",
			wantErr: true,
			reason:  "the alg-none shape by another route",
		},
		{name: "an empty header segment", raw: ".cGF5bG9hZA.c2ln", wantErr: true},
		{
			name:    "standard base64 rather than base64url",
			raw:     "aGVhZGVy.cGF5+G9hZA==.c2ln",
			wantErr: true,
			reason:  "two encodings decoding to one value is one too many",
		},
		{
			name:    "an oversized token",
			raw:     strings.Repeat("a", maxTokenBytes+1),
			wantErr: true,
			reason:  "not something to spend memory parsing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			signingInput, _, _, _, err := jwtSegments(tt.raw)
			if tt.wantErr {
				if !errors.Is(err, coreent.ErrCIUnverifiable) {
					t.Errorf("jwtSegments() error = %v, want coreent.ErrCIUnverifiable (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("jwtSegments() error = %v, want nil", err)
			}
			//: The signing input must be the ORIGINAL ascii: re-encoding the
			//: decoded values would not reproduce the bytes the signature
			//: covers.
			if want := "aGVhZGVy.cGF5bG9hZA"; signingInput != want {
				t.Errorf("signingInput = %q, want %q", signingInput, want)
			}
		})
	}
}

// Test_strictUnmarshal pins that exactly one document is accepted. Trailing
// content is how a token shows a verifier one payload and a reader another.
func Test_strictUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "one object", raw: `{"alg":"RS256"}`},
		{name: "trailing object", raw: `{"alg":"RS256"}{"alg":"none"}`, wantErr: true},
		{name: "trailing garbage", raw: `{"alg":"RS256"} nonsense`, wantErr: true},
		{
			//: Decoder.More() reports whether another VALUE follows, and a
			//: stray closing delimiter is not one — so this shape slipped
			//: through until the check became "the next read is EOF".
			name:    "a trailing closing delimiter",
			raw:     `{"alg":"RS256"}]`,
			wantErr: true,
		},
		{name: "trailing array", raw: `{"alg":"RS256"}[1]`, wantErr: true},
		{name: "not json at all", raw: `<html>`, wantErr: true},
		{name: "empty input", raw: ``, wantErr: true},
		{name: "a bare null", raw: `null`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := strictUnmarshal[jwtHeader]([]byte(tt.raw), "fixture")
			if tt.wantErr && !errors.Is(err, coreent.ErrCIUnverifiable) {
				t.Errorf("strictUnmarshal() error = %v, want coreent.ErrCIUnverifiable", err)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("strictUnmarshal() error = %v, want nil", err)
			}
		})
	}
}

// Test_checkHeaderShape pins the algorithm and extension rules. These are what
// make verifying the signature mean anything: without them a token could pick
// an algorithm the verifier is willing to accept on its own terms.
func Test_checkHeaderShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		header  jwtHeader
		wantErr bool
		reason  string
	}{
		{name: "RS256 with a JWT type", header: jwtHeader{Algorithm: "RS256", Type: "JWT"}},
		{name: "RS256 with no type", header: jwtHeader{Algorithm: "RS256"}},
		{name: "alg none", header: jwtHeader{Algorithm: "none"}, wantErr: true, reason: "would make every token valid"},
		{
			name:    "an HMAC algorithm",
			header:  jwtHeader{Algorithm: "HS256"},
			wantErr: true,
			reason:  "the verification key is published, so a caller could sign with it",
		},
		{name: "an empty algorithm", header: jwtHeader{}, wantErr: true},
		{name: "another type", header: jwtHeader{Algorithm: "RS256", Type: "JWE"}, wantErr: true},
		{
			name:    "a critical extension",
			header:  jwtHeader{Algorithm: "RS256", Critical: []string{"exp"}},
			wantErr: true,
			reason:  "crit names something the verifier MUST understand, and we understand none",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkHeaderShape(&tt.header)
			if tt.wantErr && !errors.Is(err, coreent.ErrCIUnverifiable) {
				t.Errorf("checkHeaderShape() error = %v, want coreent.ErrCIUnverifiable (%s)", err, tt.reason)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("checkHeaderShape() error = %v, want nil", err)
			}
		})
	}
}

// Test_checkHeader pins the key-selection rules layered on top of the shape.
func Test_checkHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantKid string
		wantErr bool
		reason  string
	}{
		{name: "a header naming its key", raw: `{"alg":"RS256","kid":"k1"}`, wantKid: "k1"},
		{
			name:    "no kid",
			raw:     `{"alg":"RS256"}`,
			wantErr: true,
			reason:  "trying every key turns a rotation into a forgery surface",
		},
		{
			name:    "a jku",
			raw:     `{"alg":"RS256","kid":"k1","jku":"https://example.invalid/jwks"}`,
			wantErr: true,
			reason:  "lets the token nominate its own trust anchor",
		},
		{
			name:    "an x5u",
			raw:     `{"alg":"RS256","kid":"k1","x5u":"https://example.invalid/cert"}`,
			wantErr: true,
			reason:  "same as jku by another field",
		},
		{name: "an undecodable header", raw: `not json`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			kid, err := checkHeader([]byte(tt.raw))
			if tt.wantErr {
				if !errors.Is(err, coreent.ErrCIUnverifiable) {
					t.Errorf("checkHeader() error = %v, want coreent.ErrCIUnverifiable (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkHeader() error = %v, want nil", err)
			}
			if kid != tt.wantKid {
				t.Errorf("checkHeader() kid = %q, want %q", kid, tt.wantKid)
			}
		})
	}
}

// Test_checkTiming pins the window. A token's own validity period is the only
// thing bounding how long an exfiltrated one stays usable off-CI.
func Test_checkTiming(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		iat     time.Time
		exp     time.Time
		nbf     time.Time
		wantErr bool
		reason  string
	}{
		{name: "inside its window", iat: now.Add(-time.Minute), exp: now.Add(4 * time.Minute)},
		{
			name:   "just expired, inside the skew allowance",
			iat:    now.Add(-6 * time.Minute),
			exp:    now.Add(-time.Minute),
			reason: "CI clocks disagree slightly and a valid run must not be refused for it",
		},
		{
			name:    "expired beyond the skew",
			iat:     now.Add(-time.Hour),
			exp:     now.Add(-30 * time.Minute),
			wantErr: true,
			reason:  "the window must actually close",
		},
		{
			name:    "issued in the future",
			iat:     now.Add(time.Hour),
			exp:     now.Add(2 * time.Hour),
			wantErr: true,
			reason:  "a forgery, or a clock too wrong to reason about",
		},
		{
			name:    "not valid yet",
			iat:     now.Add(-time.Minute),
			exp:     now.Add(4 * time.Minute),
			nbf:     now.Add(time.Hour),
			wantErr: true,
		},
		{
			name:    "a window wider than GitHub issues",
			iat:     now.Add(-time.Minute),
			exp:     now.Add(72 * time.Hour),
			wantErr: true,
			reason:  "turns the skew allowance into an unbounded replay window",
		},
		{
			//: A negative duration sails past the width check, so an inverted
			//: window needs its own refusal.
			name:    "expiring before it was issued",
			iat:     now.Add(-time.Minute),
			exp:     now.Add(-2 * time.Minute),
			wantErr: true,
			reason:  "not a window at all",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			claims := &ActionsClaimsValue{IssuedAt: tt.iat.Unix(), ExpiresAt: tt.exp.Unix()}
			//: nbf is optional; only the case that sets it exercises it.
			if !tt.nbf.IsZero() {
				claims.NotBefore = tt.nbf.Unix()
			}

			err := checkTiming(claims, now)
			if tt.wantErr && !errors.Is(err, coreent.ErrCIUnverifiable) {
				t.Errorf("checkTiming() error = %v, want coreent.ErrCIUnverifiable (%s)", err, tt.reason)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("checkTiming() error = %v, want nil (%s)", err, tt.reason)
			}
		})
	}
}

// Test_checkTimingRequiresBounds pins that a token must bound itself. GitHub
// always sets exp and iat, so their absence means this is not the document we
// think it is — and an unbounded token would never expire.
func Test_checkTimingRequiresBounds(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		claims ActionsClaimsValue
	}{
		{name: "no exp", claims: ActionsClaimsValue{IssuedAt: now.Unix()}},
		{name: "no iat", claims: ActionsClaimsValue{ExpiresAt: now.Add(time.Minute).Unix()}},
		{name: "neither", claims: ActionsClaimsValue{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := checkTiming(&tt.claims, now); !errors.Is(err, coreent.ErrCIUnverifiable) {
				t.Errorf("checkTiming() error = %v, want coreent.ErrCIUnverifiable", err)
			}
		})
	}
}

// Test_checkClaims pins that being authentic is not the same as being
// addressed to us.
func Test_checkClaims(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	valid := func() ActionsClaimsValue {
		return ActionsClaimsValue{
			Issuer:            ActionsIssuer,
			Audience:          audienceClaim{testProduct.audience()},
			Subject:           "repo:kodflow/ktn-linter:ref:refs/heads/main",
			RepositoryOwnerID: "133899878",
			IssuedAt:          now.Add(-time.Minute).Unix(),
			ExpiresAt:         now.Add(4 * time.Minute).Unix(),
		}
	}

	tests := []struct {
		name    string
		mutate  func(c *ActionsClaimsValue)
		wantErr bool
		reason  string
	}{
		{name: "a genuine run", mutate: func(*ActionsClaimsValue) {}},
		{
			name:    "another issuer",
			mutate:  func(c *ActionsClaimsValue) { c.Issuer = "https://example.invalid" },
			wantErr: true,
			reason:  "a signature alone does not say GitHub is who we asked",
		},
		{
			name:    "another audience",
			mutate:  func(c *ActionsClaimsValue) { c.Audience = audienceClaim{"sts.amazonaws.com"} },
			wantErr: true,
			reason:  "a token minted for a cloud provider must not be replayable here",
		},
		{
			name:    "no subject",
			mutate:  func(c *ActionsClaimsValue) { c.Subject = "" },
			wantErr: true,
			reason:  "not the shape Actions produces",
		},
		{
			name:    "no owner id",
			mutate:  func(c *ActionsClaimsValue) { c.RepositoryOwnerID = "" },
			wantErr: true,
			reason:  "falling back to the owner NAME would make a freed handle a way in",
		},
		{
			//: An empty claim would match an empty expected audience, turning
			//: the check off exactly where it matters.
			name:    "an empty audience claim",
			mutate:  func(c *ActionsClaimsValue) { c.Audience = audienceClaim{""} },
			wantErr: true,
			reason:  "an empty audience matches nothing on purpose",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			claims := valid()
			tt.mutate(&claims)

			err := checkClaims(&claims, testProduct.audience(), now)
			if tt.wantErr && !errors.Is(err, coreent.ErrCIUnverifiable) {
				t.Errorf("checkClaims() error = %v, want coreent.ErrCIUnverifiable (%s)", err, tt.reason)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("checkClaims() error = %v, want nil", err)
			}
		})
	}
}

// Test_verifySignature pins the step every other check exists to make
// meaningful.
func Test_verifySignature(t *testing.T) {
	t.Parallel()

	priv, err := rsa.GenerateKey(nil, internalKeyBits)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	other, err := rsa.GenerateKey(nil, internalKeyBits)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	tests := []struct {
		name    string
		signer  *rsa.PrivateKey
		verify  *rsa.PublicKey
		garble  bool
		wantErr bool
		reason  string
	}{
		{name: "the matching key verifies", signer: priv, verify: &priv.PublicKey},
		{
			name:    "another key does not",
			signer:  other,
			verify:  &priv.PublicKey,
			wantErr: true,
			reason:  "anyone can generate a key pair; only GitHub's signs a real token",
		},
		{
			name:    "a truncated signature is refused by length",
			signer:  priv,
			verify:  &priv.PublicKey,
			garble:  true,
			wantErr: true,
			reason:  "a signature of the wrong length cannot belong to this key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			const signingInput string = "header.payload"
			signature := signFixture(t, tt.signer, signingInput)
			//: Truncating exercises the length guard rather than the maths.
			if tt.garble {
				signature = signature[:len(signature)-1]
			}

			err := verifySignature(signingInput, signature, tt.verify)
			if tt.wantErr && !errors.Is(err, coreent.ErrCIUnverifiable) {
				t.Errorf("verifySignature() error = %v, want coreent.ErrCIUnverifiable (%s)", err, tt.reason)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("verifySignature() error = %v, want nil", err)
			}
		})
	}
}

// Test_audienceClaim_UnmarshalJSON pins that both spellings the JWT spec allows
// decode. GitHub emits one today and may emit the other tomorrow.
func Test_audienceClaim_UnmarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{name: "a single string", raw: `"one"`, want: 1},
		{name: "an array", raw: `["one","two"]`, want: 2},
		{name: "an empty array", raw: `[]`, want: 0},
		{name: "a number", raw: `42`, wantErr: true},
		{name: "an object", raw: `{"aud":"one"}`, wantErr: true},
		{
			//: null decoded as a string gives "", so the claim became [""] —
			//: which an empty expected audience would have matched.
			name:    "null",
			raw:     `null`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var claim audienceClaim
			err := claim.UnmarshalJSON([]byte(tt.raw))
			if tt.wantErr {
				//: Refusing beats treating the audience as absent, which would
				//: skip the check that keeps another service's token out.
				if !errors.Is(err, coreent.ErrCIUnverifiable) {
					t.Errorf("UnmarshalJSON() error = %v, want coreent.ErrCIUnverifiable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalJSON() error = %v, want nil", err)
			}
			if len(claim) != tt.want {
				t.Errorf("decoded %d entries, want %d", len(claim), tt.want)
			}
		})
	}
}

// Test_audienceClaim_contains pins that matching is exact. A prefix or
// case-folded rule would widen what this verifier accepts beyond what it was
// asked for.
func Test_audienceClaim_contains(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		claim audienceClaim
		want  string
		found bool
	}{
		{name: "the only entry", claim: audienceClaim{"a"}, want: "a", found: true},
		{name: "one of several", claim: audienceClaim{"a", "b"}, want: "b", found: true},
		{name: "absent", claim: audienceClaim{"a"}, want: "b"},
		{name: "empty", claim: audienceClaim{}, want: "a"},
		{name: "a prefix does not match", claim: audienceClaim{"ktn-linter-licence-x"}, want: "ktn-linter-licence"},
		{name: "case matters", claim: audienceClaim{"KTN"}, want: "ktn"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.claim.contains(tt.want); got != tt.found {
				t.Errorf("contains(%q) = %v, want %v", tt.want, got, tt.found)
			}
		})
	}
}

// Test_jwkNumbers pins that a published key has exactly one spelling. Two
// encodings of one number is one too many for something a trust decision rests
// on.
func Test_jwkNumbers(t *testing.T) {
	t.Parallel()

	encode := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

	tests := []struct {
		name    string
		entry   JWKValue
		wantErr bool
	}{
		{name: "canonical values", entry: JWKValue{Modulus: encode([]byte{1, 2}), Exponent: encode([]byte{1, 0, 1})}},
		{name: "a leading zero in the modulus", entry: JWKValue{Modulus: encode([]byte{0, 1}), Exponent: encode([]byte{1, 0, 1})}, wantErr: true},
		{name: "a leading zero in the exponent", entry: JWKValue{Modulus: encode([]byte{1, 2}), Exponent: encode([]byte{0, 1})}, wantErr: true},
		{name: "an empty modulus", entry: JWKValue{Modulus: "", Exponent: encode([]byte{1, 0, 1})}, wantErr: true},
		{name: "an undecodable modulus", entry: JWKValue{Modulus: "!!!", Exponent: encode([]byte{1, 0, 1})}, wantErr: true},
		{name: "an undecodable exponent", entry: JWKValue{Modulus: encode([]byte{1, 2}), Exponent: "!!!"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := jwkNumbers(&tt.entry)
			if tt.wantErr && !errors.Is(err, coreent.ErrCIUnverifiable) {
				t.Errorf("jwkNumbers() error = %v, want coreent.ErrCIUnverifiable", err)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("jwkNumbers() error = %v, want nil", err)
			}
		})
	}
}

// Test_jwkExponent pins the exponent range. An even exponent shares a factor
// with every Euler totient, so it cannot be a public exponent at all.
func Test_jwkExponent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   *big.Int
		want    int
		wantErr bool
	}{
		{name: "the usual 65537", value: big.NewInt(65537), want: 65537},
		{name: "the smallest legal exponent", value: big.NewInt(3), want: 3},
		{name: "one", value: big.NewInt(1), wantErr: true},
		{name: "zero", value: big.NewInt(0), wantErr: true},
		{name: "an even exponent", value: big.NewInt(4), wantErr: true},
		{name: "far larger than an int64", value: new(big.Int).Lsh(big.NewInt(1), 200), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := jwkExponent(tt.value.Bytes(), "k1")
			if tt.wantErr {
				if !errors.Is(err, coreent.ErrCIUnverifiable) {
					t.Errorf("jwkExponent() error = %v, want coreent.ErrCIUnverifiable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("jwkExponent() error = %v, want nil", err)
			}
			if got != tt.want {
				t.Errorf("jwkExponent() = %d, want %d", got, tt.want)
			}
		})
	}
}

// Test_rsaKeyFromJWK pins which published entries become usable keys.
func Test_rsaKeyFromJWK(t *testing.T) {
	t.Parallel()

	priv, err := rsa.GenerateKey(nil, internalKeyBits)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	usable := JWKValue{
		KeyType:   "RSA",
		KeyID:     "k1",
		Use:       "sig",
		Algorithm: "RS256",
		Modulus:   base64.RawURLEncoding.EncodeToString(priv.N.Bytes()),
		Exponent:  base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.E)).Bytes()),
	}

	tests := []struct {
		name    string
		mutate  func(e *JWKValue)
		wantErr bool
		reason  string
	}{
		{name: "a published signing key", mutate: func(*JWKValue) {}},
		{name: "no use declared", mutate: func(e *JWKValue) { e.Use = "" }},
		{name: "no alg declared", mutate: func(e *JWKValue) { e.Algorithm = "" }},
		{
			name:    "an encryption key",
			mutate:  func(e *JWKValue) { e.Use = "enc" },
			wantErr: true,
			reason:  "a key published for encryption is being repurposed",
		},
		{name: "an EC key", mutate: func(e *JWKValue) { e.KeyType = "EC" }, wantErr: true},
		{
			//: rsa.VerifyPKCS1v15 does modular arithmetic over whatever it is
			//: handed, so an absurd modulus turns every verification into a
			//: long computation — decided by a document nothing has
			//: authenticated yet.
			name: "an absurdly large modulus",
			mutate: func(e *JWKValue) {
				huge := make([]byte, (maxRSAModulusBits/8)+1)
				huge[0] = 0xff
				e.Modulus = base64.RawURLEncoding.EncodeToString(huge)
			},
			wantErr: true,
			reason:  "a key larger than any real one is not a key",
		},
		{name: "another algorithm", mutate: func(e *JWKValue) { e.Algorithm = "PS256" }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			entry := usable
			tt.mutate(&entry)

			key, keyErr := rsaKeyFromJWK(&entry)
			if tt.wantErr {
				if !errors.Is(keyErr, coreent.ErrCIUnverifiable) {
					t.Errorf("rsaKeyFromJWK() error = %v, want coreent.ErrCIUnverifiable (%s)", keyErr, tt.reason)
				}
				return
			}
			if keyErr != nil {
				t.Fatalf("rsaKeyFromJWK() error = %v, want nil", keyErr)
			}
			if key.N.Cmp(priv.N) != 0 || key.E != priv.E {
				t.Error("rsaKeyFromJWK() rebuilt a different key")
			}
		})
	}
}
