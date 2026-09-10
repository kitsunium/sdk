package token

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"math/big"
	"testing"
	"time"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/crypto/ed25519sig"
	"github.com/kitsunium/sdk/internal/service/crypto/hmacsha2"
)

// stageIssuer is the issuer string the white-box token carries.
const stageIssuer string = "https://auth.example"

// stageEpoch is the instant the white-box benchmarks pin their clock to.
var stageEpoch = time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)

// The sinks below defend every stage benchmark against dead-code elimination.
// A stage whose result the loop discards is a stage the compiler is entitled
// to delete, and a deleted stage measures 0.3 ns.
//
// They are TYPED, one per shape, and that is load-bearing rather than tidy: a
// single `var sink any` boxes whatever is assigned to it, and boxing a struct
// is a heap allocation the benchmark then attributes to the function under
// test. The first draft of this file did exactly that and reported
// splitCompact at 80 B / 1 alloc per call — which is sizeof(segmentsValue),
// not anything splitCompact does. Its own doc comment says the split
// allocates nothing at all, and with a typed sink it measures 0 B / 0 allocs.
var (
	// sinkSegments receives a split token.
	sinkSegments segmentsValue
	// sinkBytes receives a decoded segment or a rendered payload.
	sinkBytes []byte
	// sinkHeader receives a parsed JOSE header.
	sinkHeader headerValue
	// sinkParts receives a parsed, unauthenticated JWS.
	sinkParts jwsPartsValue
	// sinkClaims receives a decoded claim set.
	sinkClaims coretoken.ClaimsValue
	// sinkFooter receives a PASETO footer.
	sinkFooter []byte
)

// stageRig is one algorithm's white-box verification apparatus: the bound key,
// the resolved policy, and a token already parsed into its parts, so each
// stage can be timed without the ones before it.
type stageRig struct {
	// bound is the algorithm-bound verification key.
	bound verifyingKey
	// policy is the resolved verification policy.
	policy policyValue
	// token is the compact token every stage starts from.
	token string
	// parts is that token already parsed, so the signature and claim stages
	// do not pay for the parse.
	parts jwsPartsValue
}

// newStageRig builds the HS256 apparatus over a realistic claim set.
func newStageRig(b *testing.B) stageRig {
	b.Helper()
	raw := make([]byte, corecrypto.KeyLen)
	//: a fixed, non-degenerate secret.
	for i := range raw {
		raw[i] = byte(i * 7)
	}
	secret, kerr := corecrypto.NewKey(raw)
	if kerr != nil {
		b.Fatalf("NewKey: %v", kerr)
	}
	binding, berr := bindSecret(secret)
	if berr != nil {
		b.Fatalf("bindSecret: %v", berr)
	}
	clk := clock.NewManualClock(stageEpoch)
	issuer, ierr := newJWSIssuer(binding, IssuerConfig{Issuer: stageIssuer, Lifetime: time.Hour, Clock: clk})
	if ierr != nil {
		b.Fatalf("newJWSIssuer: %v", ierr)
	}
	token, merr := issuer.Issue(stageClaims(b))
	if merr != nil {
		b.Fatalf("Issue: %v", merr)
	}
	policy, perr := newPolicy(VerifierConfig{Issuer: stageIssuer, Clock: clk})
	if perr != nil {
		b.Fatalf("newPolicy: %v", perr)
	}
	parts, sperr := policy.parseJWS(token)
	if sperr != nil {
		b.Fatalf("parseJWS: %v", sperr)
	}
	return stageRig{bound: binding, policy: policy, token: token, parts: parts}
}

// stageClaims is the realistic claim set the stage benchmarks measure over:
// the six registered claims a deployment actually sets, plus three private
// ones. It mirrors benchClaims("realistic") in the black-box file so the two
// sets of numbers are about the same payload.
func stageClaims(b *testing.B) coretoken.ClaimsValue {
	b.Helper()
	claims := coretoken.NewClaimsValue().
		WithSubject("user-8f2c41d0-6b3e-4a19-9c77-0e5d1a2b3c4d").
		WithAudience("https://api.example").
		WithID("01JQ8Z4X7K2M9P3R5T6V8W0Y1A")
	for name, raw := range map[string]string{
		"scope":  `"openid profile email offline_access"`,
		"roles":  `["admin","billing:read","reports:write"]`,
		"tenant": `"acme-corp-eu-west-1"`,
	} {
		next, err := claims.WithPrivateRaw(name, []byte(raw))
		if err != nil {
			b.Fatalf("WithPrivateRaw: %v", err)
		}
		claims = next
	}
	return claims
}

// BenchmarkVerifyStage splits one HS256 verification into the four steps
// jwsVerifier.Verify runs, and measures the whole beside them.
//
// This is the crypto-versus-parsing split the report exists to publish. The
// four stage rows MUST sum to the "whole" row within the noise floor: if they
// do not, one of them is measuring something Verify does not do, or the
// compiler has deleted one. That cross-check is the reason "whole" is here at
// all rather than taken from the black-box file — same binary, same rig, same
// token.
func BenchmarkVerifyStage(b *testing.B) {
	rig := newStageRig(b)
	b.Run("1_parse", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			parts, err := rig.policy.parseJWS(rig.token)
			if err != nil {
				b.Fatalf("parseJWS: %v", err)
			}
			sinkParts = parts
		}
	})
	b.Run("2_checkHeader", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if err := rig.policy.checkHeader(rig.parts.header, rig.bound.algorithm()); err != nil {
				b.Fatalf("checkHeader: %v", err)
			}
		}
	})
	b.Run("3_signature", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			//: the crypto. Everything else in this benchmark is this
			//: package's own parsing cost.
			if !rig.bound.verify(rig.parts.input, rig.parts.signature) {
				b.Fatal("signature did not verify")
			}
		}
	})
	b.Run("4_decodeAndValidate", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			claims, err := rig.policy.decodeAndValidate(rig.parts.payload, joseShape{})
			if err != nil {
				b.Fatalf("decodeAndValidate: %v", err)
			}
			sinkClaims = claims
		}
	})
	b.Run("whole", func(b *testing.B) {
		verifier := &jwsVerifier{bound: rig.bound, policy: rig.policy}
		b.ReportAllocs()
		for range b.N {
			claims, err := verifier.Verify(rig.token)
			if err != nil {
				b.Fatalf("Verify: %v", err)
			}
			sinkClaims = claims
		}
	})
}

// BenchmarkPrimitive is the crypto floor: the three signature primitives on
// the exact input a token verification hands them, with no token around them.
//
// The difference between a primitive row and its algorithm's Verify row is
// everything this package costs — base64url, JSON, header handling and claim
// judgement. That subtraction is the number a caller cannot get anywhere else,
// because a published "JWT is N ns" figure never says which half is maths.
func BenchmarkPrimitive(b *testing.B) {
	rig := newStageRig(b)
	b.Run("hmac_sha256_verify", func(b *testing.B) {
		binding, _ := rig.bound.(hs256Binding)
		b.ReportAllocs()
		for range b.N {
			if !hmacsha2.MAC.Verify(binding.secret, rig.parts.input, rig.parts.signature) {
				b.Fatal("MAC did not verify")
			}
		}
	})
	benchmarkECDSAPrimitive(b, rig.parts.input)
	benchmarkEd25519Primitive(b, rig.parts.input)
}

// benchmarkECDSAPrimitive prices one ES256 verification over input, including
// the R||S decode the binding performs and excluding everything else.
func benchmarkECDSAPrimitive(b *testing.B, input []byte) {
	b.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	binding, berr := bindP256Private(key)
	if berr != nil {
		b.Fatalf("bindP256Private: %v", berr)
	}
	sig, serr := binding.sign(input)
	if serr != nil {
		b.Fatalf("sign: %v", serr)
	}
	verifying, verr := bindP256Public(&key.PublicKey)
	if verr != nil {
		b.Fatalf("bindP256Public: %v", verr)
	}
	b.Run("ecdsa_p256_verify", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if !verifying.verify(input, sig) {
				b.Fatal("signature did not verify")
			}
		}
	})
	b.Run("ecdsa_p256_verify_raw", func(b *testing.B) {
		digest := sha256.Sum256(input)
		r := new(big.Int).SetBytes(sig[:p256CoordLen])
		s := new(big.Int).SetBytes(sig[p256CoordLen:])
		b.ReportAllocs()
		for range b.N {
			//: the stdlib call with the R||S decode hoisted out, so the
			//: difference against the row above is exactly that decode.
			if !ecdsa.Verify(&key.PublicKey, digest[:], r, s) {
				b.Fatal("signature did not verify")
			}
		}
	})
}

// benchmarkEd25519Primitive prices one Ed25519 verification over input, which
// is the primitive both JOSE EdDSA and PASETO v4.public rest on.
func benchmarkEd25519Primitive(b *testing.B, input []byte) {
	b.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	sig, serr := ed25519sig.Signer.Sign(priv, input)
	if serr != nil {
		b.Fatalf("Sign: %v", serr)
	}
	b.Run("ed25519_verify", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if !ed25519sig.Signer.Verify(pub, input, sig) {
				b.Fatal("signature did not verify")
			}
		}
	})
	b.Run("ed25519_sign", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			out, gerr := ed25519sig.Signer.Sign(priv, input)
			if gerr != nil {
				b.Fatalf("Sign: %v", gerr)
			}
			sinkBytes = out
		}
	})
}
