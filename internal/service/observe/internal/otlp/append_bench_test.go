package otlp_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/service/observe/internal/otlp"
)

// appendBenchSink keeps the appender's result observable, so the call cannot
// be deleted.
var appendBenchSink []byte

// BenchmarkAppendString prices one string appender call on a warm buffer, by
// the shapes a name or a message takes: short and plain, long with two
// escapes, and non-ASCII with none.
func BenchmarkAppendString(b *testing.B) {
	cases := []struct{ name, in string }{
		{"short", "http.server.duration"},
		{"long", strings.Repeat("GET /api/v1/organizations/4f3a9c1e/projects ", 4) + "said \"no\"\n"},
		{"unicode", "déploiement terminé avec succès — 東京リージョン ✓"},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			buf := make([]byte, 0, 1024)
			b.SetBytes(int64(len(c.in)))
			b.ReportAllocs()
			for b.Loop() {
				appendBenchSink = otlp.AppendString(buf[:0], c.in)
			}
		})
	}
}
