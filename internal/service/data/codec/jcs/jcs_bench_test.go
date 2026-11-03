package jcs_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/jcs"
)

// benchSink keeps results alive.
var benchSink []byte

// benchDocument is about 8 KB of JSON: members out of order, nested objects
// and arrays, numbers and escaped strings.
func benchDocument() []byte {
	var b strings.Builder
	b.WriteString(`{"z":"last","items":[`)
	for i := range 60 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"name":"item-` + strconv.Itoa(i) + `","price":` + strconv.FormatFloat(float64(i)*1.25, 'f', -1, 64) +
			`,"tags":["a","b\n"],"attrs":{"y":1e3,"x":true,"w":null},"id":` + strconv.Itoa(i*7919) + `}`)
	}
	b.WriteString(`],"a":{"nested":{"deep":[1,2,3]}}}`)
	return []byte(b.String())
}

// BenchmarkCanonicalize8KB canonicalizes about 8 KB of JSON.
func BenchmarkCanonicalize8KB(b *testing.B) {
	doc := benchDocument()
	b.SetBytes(int64(len(doc)))
	b.ReportAllocs()
	for b.Loop() {
		out, err := jcs.Canonicalize(doc)
		if err != nil {
			b.Fatal(err)
		}
		benchSink = out
	}
}

// BenchmarkMarshalJWKMembers canonicalizes a JWK's four required members
// from a map — what jwk's thumbprint does.
func BenchmarkMarshalJWKMembers(b *testing.B) {
	m := map[string]string{"crv": "P-256", "kty": "EC", "x": strings.Repeat("A", 43), "y": strings.Repeat("B", 43)}
	b.ReportAllocs()
	for b.Loop() {
		out, err := jcs.Marshal(m)
		if err != nil {
			b.Fatal(err)
		}
		benchSink = out
	}
}

// BenchmarkCanonicalizeJWKMembers canonicalizes the same members already
// encoded.
func BenchmarkCanonicalizeJWKMembers(b *testing.B) {
	doc := []byte(`{"y":"` + strings.Repeat("B", 43) + `","x":"` + strings.Repeat("A", 43) + `","kty":"EC","crv":"P-256"}`)
	b.ReportAllocs()
	for b.Loop() {
		out, err := jcs.Canonicalize(doc)
		if err != nil {
			b.Fatal(err)
		}
		benchSink = out
	}
}
