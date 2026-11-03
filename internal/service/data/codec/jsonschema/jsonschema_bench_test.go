package jsonschema_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/jsonschema"
)

// benchOrder is an order document of about 10 KiB: the orderSchema shape,
// with many items.
func benchOrder() []byte {
	var b strings.Builder
	b.WriteString(`{"id":"ord_bench","customer":{"email":"buyer@example.com"},"total":12345.67,"items":[`)
	for i := range 220 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"sku":"sku-` + strconv.Itoa(i) + `","qty":` + strconv.Itoa(i%9+1) + `}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

// BenchmarkNewValidator compiles orderSchema: once per schema in a server.
func BenchmarkNewValidator(b *testing.B) {
	schema := []byte(orderSchema)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := jsonschema.NewValidator(schema, jsonschema.Config{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkValidate10KiB validates a valid order of about 10 KiB: once per
// request in a server.
func BenchmarkValidate10KiB(b *testing.B) {
	v, err := jsonschema.NewValidator([]byte(orderSchema), jsonschema.Config{AssertFormats: []string{"email"}})
	if err != nil {
		b.Fatal(err)
	}
	doc := benchOrder()
	b.SetBytes(int64(len(doc)))
	b.ReportAllocs()
	for b.Loop() {
		report, verr := v.Validate(doc, 1<<20)
		if verr != nil || !report.OK() {
			b.Fatal(verr, report.Paths())
		}
	}
}
