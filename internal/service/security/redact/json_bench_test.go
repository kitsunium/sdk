package redact_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/service/security/redact"
)

// jsonBenchSink keeps the result observable, so the call cannot be deleted.
var jsonBenchSink int

// BenchmarkJSON prices the redaction of one document whose cost is its
// strings: the quoting of each name and value on the way out.
func BenchmarkJSON(b *testing.B) {
	long := strings.Repeat("GET /api/v1/organizations/4f3a9c1e/projects ", 4)
	docs := map[string]string{
		"short":   `{"id":"a1","name":"alpha","state":"active","region":"eu"}`,
		"long":    `{"message":"` + long + `","path":"` + long + `","note":"` + long + `"}`,
		"unicode": `{"titre":"déploiement terminé avec succès","région":"東京リージョン","ok":"✓"}`,
	}
	r := redact.NewRedactor(redact.Config{})
	for name, doc := range docs {
		document := []byte(doc)
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(document)))
			b.ReportAllocs()
			for b.Loop() {
				out, err := r.JSON(document, 4096)
				if err != nil {
					b.Fatal(err)
				}
				jsonBenchSink = len(out.JSON)
			}
		})
	}
}
