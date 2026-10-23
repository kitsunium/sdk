// Package otlp_test — the appenders against the implementation they replace:
// every byte AppendString, the scalars' AppendJSON and the messages' AppendJSON
// write must be the byte encoding/json writes through Marshal for the same
// value. encoding/json is the oracle, so nothing here is checked against
// itself.
package otlp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// appendPrefix is what every appender is handed as dst, so a rendering that
// ignored dst — or overwrote it — would fail.
const appendPrefix string = "prefix:"

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

// appendAttrs is the attribute corpus: every AnyValue case, each at its zero
// and away from it, and the invalid kind that selects no case.
var appendAttrs = []coreotel.AttrValue{
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

// appendCase is one value and the appender that must render it as Marshal
// does.
type appendCase struct {
	name   string
	value  any
	render func(dst []byte) []byte
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

// stringCases is the string corpus as cases.
func stringCases() []appendCase {
	cases := make([]appendCase, 0, len(appendStrings))
	for _, s := range appendStrings {
		cases = append(cases, appendCase{
			name:   fmt.Sprintf("%q", s),
			value:  s,
			render: func(dst []byte) []byte { return otlp.AppendString(dst, s) },
		})
	}
	return cases
}

// scalarCases is each scalar at the extremes the specification allows.
func scalarCases() []appendCase {
	var cases []appendCase
	for _, v := range []int64{0, 1, -1, 1 << 53, 1<<53 + 1, math.MinInt64, math.MaxInt64} {
		cases = append(cases, appendCase{name: fmt.Sprintf("Int64(%d)", v), value: otlp.Int64(v), render: otlp.Int64(v).AppendJSON})
	}
	for _, v := range []uint64{0, 1, 1 << 53, 1<<53 + 1, math.MaxInt64, math.MaxUint64} {
		cases = append(cases, appendCase{name: fmt.Sprintf("Uint64(%d)", v), value: otlp.Uint64(v), render: otlp.Uint64(v).AppendJSON})
	}
	doubles := []float64{
		0, math.Copysign(0, -1), 1, -1.5, 0.1, 0.25, 1e20, 1e21, 1e-6, 1e-7, 123456789.123,
		math.MaxFloat64, -math.MaxFloat64, math.SmallestNonzeroFloat64, math.NaN(), math.Inf(1), math.Inf(-1),
	}
	for _, v := range doubles {
		cases = append(cases, appendCase{name: fmt.Sprintf("Double(%v)", v), value: otlp.Double(v), render: otlp.Double(v).AppendJSON})
	}
	return cases
}

// messageCases is every shared message, every AnyValue case and every
// omission included.
func messageCases() []appendCase {
	var cases []appendCase
	for i := range appendAttrs {
		kv := otlp.KeyValueOf(appendAttrs[i])
		cases = append(cases, appendCase{name: fmt.Sprintf("KeyValue(%q)", kv.Key), value: kv, render: kv.AppendJSON})
	}
	for _, set := range [][]coreotel.AttrValue{nil, {}, appendAttrs[:1], appendAttrs} {
		kvs := otlp.Attrs(set)
		if kvs == nil {
			kvs = []otlp.KeyValue{}
		}
		resource := otlp.ResourceOf(coreotel.ResourceValue{Attrs: set})
		cases = append(cases,
			appendCase{
				name:   fmt.Sprintf("AppendKeyValues(%d)", len(set)),
				value:  kvs,
				render: func(dst []byte) []byte { return otlp.AppendKeyValues(dst, kvs) },
			},
			appendCase{name: fmt.Sprintf("ResourceMessage(%d)", len(set)), value: resource, render: resource.AppendJSON},
		)
	}
	for _, value := range []coreotel.ScopeValue{{}, {Name: "n"}, {Version: "v"}, {Name: "a\"b", Version: "1.0<>"}} {
		scope := otlp.ScopeOf(value)
		cases = append(cases, appendCase{name: fmt.Sprintf("ScopeMessage(%+v)", value), value: scope, render: scope.AppendJSON})
	}
	return cases
}

// TestAppendersAreMarshalsRendering pins every appender to encoding/json: the
// string corpus, the scalars at their extremes and the shared messages, each
// appended after a prefix.
func TestAppendersAreMarshalsRendering(t *testing.T) {
	runCase := func(t *testing.T, c appendCase) {
		t.Helper()
		want := append([]byte(appendPrefix), oracle(t, c.value)...)
		if got := c.render([]byte(appendPrefix)); !bytes.Equal(got, want) {
			t.Errorf("%s rendered %s, want %s", c.name, got, want)
		}
	}
	groups := []struct {
		name  string
		cases []appendCase
	}{
		{name: "AppendString", cases: stringCases()},
		{name: "scalars", cases: scalarCases()},
		{name: "messages", cases: messageCases()},
	}
	for _, group := range groups {
		t.Run(group.name, func(t *testing.T) {
			for _, c := range group.cases {
				runCase(t, c)
			}
		})
	}
}

// FuzzAppendStringIsMarshalsRendering extends the string corpus to whatever
// the fuzzer finds; under go test it runs the seeds.
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

// TestAppendKeySeparatesMembers pins the comma rule: none after an opening
// brace, one after any value.
func TestAppendKeySeparatesMembers(t *testing.T) {
	type tc struct {
		name   string
		render func() []byte
		want   string
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		doc := c.render()
		if string(doc) != c.want {
			t.Errorf("%s: members = %s, want %s", c.name, doc, c.want)
		}
		if !json.Valid(doc) {
			t.Errorf("%s: members %s are not valid JSON", c.name, doc)
		}
	}
	cases := []tc{{
		name: "a literal, null, an empty object and a literal",
		render: func() []byte {
			doc := otlp.AppendKey([]byte("{"), "a")
			doc = otlp.AppendBool(doc, false)
			doc = otlp.AppendKey(doc, "b")
			doc = otlp.AppendNull(doc)
			doc = otlp.AppendKey(doc, "c")
			doc = append(doc, '{', '}')
			doc = otlp.AppendKey(doc, "d")
			doc = otlp.AppendBool(doc, true)
			return append(doc, '}')
		},
		want: `{"a":false,"b":null,"c":{},"d":true}`,
	}}
	for _, c := range cases {
		runCase(t, c)
	}
}
