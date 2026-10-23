// Package otlp_test — the appenders against the implementation they replace:
// every byte AppendString, the scalars' AppendJSON and the messages' AppendJSON
// write must be the byte encoding/json writes through Marshal for the same
// value. encoding/json is the oracle, so nothing here is checked against
// itself.
package otlp_test

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
	"unicode/utf8"

	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/internal/otlp"
)

// appendFailure is the wrap the oracle's Marshal would fail under; it never does.
var appendFailure = errs.WrapParams{
	Code:    errs.Pack(0, 3, 0xFE, 10),
	Reason:  "APPEND_ORACLE_FAILED",
	Public:  "The append oracle failed",
	Private: "otlp_test: never reached",
}

// appendStrings is the string corpus: every escape class encoding/json has,
// with HTML escaping off, and every UTF-8 defect it repairs.
var appendStrings = []string{
	"",
	"plain",
	`quote " and backslash \ inside`,
	"\x00\x01\x02\x07\x0b\x0e\x1f",
	"\b\f\n\r\t",
	"\x7f delete stays",
	"<script>&amp;</script>",
	"https://example.test/a?b=1&c=<2>",
	"line\xe2\x80\xa8separator\xe2\x80\xa9paragraph",
	"é漢字🙂 multi-byte",
	"\xff\xfe invalid leading bytes",
	"truncated \xe2\x82",
	"overlong \xc0\xaf slash",
	"surrogate \xed\xa0\x80 half",
	"literal \xef\xbf\xbd replacement",
	"mixed \"\x00<\xe2\x80\xa8\xff>\\ end",
	"\xe2\x80\xa8",
	"\xff",
}

// oracle renders v the way Marshal does, as the reference.
func oracle(t *testing.T, v any) []byte {
	t.Helper()
	doc, err := otlp.Marshal(v, &appendFailure)
	if err != nil {
		t.Fatalf("Marshal(%#v): %v", v, err)
	}
	return doc
}

// TestAppendStringIsMarshalsRendering pins AppendString to encoding/json on the
// corpus, appended after a prefix so a run that ignores dst would fail.
func TestAppendStringIsMarshalsRendering(t *testing.T) {
	for _, s := range appendStrings {
		want := oracle(t, s)
		got := otlp.AppendString([]byte("prefix:"), s)
		if !bytes.Equal(got, append([]byte("prefix:"), want...)) {
			t.Errorf("AppendString(%q) = %s, want prefix:%s", s, got, want)
		}
	}
}

// FuzzAppendStringIsMarshalsRendering extends the corpus to whatever the
// fuzzer finds; under go test it runs the seeds.
func FuzzAppendStringIsMarshalsRendering(f *testing.F) {
	for _, s := range appendStrings {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		want := oracle(t, s)
		if got := otlp.AppendString(nil, s); !bytes.Equal(got, want) {
			t.Errorf("AppendString(%q) = %s, want %s", s, got, want)
		}
		if !utf8.Valid(otlp.AppendString(nil, s)) {
			t.Errorf("AppendString(%q) is not valid UTF-8", s)
		}
	})
}

// TestScalarAppendJSONIsMarshalJSON pins each scalar's AppendJSON to its own
// MarshalJSON, through encoding/json, at the extremes the specification allows.
func TestScalarAppendJSONIsMarshalJSON(t *testing.T) {
	ints := []int64{0, 1, -1, 1 << 53, 1<<53 + 1, math.MinInt64, math.MaxInt64}
	for _, v := range ints {
		if got, want := otlp.Int64(v).AppendJSON(nil), oracle(t, otlp.Int64(v)); !bytes.Equal(got, want) {
			t.Errorf("Int64(%d).AppendJSON = %s, want %s", v, got, want)
		}
	}
	uints := []uint64{0, 1, 1 << 53, 1<<53 + 1, math.MaxInt64, math.MaxUint64}
	for _, v := range uints {
		if got, want := otlp.Uint64(v).AppendJSON(nil), oracle(t, otlp.Uint64(v)); !bytes.Equal(got, want) {
			t.Errorf("Uint64(%d).AppendJSON = %s, want %s", v, got, want)
		}
	}
	doubles := []float64{
		0, math.Copysign(0, -1), 1, -1.5, 0.1, 0.25, 1e20, 1e21, 1e-6, 1e-7, 123456789.123,
		math.MaxFloat64, -math.MaxFloat64, math.SmallestNonzeroFloat64, math.NaN(), math.Inf(1), math.Inf(-1),
	}
	for _, v := range doubles {
		if got, want := otlp.Double(v).AppendJSON(nil), oracle(t, otlp.Double(v)); !bytes.Equal(got, want) {
			t.Errorf("Double(%v).AppendJSON = %s, want %s", v, got, want)
		}
	}
}

// TestMessageAppendJSONIsMarshalsRendering pins the shared messages, every
// AnyValue case and every omission included, to encoding/json.
func TestMessageAppendJSONIsMarshalsRendering(t *testing.T) {
	attrs := []coreotel.AttrValue{
		coreotel.String("s", "<a&b>\xe2\x80\xa8\xff"),
		coreotel.String("", ""),
		coreotel.Bool("t", true),
		coreotel.Bool("f", false),
		coreotel.Int64("min", math.MinInt64),
		coreotel.Int64("zero", 0),
		coreotel.Float64("nan", math.NaN()),
		coreotel.Float64("inf", math.Inf(-1)),
		coreotel.Float64("tiny", 1e-7),
		{Key: "invalid \"kind\""},
	}
	for i := range attrs {
		kv := otlp.KeyValueOf(attrs[i])
		if got, want := kv.AppendJSON(nil), oracle(t, kv); !bytes.Equal(got, want) {
			t.Errorf("KeyValue(%q).AppendJSON = %s, want %s", attrs[i].Key, got, want)
		}
	}
	sets := [][]coreotel.AttrValue{nil, {}, attrs[:1], attrs}
	for _, set := range sets {
		kvs := otlp.Attrs(set)
		if kvs == nil {
			kvs = []otlp.KeyValue{}
		}
		if got, want := otlp.AppendKeyValues(nil, kvs), oracle(t, kvs); !bytes.Equal(got, want) {
			t.Errorf("AppendKeyValues(%d) = %s, want %s", len(set), got, want)
		}
		resource := otlp.ResourceOf(coreotel.ResourceValue{Attrs: set})
		if got, want := resource.AppendJSON(nil), oracle(t, resource); !bytes.Equal(got, want) {
			t.Errorf("ResourceMessage(%d).AppendJSON = %s, want %s", len(set), got, want)
		}
	}
	scopes := []coreotel.ScopeValue{{}, {Name: "n"}, {Version: "v"}, {Name: "a\"b", Version: "1.0<>"}}
	for _, value := range scopes {
		scope := otlp.ScopeOf(value)
		if got, want := scope.AppendJSON(nil), oracle(t, scope); !bytes.Equal(got, want) {
			t.Errorf("ScopeMessage(%+v).AppendJSON = %s, want %s", value, got, want)
		}
	}
}

// TestAppendKeySeparatesMembers pins the comma rule: none after an opening
// brace, one after any value.
func TestAppendKeySeparatesMembers(t *testing.T) {
	doc := otlp.AppendKey([]byte("{"), "a")
	doc = otlp.AppendBool(doc, false)
	doc = otlp.AppendKey(doc, "b")
	doc = otlp.AppendNull(doc)
	doc = otlp.AppendKey(doc, "c")
	doc = append(doc, '{', '}')
	doc = otlp.AppendKey(doc, "d")
	doc = otlp.AppendBool(doc, true)
	doc = append(doc, '}')
	const want = `{"a":false,"b":null,"c":{},"d":true}`
	if string(doc) != want {
		t.Errorf("members = %s, want %s", doc, want)
	}
	if !json.Valid(doc) {
		t.Errorf("members %s are not valid JSON", doc)
	}
}
