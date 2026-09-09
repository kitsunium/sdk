package jwk

import (
	"crypto/elliptic"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func TestCoordLen(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		crv  Curve
		want int
	}{
		{"P-256 is 32 octets", CurveP256, p256CoordLen},
		{"P-384 is 48 octets", CurveP384, p384CoordLen},
		{"P-521 is 66, not 65", CurveP521, p521CoordLen},
		{"Ed25519 is not an EC curve", CurveEd25519, 0},
		{"an unknown name has no length", Curve("P-192"), 0},
		{"the empty curve has no length", Curve(""), 0},
	}
	runCase := func(t *testing.T, c struct {
		name string
		crv  Curve
		want int
	},
	) {
		t.Helper()
		//: the fixed length is what makes an encoded coordinate unambiguous.
		if got := coordLen(c.crv); got != c.want {
			t.Errorf("coordLen(%q)=%d want %d", c.crv, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestCurveTablesAgree(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		crv     Curve
		unnamed elliptic.Curve
		mapped  bool
	}{
		{"P-256 maps both ways", CurveP256, nil, true},
		{"P-384 maps both ways", CurveP384, nil, true},
		{"P-521 maps both ways", CurveP521, nil, true},
		{"Ed25519 has no NIST counterpart", CurveEd25519, elliptic.P224(), false},
		{"an unknown name maps nowhere", Curve("P-192"), nil, false},
	}
	runCase := func(t *testing.T, c struct {
		name    string
		crv     Curve
		unnamed elliptic.Curve
		mapped  bool
	},
	) {
		t.Helper()
		_, ecdhOK := ecdhCurve(c.crv)
		mapped, ellipticOK := ellipticCurve(c.crv)
		//: the validation table and the x509 table must never disagree, or a
		//: key would validate on one curve and marshal on another.
		if ecdhOK != c.mapped || ellipticOK != c.mapped {
			t.Fatalf("ecdhCurve=%v ellipticCurve=%v want %v", ecdhOK, ellipticOK, c.mapped)
		}
		//: the round trip through crypto/elliptic must name the same curve.
		if c.mapped {
			back, ok := curveFromElliptic(mapped)
			//: a mapped curve must survive both directions unchanged.
			if !ok || back != c.crv {
				t.Errorf("curveFromElliptic=%q,%v want %q", back, ok, c.crv)
			}
			return
		}
		//: the unmapped rows check the reverse table rejects too; P-224 is the
		//: interesting negative — a real curve with no JOSE name.
		if _, ok := curveFromElliptic(c.unnamed); ok {
			t.Errorf("curveFromElliptic accepted an unnamed curve")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestUncompressedPoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		x    []byte
		y    []byte
		want string
	}{
		{"tags the SEC1 uncompressed form", []byte{1, 2}, []byte{3, 4}, "\x04\x01\x02\x03\x04"},
	}
	runCase := func(t *testing.T, c struct {
		name string
		x    []byte
		y    []byte
		want string
	},
	) {
		t.Helper()
		//: crypto/ecdh only accepts the 0x04-tagged concatenation.
		if got := string(uncompressedPoint(c.x, c.y)); got != c.want {
			t.Errorf("uncompressedPoint=%q want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestCurveChecksRejectUnmappedCurves(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		invoke func() error
	}{
		{"checkECPoint on an unmapped curve", func() error { return checkECPoint(CurveEd25519, nil, nil) }},
		{"checkECScalar on an unmapped curve", func() error { return checkECScalar(CurveEd25519, nil, nil, nil) }},
	}
	runCase := func(t *testing.T, c struct {
		name   string
		invoke func() error
	},
	) {
		t.Helper()
		//: a curve with no ecdh counterpart cannot be validated, so the check
		//: refuses instead of reporting a key it never examined as valid.
		if err := c.invoke(); !errs.HasCode(err, CodeJWKUnsupportedCurve) {
			t.Errorf("err=%v want UnsupportedCurve", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestWireRejectsIncompleteKeys(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		key      KeyValue
		public   bool
		wantCode errs.Code
	}{
		{"an EC key with no coordinates cannot be published", KeyValue{kty: TypeEC, crv: CurveP256}, true, CodeJWKMissingMember},
		{"an OKP key with no x cannot be published", KeyValue{kty: TypeOKP, crv: CurveEd25519}, true, CodeJWKMissingMember},
		{"a private EC key with no coordinates cannot be exported", KeyValue{kty: TypeEC, crv: CurveP256, priv: []byte{1}}, false, CodeJWKMissingMember},
	}
	runCase := func(t *testing.T, c struct {
		name     string
		key      KeyValue
		public   bool
		wantCode errs.Code
	},
	) {
		t.Helper()
		var err error
		//: both wire builders must refuse a key that is only half-assembled.
		if c.public {
			_, err = c.key.publicWire()
		} else {
			_, err = c.key.privateWire()
		}
		//: the refusal must be typed, not a nil document with a nil error.
		if !errs.HasCode(err, c.wantCode) {
			t.Errorf("err=%v want code %v", err, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestThumbprintInputRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		key      KeyValue
		wantCode errs.Code
	}{
		{"the zero key has no required members", KeyValue{}, CodeJWKMissingMember},
		{"an unmodelled family has no canonical form", KeyValue{kty: Type("RSA")}, CodeJWKUnsupportedKeyType},
		{"an EC key without y", KeyValue{kty: TypeEC, crv: CurveP256, x: []byte{1}}, CodeJWKMissingMember},
		{"an OKP key without x", KeyValue{kty: TypeOKP, crv: CurveEd25519}, CodeJWKMissingMember},
		{"an oct key without k", KeyValue{kty: TypeOct}, CodeJWKNoPrivateMaterial},
	}
	runCase := func(t *testing.T, c struct {
		name     string
		key      KeyValue
		wantCode errs.Code
	},
	) {
		t.Helper()
		canonical, err := c.key.thumbprintInput()
		//: an incomplete key must not produce a thumbprint that would then be
		//: used as a kid — a stable id for a key that does not exist.
		if canonical != nil || !errs.HasCode(err, c.wantCode) {
			t.Errorf("thumbprintInput=(%q,%v) want code %v", canonical, err, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// baseKey is the fully-populated key TestEqualComparesEveryMember mutates one
// member at a time. It lives outside the test so each row can be written as a
// literal rather than through a mutating closure.
var baseKey = KeyValue{
	kty: TypeEC, crv: CurveP256, kid: "k", use: "sig", alg: "ES256",
	keyOps: []string{"sign"}, x: []byte{1}, y: []byte{2}, priv: []byte{3},
}

// withMember returns a copy of baseKey with one member replaced.
func withMember(apply func(target *KeyValue)) KeyValue {
	out := baseKey
	apply(&out)
	return out
}

func TestEqualComparesEveryMember(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		other KeyValue
		want  bool
	}{
		{"an identical key is equal", baseKey, true},
		{"a different kty is not", withMember(func(k *KeyValue) { k.kty = TypeOKP }), false},
		{"a different crv is not", withMember(func(k *KeyValue) { k.crv = CurveP384 }), false},
		{"a different kid is not", withMember(func(k *KeyValue) { k.kid = "other" }), false},
		{"a different use is not", withMember(func(k *KeyValue) { k.use = "enc" }), false},
		{"a different alg is not", withMember(func(k *KeyValue) { k.alg = "ES384" }), false},
		{"different key_ops are not", withMember(func(k *KeyValue) { k.keyOps = []string{"verify"} }), false},
		{"a different x is not", withMember(func(k *KeyValue) { k.x = []byte{9} }), false},
		{"a different y is not", withMember(func(k *KeyValue) { k.y = []byte{9} }), false},
		{"a dropped secret is not", withMember(func(k *KeyValue) { k.priv = nil }), false},
	}
	runCase := func(t *testing.T, c struct {
		name  string
		other KeyValue
		want  bool
	},
	) {
		t.Helper()
		//: Equal must notice every member, including the ones a round-trip
		//: test would otherwise silently drop.
		if got := baseKey.Equal(c.other); got != c.want {
			t.Errorf("Equal=%v want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestKeyOpsRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ops  []string
	}{
		{"a single operation survives", []string{"verify"}},
		{"several operations survive in order", []string{"sign", "verify"}},
		{"no operations means no member", nil},
	}
	runCase := func(t *testing.T, c struct {
		name string
		ops  []string
	},
	) {
		t.Helper()
		key, perr := Parse([]byte(`{"kty":"OKP","crv":"Ed25519","x":"11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"}`))
		//: a broken fixture must not read as a broken assertion.
		if perr != nil {
			t.Fatalf("Parse: %v", perr)
		}
		document, merr := key.WithKeyOps(c.ops...).MarshalPublic()
		//: the member has to survive serialisation before it can survive a parse.
		if merr != nil {
			t.Fatalf("MarshalPublic: %v", merr)
		}
		back, berr := Parse(document)
		//: and the document has to be readable again.
		if berr != nil {
			t.Fatalf("re-parse: %v", berr)
		}
		got := back.KeyOps()
		//: key_ops is order-significant as published, so it must come back
		//: verbatim rather than normalised.
		if len(got) != len(c.ops) {
			t.Fatalf("KeyOps()=%v want %v", got, c.ops)
		}
		for i, op := range got {
			//: each operation, in the published order.
			if op != c.ops[i] {
				t.Errorf("KeyOps()[%d]=%q want %q", i, op, c.ops[i])
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestB64DecodeFixedReportsBothSizes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		member string
		want   int
		ok     bool
	}{
		{"the exact length is accepted", "AQEB", 3, true},
		{"a shorter member is refused", "AQEB", 4, false},
		{"a longer member is refused", "AQEB", 2, false},
	}
	runCase := func(t *testing.T, c struct {
		name   string
		member string
		want   int
		ok     bool
	},
	) {
		t.Helper()
		raw, err := b64DecodeFixed(c.member, c.want)
		//: the accepted row must hand back exactly the requested octets.
		if c.ok {
			//: exact length, no error.
			if err != nil || len(raw) != c.want {
				t.Errorf("b64DecodeFixed=(%d,%v) want %d octets", len(raw), err, c.want)
			}
			return
		}
		//: the refused rows carry the typed code …
		if !errs.HasCode(err, CodeJWKInvalidEncoding) {
			t.Fatalf("err=%v want InvalidEncoding", err)
		}
		//: … plus both sizes as fields, so a caller sees the mismatch without
		//: ever seeing the material.
		if len(errs.FieldsOf(err)) != 2 {
			t.Errorf("fields=%v want want/got", errs.FieldsOf(err))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
