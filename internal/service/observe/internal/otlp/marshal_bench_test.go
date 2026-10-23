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

// appended renders the bench tree the way both signals now render theirs:
// the appenders, into one buffer sized near the document.
func appended(request benchRequest) ([]byte, error) {
	dst := make([]byte, 0, 256+448*len(request.Points))
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "resource")
	dst = request.Resource.AppendJSON(dst)
	dst = otlp.AppendKey(dst, "scope")
	dst = request.Scope.AppendJSON(dst)
	dst = otlp.AppendKey(dst, "dataPoints")
	if request.Points == nil {
		dst = otlp.AppendNull(dst)
		return append(dst, '}'), nil
	}
	dst = append(dst, '[')
	for i := range request.Points {
		if i > 0 {
			dst = append(dst, ',')
		}
		point := &request.Points[i]
		dst = append(dst, '{')
		dst = otlp.AppendKey(dst, "startTimeUnixNano")
		dst = point.Start.AppendJSON(dst)
		dst = otlp.AppendKey(dst, "timeUnixNano")
		dst = point.End.AppendJSON(dst)
		if point.AsInt != nil {
			dst = otlp.AppendKey(dst, "asInt")
			dst = point.AsInt.AppendJSON(dst)
		}
		if len(point.Attributes) > 0 {
			dst = otlp.AppendKey(dst, "attributes")
			dst = otlp.AppendKeyValues(dst, point.Attributes)
		}
		dst = append(dst, '}')
	}
	return append(dst, ']', '}'), nil
}

// TestBenchAppendIsMarshal keeps the two benchmarked renderings the same
// document, so their numbers price the same bytes.
func TestBenchAppendIsMarshal(t *testing.T) {
	type tc struct {
		name   string
		points int
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		tree := benchTree(c.points)
		want, err := shared(tree)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := appended(tree); !bytes.Equal(got, want) {
			t.Fatalf("%s: append and Marshal differ:\n%s\n%s", c.name, got, want)
		}
	}
	cases := []tc{
		{name: "no point", points: 0},
		{name: "a quiet export", points: smallPayload},
		{name: "a busy export", points: largePayload},
	}
	for _, c := range cases {
		runCase(t, c)
	}
}

// BenchmarkAppend_10 and _1000 price the appenders on the trees
// BenchmarkMarshal_10 and _1000 marshal: the same document, written without
// reflection and without a slice per scalar.
func BenchmarkAppend_10(b *testing.B)   { runMarshal(b, smallPayload, appended) }
func BenchmarkAppend_1000(b *testing.B) { runMarshal(b, largePayload, appended) }

// BenchmarkBuildAndAppend_1000 is BenchmarkBuildAndMarshal_1000 through the
// appenders: what an export pays now.
func BenchmarkBuildAndAppend_1000(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		doc, err := appended(benchTree(largePayload))
		if err != nil {
			b.Fatal(err)
		}
		docSink = doc
	}
}
