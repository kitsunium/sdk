package entitlement_test

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	entitlement "github.com/kitsunium/sdk/third-party/entitlement"
)

// testKeyBits matches the smallest modulus the verifier accepts. Generating a
// smaller one would test the size guard rather than the signature path.
const testKeyBits int = 2048

// undersizedKeyBits is below the minimum the verifier accepts, so a key of this
// size exercises the size guard rather than the signature path.
const undersizedKeyBits int = 1024

// signingKey builds a key pair and the JWKS entry that publishes its public
// half, so a test signs with exactly the key the verifier will look up.
func signingKey(t *testing.T, kid string) (priv *rsa.PrivateKey, set map[string]*rsa.PublicKey) {
	t.Helper()

	priv, err := rsa.GenerateKey(nil, testKeyBits)
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("generating signing key: %v", err)
	}
	//: Publish through ParseJWKS rather than building the map directly: the
	//: encoding the verifier accepts is part of what these tests pin.
	raw, marshalErr := json.Marshal(entitlement.JWKSValue{Keys: []entitlement.JWKValue{jwkFor(priv, kid)}})
	if marshalErr != nil {
		t.Fatalf("marshalling key set: %v", marshalErr)
	}
	set, parseErr := entitlement.ParseJWKS(raw)
	if parseErr != nil {
		t.Fatalf("ParseJWKS() error = %v", parseErr)
	}
	return priv, set
}

// jwkFor renders a public key as GitHub publishes one.
func jwkFor(priv *rsa.PrivateKey, kid string) entitlement.JWKValue {
	return entitlement.JWKValue{
		KeyType:   "RSA",
		KeyID:     kid,
		Use:       "sig",
		Algorithm: "RS256",
		Modulus:   base64.RawURLEncoding.EncodeToString(priv.N.Bytes()),
		Exponent:  base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.E)).Bytes()),
	}
}

// mintToken assembles and signs a compact JWS from the given header and claims.
func mintToken(t *testing.T, priv *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()

	encode := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshalling segment: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}

	signingInput := encode(header) + "." + encode(claims)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(nil, priv, crypto.SHA256, digest[:])
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// validHeader is what GitHub actually sends.
func validHeader(kid string) map[string]any {
	return map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"}
}

// validClaims is a token from a legitimate Actions run, as a mutable map.
//
// Built by round-tripping the typed claims struct rather than written as a map
// literal, so the field names come from the one place that already defines
// them: a test asserting on a claim the verifier does not read would otherwise
// pass while proving nothing.
//
// UseNumber matters. Decoding into map[string]any turns every number into a
// float64, and re-marshalling a NumericDate that way yields 1.7570736e+09 —
// which is not what any verifier parses. json.Number round-trips the exact
// digits.
func validClaims(t *testing.T, now time.Time) map[string]any {
	t.Helper()

	raw, err := json.Marshal(entitlement.ActionsClaimsValue{
		Issuer:            entitlement.ActionsIssuer,
		Subject:           "repo:kodflow/ktn-linter:ref:refs/heads/main",
		Audience:          []string{entitlement.DefaultActionsAudience},
		RepositoryOwnerID: "133899878",
		RepositoryOwner:   "kodflow",
		Repository:        "kodflow/ktn-linter",
		EventName:         "push",
		RunnerEnvironment: "github-hosted",
		IssuedAt:          now.Add(-time.Minute).Unix(),
		NotBefore:         now.Add(-time.Minute).Unix(),
		ExpiresAt:         now.Add(4 * time.Minute).Unix(),
	})
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("marshalling claims: %v", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var claims map[string]any
	//: A failure here is an environment problem, not a test outcome.
	if decodeErr := decoder.Decode(&claims); decodeErr != nil {
		t.Fatalf("decoding claims: %v", decodeErr)
	}
	//: Return a payload a test can mutate one field at a time.
	return claims
}

// TestVerifyActionsToken_AcceptsAGenuineRun pins the one shape that must work.
// Everything else in this file is a refusal, and a verifier that refuses
// everything is not a verifier.
func TestVerifyActionsToken(t *testing.T) {
	t.Parallel()

	now := time.Now()
	priv, keys := signingKey(t, "k1")

	tests := []struct {
		name   string
		mutate func(claims map[string]any)
		reason string
	}{
		{
			name:   "a push on the default branch",
			mutate: func(map[string]any) {},
			reason: "the ordinary case",
		},
		{
			name:   "an audience carried as an array",
			mutate: func(c map[string]any) { c["aud"] = []string{"other", entitlement.DefaultActionsAudience} },
			reason: "the JWT spec allows both spellings and GitHub may switch",
		},
		{
			name:   "a token with no nbf",
			mutate: func(c map[string]any) { delete(c, "nbf") },
			reason: "nbf is optional; only exp and iat are required",
		},
		{
			name:   "an unknown future claim",
			mutate: func(c map[string]any) { c["some_new_claim"] = "value" },
			reason: "GitHub adds claims over time and must not break verification",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			claims := validClaims(t, now)
			tt.mutate(claims)
			token := mintToken(t, priv, validHeader("k1"), claims)

			got, err := entitlement.VerifyActionsToken(token, keys, entitlement.DefaultActionsAudience, now)
			if err != nil {
				t.Fatalf("VerifyActionsToken() error = %v, want nil (%s)", err, tt.reason)
			}
			//: The owner id is the entitlement key; losing it would leave
			//: nothing to match a licence against.
			if got.RepositoryOwnerID != "133899878" {
				t.Errorf("RepositoryOwnerID = %q, want %q", got.RepositoryOwnerID, "133899878")
			}
		})
	}
}

// TestVerifyActionsToken_RefusesForgeries pins the refusals that make a free CI
// seat worth anything. Each case is a way someone could otherwise claim to be
// a CI run without being one.
func TestVerifyActionsToken_RefusesForgeries(t *testing.T) {
	t.Parallel()

	now := time.Now()
	priv, keys := signingKey(t, "k1")
	other, _ := signingKey(t, "k1")

	tests := []struct {
		name    string
		token   func(t *testing.T) string
		wantErr error
		reason  string
	}{
		{
			name: "alg none",
			token: func(t *testing.T) string {
				header := map[string]any{"alg": "none", "kid": "k1"}
				return mintToken(t, priv, header, validClaims(t, now))
			},
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "accepting it would make every token valid",
		},
		{
			name: "an HMAC algorithm",
			token: func(t *testing.T) string {
				header := map[string]any{"alg": "HS256", "kid": "k1"}
				return mintToken(t, priv, header, validClaims(t, now))
			},
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "algorithm confusion: the verification key is published, so a caller could sign with it",
		},
		{
			name: "a signature from another key",
			token: func(t *testing.T) string {
				return mintToken(t, other, validHeader("k1"), validClaims(t, now))
			},
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "anyone can generate a key pair; only GitHub's signs a real token",
		},
		{
			name: "a tampered payload",
			token: func(t *testing.T) string {
				token := mintToken(t, priv, validHeader("k1"), validClaims(t, now))
				parts := strings.Split(token, ".")
				forged := validClaims(t, now)
				forged["repository_owner_id"] = "999999"
				raw, marshalErr := json.Marshal(forged)
				if marshalErr != nil {
					t.Fatalf("marshalling forged claims: %v", marshalErr)
				}
				parts[1] = base64.RawURLEncoding.EncodeToString(raw)
				return strings.Join(parts, ".")
			},
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "swapping the owner id is exactly how a real token becomes someone else's entitlement",
		},
		{
			name: "a token nominating its own key source",
			token: func(t *testing.T) string {
				header := map[string]any{"alg": "RS256", "kid": "k1", "jku": "https://example.invalid/jwks"}
				return mintToken(t, priv, header, validClaims(t, now))
			},
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "honouring jku hands the trust decision to the thing being verified",
		},
		{
			name: "an unknown kid",
			token: func(t *testing.T) string {
				return mintToken(t, priv, validHeader("rotated"), validClaims(t, now))
			},
			wantErr: entitlement.ErrCIUnknownKey,
			reason:  "distinct so the caller knows a key-set refresh is the remedy",
		},
		{
			name: "a token minted for another service",
			token: func(t *testing.T) string {
				claims := validClaims(t, now)
				claims["aud"] = "sts.amazonaws.com"
				return mintToken(t, priv, validHeader("k1"), claims)
			},
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "a token minted for a cloud provider must not be replayable here",
		},
		{
			name: "another issuer",
			token: func(t *testing.T) string {
				claims := validClaims(t, now)
				claims["iss"] = "https://example.invalid"
				return mintToken(t, priv, validHeader("k1"), claims)
			},
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "signature alone does not say GitHub is who we asked",
		},
		{
			name: "an expired token",
			token: func(t *testing.T) string {
				claims := validClaims(t, now)
				claims["iat"] = now.Add(-2 * time.Hour).Unix()
				claims["exp"] = now.Add(-time.Hour).Unix()
				return mintToken(t, priv, validHeader("k1"), claims)
			},
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "an exfiltrated token stays usable until it expires; the window must actually close",
		},
		{
			name: "a token with an over-wide window",
			token: func(t *testing.T) string {
				claims := validClaims(t, now)
				claims["iat"] = now.Add(-time.Minute).Unix()
				claims["exp"] = now.Add(72 * time.Hour).Unix()
				return mintToken(t, priv, validHeader("k1"), claims)
			},
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "a long-lived token turns the skew allowance into an unbounded replay window",
		},
		{
			name: "a token with no owner id",
			token: func(t *testing.T) string {
				claims := validClaims(t, now)
				delete(claims, "repository_owner_id")
				return mintToken(t, priv, validHeader("k1"), claims)
			},
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "falling back to the owner NAME would make a freed handle a way in",
		},
		{
			name: "a token with no kid",
			token: func(t *testing.T) string {
				return mintToken(t, priv, map[string]any{"alg": "RS256"}, validClaims(t, now))
			},
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "trying every key turns a rotation into a forgery surface",
		},
		{
			name:    "not a JWS at all",
			token:   func(*testing.T) string { return "not.a.token" },
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "a captive portal or a proxy error page must not reach the parser",
		},
		{
			name:    "an empty signature segment",
			token:   func(*testing.T) string { return "eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJ4In0." },
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "the alg-none shape by another route",
		},
		{
			name: "trailing content after the payload",
			token: func(t *testing.T) string {
				token := mintToken(t, priv, validHeader("k1"), validClaims(t, now))
				parts := strings.Split(token, ".")
				raw, marshalErr := json.Marshal(validClaims(t, now))
				if marshalErr != nil {
					t.Fatalf("marshalling claims: %v", marshalErr)
				}
				parts[1] = base64.RawURLEncoding.EncodeToString(append(raw, []byte(`{"extra":1}`)...))
				return strings.Join(parts, ".")
			},
			wantErr: entitlement.ErrCIUnverifiable,
			reason:  "showing a verifier one document and a reader another",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := entitlement.VerifyActionsToken(tt.token(t), keys, entitlement.DefaultActionsAudience, now)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("VerifyActionsToken() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
			}
		})
	}
}
