// Package otlp_test — what one OTLP/JSON document costs to marshal, and the
// pooled-buffer alternative kept beside it as a CONTROL, because "borrow a
// buffer instead of a fresh bytes.Buffer per export" is the obvious
// optimisation and it was measured not to pay (BENCH.md).
package otlp_test

import (
	"bytes"
	"encoding/json"
	"sync"
	"testing"
	"time"

	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/internal/otlp"
)

// The two payload sizes: a quiet export and a busy one.
const (
	smallPayload int = 10
	largePayload int = 1000
)

// benchPoint is a NumberDataPoint-shaped message: two timestamps, a oneof
// value and three attributes — the shape both signals' payloads are made of.
type benchPoint struct {
	Start      otlp.Uint64     `json:"startTimeUnixNano"`
	End        otlp.Uint64     `json:"timeUnixNano"`
	AsInt      *otlp.Int64     `json:"asInt,omitempty"`
	Attributes []otlp.KeyValue `json:"attributes,omitempty"`
}

// benchRequest is a one-Resource, one-Scope payload of points.
type benchRequest struct {
	Resource otlp.ResourceMessage `json:"resource"`
	Scope    otlp.ScopeMessage    `json:"scope"`
	Points   []benchPoint         `json:"dataPoints"`
}

// pooledEncoder is the CONTROL: a bytes.Buffer and the json.Encoder bound to
// it, recycled across exports, the document copied out because it is returned.
type pooledEncoder struct {
	buf bytes.Buffer
	enc *json.Encoder
}

var (
	// benchFailure is the wrap the marshal would fail under; it never does.
	benchFailure = errs.WrapParams{
		Code:    errs.Pack(0, 3, 0xFE, 9),
		Reason:  "BENCH_EXPORT_FAILED",
		Public:  "The bench exporter failed",
		Private: "otlp_test: never reached",
	}
	// encoderPool recycles the control's buffer and encoder.
	encoderPool = sync.Pool{New: func() any {
		pooled := &pooledEncoder{}
		pooled.enc = json.NewEncoder(&pooled.buf)
		pooled.enc.SetEscapeHTML(false)
		return pooled
	}}
	// docSink keeps the compiler from proving a document dead.
	docSink []byte
)

// benchTree builds a payload of n points, as an encoder would per export.
func benchTree(n int) benchRequest {
	now := time.Unix(1_700_000_000, 0)
	points := make([]benchPoint, n)
	for i := range points {
		points[i] = benchPoint{
			Start: otlp.UnixNano(now),
			End:   otlp.UnixNano(now),
			AsInt: new(otlp.Int64(int64(i))),
			Attributes: otlp.Attrs([]coreotel.AttrValue{
				coreotel.String("http.request.method", "GET"),
				coreotel.String("http.route", "/v1/orders/{id}"),
				coreotel.Int64("http.response.status_code", 200),
			}),
		}
	}
	return benchRequest{
		Resource: otlp.ResourceOf(coreotel.ResourceValue{Attrs: []coreotel.AttrValue{
			coreotel.String(coreotel.ServiceNameKey, "checkout"),
		}}),
		Scope:  otlp.ScopeOf(coreotel.ScopeValue{Name: "github.com/acme/orders"}),
		Points: points,
	}
}

// marshalPooled is the control's marshal: a recycled buffer, the document
// copied out of it.
func marshalPooled(request benchRequest) ([]byte, error) {
	pooled, _ := encoderPool.Get().(*pooledEncoder)
	pooled.buf.Reset()
	defer encoderPool.Put(pooled)
	if err := pooled.enc.Encode(request); err != nil {
		return nil, err
	}
	return bytes.Clone(bytes.TrimSuffix(pooled.buf.Bytes(), []byte{otlp.DocumentTerminator})), nil
}

// runMarshal measures marshal on a prebuilt tree of n points.
func runMarshal(b *testing.B, n int, marshal func(benchRequest) ([]byte, error)) {
	tree := benchTree(n)
	b.ReportAllocs()
	for b.Loop() {
		doc, err := marshal(tree)
		if err != nil {
			b.Fatal(err)
		}
		docSink = doc
	}
}

// shared is the marshal the two signals call.
func shared(request benchRequest) ([]byte, error) {
	return otlp.Marshal(request, &benchFailure)
}

// BenchmarkMarshal_10 and _1000 price the shared marshal alone, on a tree
// built once.
func BenchmarkMarshal_10(b *testing.B)   { runMarshal(b, smallPayload, shared) }
func BenchmarkMarshal_1000(b *testing.B) { runMarshal(b, largePayload, shared) }

// BenchmarkMarshal_10_PooledControl and _1000_PooledControl price the pooled
// alternative on the same trees. Kept as controls, not as candidates.
func BenchmarkMarshal_10_PooledControl(b *testing.B) { runMarshal(b, smallPayload, marshalPooled) }

func BenchmarkMarshal_1000_PooledControl(b *testing.B) { runMarshal(b, largePayload, marshalPooled) }

// BenchmarkBuildAndMarshal_1000 prices what an export actually pays: the tree
// built from the model, then marshalled — which is where the allocations are.
func BenchmarkBuildAndMarshal_1000(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		doc, err := otlp.Marshal(benchTree(largePayload), &benchFailure)
		if err != nil {
			b.Fatal(err)
		}
		docSink = doc
	}
}
