package jwk_test

import (
	"encoding/json"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/crypto/jwk"
)

// The EC vectors are RFC 7515 §A.3.1 (private ES256 key) and RFC 7517 §A.1
// (public P-256 key); the OKP vectors are RFC 8037 §A.1. Using published
// vectors rather than freshly generated keys means a regression in the
// on-curve / scalar-consistency checks shows up as a failure here instead of
// being masked by material this package produced itself.
const (
	ecPrivateDoc = `{"kty":"EC","crv":"P-256",` +
		`"x":"f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU",` +
		`"y":"x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0",` +
		`"d":"jpsQnnGQmL-YBIffH1136cspYG6-0iY7X1fCE9-E9LI"}`

	ecPublicDoc = `{"kty":"EC","crv":"P-256","kid":"1","use":"enc",` +
		`"x":"MKBCTNIcKUSDii11ySs3526iDZ8AiTo7Tu6KPAqv7D4",` +
		`"y":"4Etl6SRW2YiLUrN5vfvVHuhp7x8PxltmWWlbbM4IFyM"}`

	okpPrivateDoc = `{"kty":"OKP","crv":"Ed25519",` +
		`"x":"11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo",` +
		`"d":"nWGxne_9WmC6hEr0kuwsxERJxWl7MmkZcDusAxyuf2A"}`

	okpPublicDoc = `{"kty":"OKP","crv":"Ed25519",` +
		`"x":"11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"}`

	octDoc = `{"kty":"oct","kid":"mac-1",` +
		`"k":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"}`

	// ecXCoord is the RFC 7515 §A.3.1 x coordinate, reused as a y to build an
	// off-curve point of the RIGHT length — the case a parser that only
	// measures coordinates accepts.
	ecXCoord = "f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU"
	// b64Of31Ones decodes to 31 octets, one short of a P-256 coordinate.
	b64Of31Ones = "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQ"
	// b64Of32Ones decodes to a valid-length, in-range scalar that derives a
	// DIFFERENT point than the vectors declare.
	b64Of32Ones = "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"
	// b64Of32Zeros decodes to the scalar 0, outside [1, n-1].
	b64Of32Zeros = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

func TestParseAccepts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		doc         string
		wantType    jwk.Type
		wantCurve   jwk.Curve
		wantKID     string
		wantPrivate bool
	}{
		{"RFC 7515 A.3.1 private EC key", ecPrivateDoc, jwk.TypeEC, jwk.CurveP256, "", true},
		{"RFC 7517 A.1 public EC key", ecPublicDoc, jwk.TypeEC, jwk.CurveP256, "1", false},
		{"RFC 8037 A.1 private OKP key", okpPrivateDoc, jwk.TypeOKP, jwk.CurveEd25519, "", true},
		{"RFC 8037 A.2 public OKP key", okpPublicDoc, jwk.TypeOKP, jwk.CurveEd25519, "", false},
		{"symmetric oct key", octDoc, jwk.TypeOct, "", "mac-1", true},
	}
	runCase := func(t *testing.T, c struct {
		name        string
		doc         string
		wantType    jwk.Type
		wantCurve   jwk.Curve
		wantKID     string
		wantPrivate bool
	},
	) {
		t.Helper()
		key, err := jwk.Parse([]byte(c.doc))
		//: a published vector must parse cleanly.
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		//: every metadata member must survive the decode.
		if key.Kty() != c.wantType || key.Crv() != c.wantCurve || key.Kid() != c.wantKID {
			t.Errorf("got (%q,%q,%q) want (%q,%q,%q)",
				key.Kty(), key.Crv(), key.Kid(), c.wantType, c.wantCurve, c.wantKID)
		}
		//: IsPrivate is what every export decision keys on.
		if key.IsPrivate() != c.wantPrivate {
			t.Errorf("IsPrivate()=%v want %v", key.IsPrivate(), c.wantPrivate)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestParseRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		doc      string
		wantCode errs.Code
	}{
		{"not JSON at all", `{`, jwk.CodeJWKMalformed},
		{"JSON but not an object", `["EC"]`, jwk.CodeJWKMalformed},
		{"kty absent", `{"crv":"P-256","x":"` + ecXCoord + `","y":"` + ecXCoord + `"}`, jwk.CodeJWKMissingMember},
		{"kty empty", `{"kty":"","crv":"P-256"}`, jwk.CodeJWKMissingMember},
		{"RSA is not modelled", `{"kty":"RSA","n":"AQAB","e":"AQAB"}`, jwk.CodeJWKUnsupportedKeyType},
		{"unknown kty", `{"kty":"nonsense"}`, jwk.CodeJWKUnsupportedKeyType},
		{"unknown EC curve", `{"kty":"EC","crv":"P-192","x":"` + ecXCoord + `","y":"` + ecXCoord + `"}`, jwk.CodeJWKUnsupportedCurve},
		{"EC crv absent", `{"kty":"EC","x":"` + ecXCoord + `","y":"` + ecXCoord + `"}`, jwk.CodeJWKUnsupportedCurve},
		{"Ed25519 mislabelled as EC", `{"kty":"EC","crv":"Ed25519","x":"` + ecXCoord + `"}`, jwk.CodeJWKUnsupportedCurve},
		{"P-256 mislabelled as OKP", `{"kty":"OKP","crv":"P-256","x":"` + ecXCoord + `"}`, jwk.CodeJWKUnsupportedCurve},
		{"oct carrying a curve", `{"kty":"oct","crv":"P-256","k":"` + b64Of32Ones + `"}`, jwk.CodeJWKUnsupportedCurve},
		{"EC y absent", `{"kty":"EC","crv":"P-256","x":"` + ecXCoord + `"}`, jwk.CodeJWKMissingMember},
		{"OKP x absent", `{"kty":"OKP","crv":"Ed25519"}`, jwk.CodeJWKMissingMember},
		{"oct k absent", `{"kty":"oct","kid":"1"}`, jwk.CodeJWKMissingMember},
		{"padded base64url is refused", `{"kty":"oct","k":"AQEB="}`, jwk.CodeJWKInvalidEncoding},
		{"standard-alphabet base64 is refused", `{"kty":"oct","k":"a+b/c"}`, jwk.CodeJWKInvalidEncoding},
		{"coordinate one octet short", `{"kty":"EC","crv":"P-256","x":"` + b64Of31Ones + `","y":"` + ecXCoord + `"}`, jwk.CodeJWKInvalidEncoding},
		{"OKP key of the wrong length", `{"kty":"OKP","crv":"Ed25519","x":"` + b64Of31Ones + `"}`, jwk.CodeJWKInvalidEncoding},
		{"point not on the declared curve", `{"kty":"EC","crv":"P-256","x":"` + ecXCoord + `","y":"` + ecXCoord + `"}`, jwk.CodeJWKKeyMismatch},
		{"EC scalar out of range", `{"kty":"EC","crv":"P-256","x":"f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU","y":"x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0","d":"` + b64Of32Zeros + `"}`, jwk.CodeJWKKeyMismatch},
		{"EC d does not derive x,y", `{"kty":"EC","crv":"P-256","x":"f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU","y":"x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0","d":"` + b64Of32Ones + `"}`, jwk.CodeJWKKeyMismatch},
		{"OKP d does not derive x", `{"kty":"OKP","crv":"Ed25519","x":"11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo","d":"` + b64Of32Ones + `"}`, jwk.CodeJWKKeyMismatch},
	}
	runCase := func(t *testing.T, c struct {
		name     string
		doc      string
		wantCode errs.Code
	},
	) {
		t.Helper()
		key, err := jwk.Parse([]byte(c.doc))
		//: a rejected document must yield the zero Key, never a half-built one.
		if !key.IsZero() {
			t.Errorf("Parse returned a non-zero Key on rejection: %v", key)
		}
		//: and the typed code the package documents for that rejection.
		if !errs.HasCode(err, c.wantCode) {
			t.Errorf("Parse err=%v want code %v", err, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestParseIgnoresUnknownMembers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
	}{
		{"an unmodelled member does not block the parse", `{"kty":"OKP","crv":"Ed25519","x":"11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo","x5t":"ignored","ext":true}`},
	}
	runCase := func(t *testing.T, c struct {
		name string
		doc  string
	},
	) {
		t.Helper()
		key, err := jwk.Parse([]byte(c.doc))
		//: RFC 7517 §4 lets a parser ignore members it does not recognise.
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		out, merr := key.MarshalPublic()
		//: but the ignored members must NOT reappear on the way out — this
		//: package only vouches for what it understood.
		if merr != nil || string(out) != okpPublicDoc {
			t.Errorf("MarshalPublic=(%s,%v) want %s", out, merr, okpPublicDoc)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestParseIsTheOnlyDecodeEntryPoint pins a deliberate asymmetry. KeyValue
// implements json.Marshaler — that is load-bearing, because it makes the SAFE
// rendering the one plain json.Marshal reaches for. It does NOT implement
// json.Unmarshaler: adding one would force a pointer receiver into an otherwise
// all-value method set, and a KeyValue that only marshals correctly through a
// pointer would quietly defeat the guarantee above.
//
// The visible consequence is that decoding straight into a KeyValue yields the
// ZERO value rather than a key. That is safe — every method refuses the zero
// value with MissingMember — but it is a trap worth pinning, so it stays a
// documented property instead of a surprise.
func TestParseIsTheOnlyDecodeEntryPoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
	}{
		{"a public OKP key", okpPublicDoc},
		{"a private EC key", ecPrivateDoc},
	}
	runCase := func(t *testing.T, c struct {
		name string
		doc  string
	},
	) {
		t.Helper()
		//: the realistic shape: a KeyValue sitting in somebody's payload struct.
		var envelope struct {
			Key jwk.KeyValue `json:"key"`
		}
		//: no Unmarshaler, no exported fields — nothing lands in the value.
		if err := json.Unmarshal([]byte(`{"key":`+c.doc+`}`), &envelope); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		//: the result is the zero key, which every method then refuses.
		if !envelope.Key.IsZero() {
			t.Fatalf("reflective decode populated a KeyValue: %v", envelope.Key)
		}
		//: and refuses it typed, so the mistake surfaces at first use.
		if _, err := envelope.Key.MarshalPublic(); !errs.HasCode(err, jwk.CodeJWKMissingMember) {
			t.Errorf("MarshalPublic err=%v want MissingMember", err)
		}
		//: Parse is the path that actually produces a key.
		parsed, perr := jwk.Parse([]byte(c.doc))
		if perr != nil || parsed.IsZero() {
			t.Errorf("Parse=(%v,%v) want a populated key", parsed, perr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
