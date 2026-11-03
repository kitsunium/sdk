package jwk

import (
	"bytes"
	"testing"
)

// TestThumbprintInputIsJCS is #895's acceptance on jwk: the thumbprint input,
// now the RFC 8785 canonical form of the required members, is byte for byte
// the RFC 7638 concatenation it replaced — kept here as the oracle — for each
// family, so every thumbprint is unchanged.
func TestThumbprintInputIsJCS(t *testing.T) {
	t.Parallel()
	x, y, k := []byte{0, 1, 2, 250, 251, 252}, []byte{9, 8, 7, 0xff}, []byte("a symmetric secret")
	cases := []struct {
		name   string
		key    KeyValue
		oracle string
	}{
		{
			"EC",
			KeyValue{kty: TypeEC, crv: CurveP256, x: x, y: y},
			`{"crv":"` + string(CurveP256) + `","kty":"EC","x":"` + b64Encode(x) + `","y":"` + b64Encode(y) + `"}`,
		},
		{
			"OKP",
			KeyValue{kty: TypeOKP, crv: CurveEd25519, x: x},
			`{"crv":"` + string(CurveEd25519) + `","kty":"OKP","x":"` + b64Encode(x) + `"}`,
		},
		{
			"oct",
			KeyValue{kty: TypeOct, priv: holdPrivate(k)},
			`{"k":"` + b64Encode(k) + `","kty":"oct"}`,
		},
	}
	for _, c := range cases {
		got, err := c.key.thumbprintInput()
		if err != nil {
			t.Fatalf("%s: thumbprintInput: %v", c.name, err)
		}
		if !bytes.Equal(got, []byte(c.oracle)) {
			t.Fatalf("%s: canonical input %s, RFC 7638 concatenation %s", c.name, got, c.oracle)
		}
	}
}
