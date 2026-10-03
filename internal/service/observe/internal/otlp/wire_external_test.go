// Package otlp_test — the shared wire: the proto3-JSON scalars, the common
// messages, the single-document marshal, the stream and the lenient decode of
// a collector's answer. Each signal's own suite pins the whole payload; this one
// pins the pieces both signals now borrow, and that every refusal leaves under
// the CALLER'S code.
package otlp_test

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/internal/otlp"
)

// concurrentEmits is how many goroutines share one Stream in the race test.
const concurrentEmits int = 16

// The two extremes the 64-bit scalars must spell exactly — both past 2^53,
// which is the whole reason they ride as strings.
const (
	minInt64  otlp.Int64  = math.MinInt64
	maxUint64 otlp.Uint64 = math.MaxUint64
)

// The two failures a test hands the wire, standing in for two signals'
// EXPORT_FAILED. Different codes on purpose: the property is that a fault
// leaves under the code the CALLER passed.
var (
	firstFailure = errs.WrapParams{
		Code:    errs.Pack(0, 3, 0xFE, 1),
		Reason:  "FIRST_EXPORT_FAILED",
		Public:  "The first signal's exporter failed",
		Private: "otlp_test: a stand-in for one signal's EXPORT_FAILED",
	}
	secondFailure = errs.WrapParams{
		Code:    errs.Pack(0, 3, 0xFE, 2),
		Reason:  "SECOND_EXPORT_FAILED",
		Public:  "The second signal's exporter failed",
		Private: "otlp_test: a stand-in for another signal's EXPORT_FAILED",
	}
)

// failingWriter refuses every write with boom.
type failingWriter struct{ boom error }

// Write fails, always.
func (w failingWriter) Write([]byte) (int, error) { return 0, w.boom }

// lockedBuffer is a bytes.Buffer safe to write from several goroutines, so the
// race test checks the Stream's lock and not the buffer's absence of one.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p under the buffer's own lock.
func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns everything written so far.
func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestScalarsSpellWhatProto3JSONSpells pins the three encodings encoding/json
// would get wrong on its own: a 64-bit integer as a DECIMAL STRING (a JSON
// number is a double, and every timestamp is past 2^53), and a double with its
// three non-finite values NAMED rather than refused.
func TestScalarsSpellWhatProto3JSONSpells(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		value interface{ MarshalJSON() ([]byte, error) }
		want  string
	}
	tests := []tc{
		{"a signed integer is a quoted decimal", otlp.Int64(-42), `"-42"`},
		{"the signed minimum", minInt64, `"-9223372036854775808"`},
		{"an unsigned integer is a quoted decimal", maxUint64, `"18446744073709551615"`},
		{"a finite double is a number", otlp.Double(1.5), `1.5`},
		{"a large double keeps its exponent", otlp.Double(1e21), `1e+21`},
		{"NaN is named", otlp.Double(math.NaN()), `"NaN"`},
		{"+Inf is named", otlp.Double(math.Inf(1)), `"Infinity"`},
		{"-Inf is named", otlp.Double(math.Inf(-1)), `"-Infinity"`},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := c.value.MarshalJSON()
		if err != nil || string(got) != c.want {
			t.Errorf("MarshalJSON = %s, %v; want %s", got, err, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAttrsCarryTheKindAndKeepPresence pins the AnyValue oneof: one case per
// attribute kind, emitted even at its zero — false is a value, not an absence
// — and the dimensionless set omitted rather than emitted empty.
func TestAttrsCarryTheKindAndKeepPresence(t *testing.T) {
	t.Parallel()
	if otlp.Attrs(nil) != nil {
		t.Error("an empty attribute set must stay nil, so omitempty drops it")
	}
	doc, err := otlp.Marshal(otlp.Attrs([]coreotel.AttrValue{
		coreotel.Bool("cache.hit", false),
		coreotel.Float64("ratio", 0),
		coreotel.Int64("http.response.status_code", 200),
		coreotel.String("url.full", "https://x/?a=1&b=2"),
	}), &firstFailure)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `[{"key":"cache.hit","value":{"boolValue":false}},` +
		`{"key":"ratio","value":{"doubleValue":0}},` +
		`{"key":"http.response.status_code","value":{"intValue":"200"}},` +
		`{"key":"url.full","value":{"stringValue":"https://x/?a=1&b=2"}}]`
	if string(doc) != want {
		t.Errorf("Attrs rendered\n%s\nwant\n%s", doc, want)
	}
	//: an attribute no constructor built selects no case at all.
	if got := otlp.KeyValueOf(coreotel.AttrValue{Key: "k"}); got.Value != (otlp.AnyValue{}) {
		t.Errorf("an unset attribute rendered %+v, want an empty AnyValue", got.Value)
	}
}

// TestMessagesCopyTheSharedModel pins the two once-per-payload messages: the
// Resource carries the shared model's attributes in their order, and the Scope
// omits a Version nobody declared rather than emitting it blank.
func TestMessagesCopyTheSharedModel(t *testing.T) {
	t.Parallel()
	resource := otlp.ResourceOf(coreotel.ResourceValue{Attrs: []coreotel.AttrValue{
		coreotel.String(coreotel.ServiceNameKey, "checkout"),
	}})
	scope := otlp.ScopeOf(coreotel.ScopeValue{Name: "github.com/acme/orders"})
	doc, err := otlp.Marshal(struct {
		Resource otlp.ResourceMessage `json:"resource"`
		Scope    otlp.ScopeMessage    `json:"scope"`
	}{resource, scope}, &firstFailure)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"checkout"}}]},` +
		`"scope":{"name":"github.com/acme/orders"}}`
	if string(doc) != want {
		t.Errorf("messages rendered\n%s\nwant\n%s", doc, want)
	}
}

// TestMarshalIsOneUnescapedDocument pins the two departures from json.Marshal:
// HTML escaping is OFF, so a URL's '&' stays '&', and there is no trailing
// newline, because an HTTP body is one document.
func TestMarshalIsOneUnescapedDocument(t *testing.T) {
	t.Parallel()
	doc, err := otlp.Marshal(map[string]string{"url": "a<b>&c"}, &firstFailure)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(doc) != `{"url":"a<b>&c"}` {
		t.Errorf("Marshal = %s, want the unescaped document", doc)
	}
	if bytes.HasSuffix(doc, []byte{otlp.DocumentTerminator}) {
		t.Error("a single document must not carry the stream terminator")
	}
}

// TestMarshalFailsUnderTheCallersCode pins the seam: a value encoding/json
// cannot render fails under the code the CALLER passed, with the cause on the
// trail — two callers, two codes, one fault.
func TestMarshalFailsUnderTheCallersCode(t *testing.T) {
	t.Parallel()
	for _, failure := range []errs.WrapParams{firstFailure, secondFailure} {
		_, err := otlp.Marshal(make(chan int), &failure)
		if !errs.HasCode(err, failure.Code) {
			t.Errorf("Marshal of a channel = %v, want the caller's %s", err, failure.Code)
		}
	}
}

// TestUnixNanoMapsTheUnsetInstantToZero pins the guard time.Time's own
// documentation makes necessary: the zero Time's UnixNano is undefined and,
// cast to uint64, a timestamp centuries away. Zero is what the schema already
// means by "unknown".
func TestUnixNanoMapsTheUnsetInstantToZero(t *testing.T) {
	t.Parallel()
	if got := otlp.UnixNano(time.Time{}); got != 0 {
		t.Errorf("UnixNano(zero) = %d, want 0", got)
	}
	if got := otlp.UnixNano(time.Unix(-1, 0)); got != 0 {
		t.Errorf("UnixNano(pre-1970) = %d, want 0", got)
	}
	instant := time.Unix(1_700_000_000, 123)
	if got := otlp.UnixNano(instant); uint64(got) != uint64(instant.UnixNano()) {
		t.Errorf("UnixNano = %d, want %d", got, instant.UnixNano())
	}
}

// TestStreamEmitsOneTerminatedDocumentPerWrite pins the writer-bound shape: a
// document and its terminator in ONE write — so concurrent exports cannot
// interleave inside a document — and a writer's fault under the caller's code.
func TestStreamEmitsOneTerminatedDocumentPerWrite(t *testing.T) {
	t.Parallel()
	var sink lockedBuffer
	stream := otlp.NewStream(&sink, &firstFailure)
	var wg sync.WaitGroup
	for range concurrentEmits {
		wg.Go(func() {
			if err := stream.Emit([]byte(`{"doc":"whole"}`)); err != nil {
				t.Errorf("Emit: %v", err)
			}
		})
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(sink.String(), "\n"), "\n")
	if len(lines) != concurrentEmits {
		t.Fatalf("%d lines for %d documents", len(lines), concurrentEmits)
	}
	for _, line := range lines {
		if line != `{"doc":"whole"}` {
			t.Errorf("a document was torn: %q", line)
		}
	}
	boom := errors.New("disk full")
	err := otlp.NewStream(failingWriter{boom: boom}, &secondFailure).Emit([]byte(`{}`))
	if !errors.Is(err, boom) || !errs.HasCode(err, secondFailure.Code) {
		t.Errorf("a writer fault = %v, want the cause under the caller's %s", err, secondFailure.Code)
	}
}
