package jwk_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"strconv"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/service/crypto/jwk"
)

// benchSetSizes is the key-count sweep every set benchmark runs. It brackets
// MaxKeyCandidatesCeiling (16) on both sides and reaches a set size a busy
// multi-tenant issuer really publishes, so the scan's slope is a number over a
// range rather than a claim at one point.
var benchSetSizes = []int{1, 4, 16, 64}

// The sinks below defend the benchmarks against dead-code elimination, and
// they are TYPED, one per shape. A single `var sink any` boxes whatever is
// assigned to it, and boxing is a heap allocation the benchmark would then
// charge to the function under test — an error that published "1 alloc" for a
// documented zero-allocation function earlier in this campaign.
var (
	// sinkKeys receives an AllByKid result.
	sinkKeys []jwk.KeyValue
	// sinkKey receives a parsed or projected key.
	sinkKey jwk.KeyValue
	// sinkDER receives a rendered DER blob or a raw public key.
	sinkDER []byte
	// sinkSecret receives a symmetric key.
	sinkSecret corecrypto.Key
	// sinkString receives a rendering.
	sinkString string
	// sinkErr receives an error so a refusal path is not elided either.
	sinkErr error
)

// benchECKey returns the RFC 7517 §A.1 public EC key, with the advisory "use"
// dropped: the vector says "enc", and a key published for encryption is one
// the token domain refuses before it costs anything, which would measure the
// refusal instead of the bridge.
func benchECKey(b *testing.B) jwk.KeyValue {
	b.Helper()
	key, err := jwk.Parse([]byte(ecPublicDoc))
	if err != nil {
		b.Fatalf("Parse: %v", err)
	}
	return key.WithUse("sig")
}

// benchOKPKey returns the RFC 8037 §A.1 public Ed25519 key.
func benchOKPKey(b *testing.B) jwk.KeyValue {
	b.Helper()
	key, err := jwk.Parse([]byte(okpPublicDoc))
	if err != nil {
		b.Fatalf("Parse: %v", err)
	}
	return key
}

// benchOctKey returns the symmetric vector.
func benchOctKey(b *testing.B) jwk.KeyValue {
	b.Helper()
	key, err := jwk.Parse([]byte(octDoc))
	if err != nil {
		b.Fatalf("Parse: %v", err)
	}
	return key
}

// benchKid renders a FIXED-WIDTH kid for index — "key-00" through "key-63".
//
// The width is load-bearing, not cosmetic. Go compares strings by length
// first, so a set keyed "k0".."k63" makes a two-octet lookup fail on length
// against fifty-four of its sixty-four members while a three-octet lookup
// reaches memcmp on all but ten. The first draft of BenchmarkAllByKid used
// exactly that scheme and measured match=last 43 % SLOWER than match=first —
// a difference the scan cannot produce, since it has no early exit. The cause
// was the kid's length, not the match's position. Fixed width removes it.
func benchKid(index int) string {
	//: two digits cover the whole sweep, so every kid is six octets wide.
	return "key-" + string(rune('0'+index/10)) + string(rune('0'+index%10))
}

// benchSet builds a set of size keys whose kids are benchKid(0)..benchKid(size-1),
// every member a distinct freshly generated P-256 key.
//
// The members are DISTINCT keys rather than one key repeated because a set of
// clones would let a future index dedupe them and measure a set size the
// deployment does not have.
func benchSet(b *testing.B, size int) jwk.Set {
	b.Helper()
	keys := make([]jwk.KeyValue, 0, size)
	for index := range size {
		priv, err := ecdsa.GenerateKey(elliptic.P256(), nil)
		if err != nil {
			b.Fatalf("GenerateKey: %v", err)
		}
		der, merr := x509.MarshalPKIXPublicKey(&priv.PublicKey)
		if merr != nil {
			b.Fatalf("MarshalPKIXPublicKey: %v", merr)
		}
		key, kerr := jwk.FromECDSAPublic(der)
		if kerr != nil {
			b.Fatalf("FromECDSAPublic: %v", kerr)
		}
		keys = append(keys, key.WithKid(benchKid(index)))
	}
	return jwk.NewSet(keys...)
}

// BenchmarkAllByKid prices the lookup a JWKS verifier performs on every
// request, across set size and match POSITION.
//
// The position arm is a control, not a slope: AllByKid has no early exit — it
// walks every member and appends the matches — so first and last must measure
// the same, and a divergence between them is a defect in the harness or in the
// scan. The "absent" arm isolates the allocation, since a lookup that matches
// nothing never appends and must cost zero bytes.
func BenchmarkAllByKid(b *testing.B) {
	for _, size := range benchSetSizes {
		set := benchSet(b, size)
		positions := map[string]string{
			"first":  benchKid(0),
			"last":   benchKid(size - 1),
			"absent": benchKid(99),
		}
		for name, kid := range positions {
			b.Run("n="+strconv.Itoa(size)+"/match="+name, makeAllByKidBench(set, kid))
		}
	}
}

// makeAllByKidBench returns the timed body for one (set, kid) pair.
//
// The set is a PARAMETER rather than a closed-over loop-body local, which is
// the same shape pkg/v1/codec's makeMarshalBench uses: a captured local escapes
// to the heap for the whole benchmark, and a jwk.Set captured per sub-benchmark
// would put the harness's own allocations inside the window it measures.
func makeAllByKidBench(set jwk.Set, kid string) func(b *testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			sinkKeys = set.AllByKid(kid)
		}
	}
}

// BenchmarkByKid prices the unambiguous lookup, which runs AllByKid and then
// refuses anything but a single match. It is published beside AllByKid so the
// switch's cost is visible rather than assumed to be the same call.
func BenchmarkByKid(b *testing.B) {
	set := benchSet(b, 16)
	kid := benchKid(15)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		sinkKey, sinkErr = set.ByKid(kid)
	}
}

// BenchmarkBridgeOut prices each family's outbound accessor — the call a
// consumer makes to hand a JWK to the scheme that will use it.
//
// ECDSAPublic is the expensive one and the reason this file exists: it rebuilds
// two big.Ints from the stored coordinates and then runs x509.MarshalPKIXPublicKey,
// while the OKP and oct paths are a clone and a length check.
func BenchmarkBridgeOut(b *testing.B) {
	ecKey := benchECKey(b)
	okpKey := benchOKPKey(b)
	octKey := benchOctKey(b)
	b.Run("EC/ECDSAPublic", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			sinkDER, sinkErr = ecKey.ECDSAPublic()
		}
	})
	b.Run("OKP/Ed25519Public", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			sinkDER, sinkErr = okpKey.Ed25519Public()
		}
	})
	b.Run("oct/Secret", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			sinkSecret, sinkErr = octKey.Secret()
		}
	})
}

// BenchmarkPKIXRoundTrip prices the two halves of the round trip a JWKS
// verification performs per token: jwk renders PKIX DER, and the caller
// immediately re-parses it. Publishing the halves separately is what makes the
// round trip's total attributable rather than a single opaque figure.
func BenchmarkPKIXRoundTrip(b *testing.B) {
	ecKey := benchECKey(b)
	der, err := ecKey.ECDSAPublic()
	if err != nil {
		b.Fatalf("ECDSAPublic: %v", err)
	}
	pub, perr := x509.ParsePKIXPublicKey(der)
	if perr != nil {
		b.Fatalf("ParsePKIXPublicKey: %v", perr)
	}
	ecdsaPub, isECDSA := pub.(*ecdsa.PublicKey)
	if !isECDSA {
		b.Fatalf("ParsePKIXPublicKey: got %T, want *ecdsa.PublicKey", pub)
	}
	b.Run("1_marshal", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			sinkDER, sinkErr = ecKey.ECDSAPublic()
		}
	})
	b.Run("2_parse", func(b *testing.B) {
		var out any
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			out, sinkErr = x509.ParsePKIXPublicKey(der)
		}
		sinkParsed = out
	})
	b.Run("3_ecdh_pointcheck", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			_, sinkErr = ecdsaPub.ECDH()
		}
	})
}

// sinkParsed receives the x509 parser's `any` result. It is assigned OUTSIDE
// the timed loop, so the interface it already is costs the loop nothing.
var sinkParsed any

// BenchmarkParse prices the single decode entry point per family — the cost of
// ingesting a JWKS document, which a verifier pays once per refresh rather
// than once per request.
func BenchmarkParse(b *testing.B) {
	docs := map[string]string{"EC": ecPublicDoc, "OKP": okpPublicDoc, "oct": octDoc}
	for name, doc := range docs {
		b.Run(name, makeParseBench([]byte(doc)))
	}
}

// makeParseBench returns the timed body for one JWK document.
func makeParseBench(raw []byte) func(b *testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			sinkKey, sinkErr = jwk.Parse(raw)
		}
	}
}

// BenchmarkParseSet prices ingesting a whole document, so the per-refresh cost
// can be weighed against the per-request one.
func BenchmarkParseSet(b *testing.B) {
	for _, size := range []int{1, 16} {
		set := benchSet(b, size)
		doc, err := set.MarshalPublic()
		if err != nil {
			b.Fatalf("MarshalPublic: %v", err)
		}
		b.Run("n="+strconv.Itoa(size), makeParseSetBench(doc))
	}
}

// makeParseSetBench returns the timed body for one JWK Set document.
func makeParseSetBench(doc []byte) func(b *testing.B) {
	return func(b *testing.B) {
		var out jwk.Set
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			out, sinkErr = jwk.ParseSet(doc)
		}
		sinkSet = out
	}
}

// sinkSet receives a parsed set outside the timed loop.
var sinkSet jwk.Set

// BenchmarkMarshalPublic prices the JWKS-endpoint path, per family.
func BenchmarkMarshalPublic(b *testing.B) {
	keys := map[string]jwk.KeyValue{"EC": benchECKey(b), "OKP": benchOKPKey(b)}
	for name, key := range keys {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				sinkDER, sinkErr = key.MarshalPublic()
			}
		})
	}
}

// BenchmarkThumbprint prices the RFC 7638 digest — the call a caller makes
// once per key to derive a kid, never per request.
func BenchmarkThumbprint(b *testing.B) {
	key := benchECKey(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		sinkString, sinkErr = key.Thumbprint()
	}
}
