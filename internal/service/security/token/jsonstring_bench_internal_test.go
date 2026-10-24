package token

import (
	"strings"
	"testing"
)

// quoteBenchSink keeps the result observable, so the call cannot be deleted.
var quoteBenchSink string

// BenchmarkQuoteJSONString prices the literal the JOSE and PASETO shapes write
// for an issuer, an audience or a key id, by the shapes those take.
func BenchmarkQuoteJSONString(b *testing.B) {
	cases := []struct{ name, in string }{
		{"short", "HS256"},
		{"issuer", "https://auth.example.com/realms/production"},
		{"long", strings.Repeat("https://api.example.com/v1/orders/", 6)},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(int64(len(c.in)))
			b.ReportAllocs()
			for b.Loop() {
				quoteBenchSink = quoteJSONString(c.in)
			}
		})
	}
}
