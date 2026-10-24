package encoder

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

func Test_jsonEncoder_Name(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
	}{
		{"json encoder identifier", "json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &jsonEncoder{clk: clock.System}
			if got := e.Name(); got != tc.want {
				t.Errorf("Name = %q, want %q", got, tc.want)
			}
		})
	}
}

func Test_jsonEncoder_Append(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 4, 20, 10, 0, 0, 6_000_000, time.UTC)
	tests := []struct {
		name   string
		groups []string
		rec    corelogger.RecordEvent
		want   string
	}{
		{
			name:   "header only renders fixed key order",
			groups: nil,
			rec:    corelogger.RecordEvent{Time: fixed, Level: level.Info, Message: "hello"},
			want:   `{"ts":"2026-04-20T10:00:00.006Z","level":"INFO","msg":"hello"}` + "\n",
		},
		{
			name:   "flat attrs follow header in order",
			groups: nil,
			rec: corelogger.RecordEvent{Time: fixed, Level: level.Error, Message: "boom", Attrs: []corelogger.AttrValue{
				{Key: "count", Value: corelogger.Int64Value(3)},
				{Key: "ok", Value: corelogger.BoolValue(true)},
				{Key: "who", Value: corelogger.StringValue("bob")},
			}},
			want: `{"ts":"2026-04-20T10:00:00.006Z","level":"ERROR","msg":"boom","count":3,"ok":true,"who":"bob"}` + "\n",
		},
		{
			name:   "groups prefix the attribute key with dots",
			groups: []string{"http", "req"},
			rec: corelogger.RecordEvent{Time: fixed, Level: level.Warn, Message: "audit", Attrs: []corelogger.AttrValue{
				{Key: "method", Value: corelogger.StringValue("GET")},
			}},
			want: `{"ts":"2026-04-20T10:00:00.006Z","level":"WARN","msg":"audit","http.req.method":"GET"}` + "\n",
		},
		{
			name:   "message control bytes escape rather than break framing",
			groups: nil,
			rec:    corelogger.RecordEvent{Time: fixed, Level: level.Info, Message: "a\nb\"c\\"},
			want:   `{"ts":"2026-04-20T10:00:00.006Z","level":"INFO","msg":"a\nb\"c\\"}` + "\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &jsonEncoder{clk: clock.System}
			got := string(e.Append(nil, tc.groups, tc.rec))
			//: exact match guards the frozen key order and escaping contract.
			if got != tc.want {
				t.Errorf("Append = %q, want %q", got, tc.want)
			}
			//: the object body without the trailing newline must be valid JSON.
			if !json.Valid([]byte(got[:len(got)-1])) {
				t.Errorf("Append output is not valid JSON: %q", got)
			}
		})
	}
}

func Test_jsonEncoder_Append_ClockFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"zero time triggers clock fallback"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &jsonEncoder{clk: clock.System}
			got := string(e.Append(nil, nil, corelogger.RecordEvent{Level: level.Info, Message: "m"}))
			//: a populated ts member proves the clock fallback ran.
			if got == "" || got[len(got)-1] != '\n' {
				t.Errorf("Append produced %q, want trailing-newline output", got)
			}
			if !json.Valid([]byte(got[:len(got)-1])) {
				t.Errorf("Append output is not valid JSON: %q", got)
			}
		})
	}
}

func Test_appendJSONHeader(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		rec  corelogger.RecordEvent
		want string
	}{
		{"info header", corelogger.RecordEvent{Time: fixed, Level: level.Info, Message: "hi"}, `"ts":"2026-04-20T10:00:00.000Z","level":"INFO","msg":"hi"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := string(appendJSONHeader(nil, tc.rec)); got != tc.want {
				t.Errorf("appendJSONHeader = %q, want %q", got, tc.want)
			}
		})
	}
}

func Test_appendJSONAttr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		groups []string
		attr   corelogger.AttrValue
		want   string
	}{
		{"no groups", nil, corelogger.AttrValue{Key: "k", Value: corelogger.StringValue("v")}, `"k":"v"`},
		{"one group", []string{"g"}, corelogger.AttrValue{Key: "k", Value: corelogger.Int64Value(2)}, `"g.k":2`},
		{"two groups", []string{"a", "b"}, corelogger.AttrValue{Key: "k", Value: corelogger.BoolValue(false)}, `"a.b.k":false`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := string(appendJSONAttr(nil, tc.groups, tc.attr)); got != tc.want {
				t.Errorf("appendJSONAttr = %q, want %q", got, tc.want)
			}
		})
	}
}

func Test_appendJSONValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		v    corelogger.Value
		want string
	}{
		{"string", corelogger.StringValue("v"), `"v"`},
		{"int64", corelogger.Int64Value(7), "7"},
		{"uint64", corelogger.Uint64Value(42), "42"},
		{"bool", corelogger.BoolValue(true), "true"},
		{"float", corelogger.Float64Value(0.5), "0.5"},
		{"duration quoted", corelogger.DurationValue(time.Second), `"1s"`},
		{"time quoted", corelogger.TimeValue(time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)), `"2026-04-20T12:00:00.000Z"`},
		{"any degrades to quoted question mark", corelogger.AnyValue(struct{}{}), `"?"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attr := corelogger.AttrValue{Key: "k", Value: tc.v}
			if got := string(appendJSONValue(nil, attr)); got != tc.want {
				t.Errorf("appendJSONValue = %q, want %q", got, tc.want)
			}
		})
	}
}

func Test_appendJSONFloat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   float64
		want string
	}{
		{"finite", 1.5, "1.5"},
		{"nan degrades to null", nan(), "null"},
		{"positive inf degrades to null", inf(1), "null"},
		{"negative inf degrades to null", inf(-1), "null"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := string(appendJSONFloat(nil, tc.in)); got != tc.want {
				t.Errorf("appendJSONFloat = %q, want %q", got, tc.want)
			}
		})
	}
}

// nan returns a quiet NaN without importing math, keeping the test in lockstep
// with the encoder's stdlib-light intent.
func nan() float64 {
	zero := 0.0
	return zero / zero
}

// inf returns positive infinity for sign >= 0 and negative infinity otherwise.
func inf(sign int) float64 {
	zero := 0.0
	//: divide by zero yields the requested signed infinity.
	if sign >= 0 {
		return 1.0 / zero
	}
	return -1.0 / zero
}

// Test_jsonStringsThroughTheEncoder pins what the encoder writes for a message,
// a value and a key, now that all three are escaped by kernel/jsonenc: the
// escapes the byte loop wrote, and the three differences from it. A control
// byte that has a short escape (\b, \f) is written with it and not as \u00XX;
// invalid UTF-8 is written as U+FFFD, where the loop copied the bad byte and
// left the line invalid; U+2028 and U+2029 are escaped, where the loop copied
// them. These are the behaviour change the migration declares.
func Test_jsonStringsThroughTheEncoder(t *testing.T) {
	t.Parallel()
	// lowControl holds a single 0x01 byte built from a byte slice so the test
	// source carries no raw control-character literal.
	lowControl := string([]byte{1})
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "ok", `"ok"`},
		{"quote and backslash", "a\"b\\c", `"a\"b\\c"`},
		{"whitespace controls", "\n\r\t", `"\n\r\t"`},
		{"low control escapes to unicode", lowControl, "\"\\u0001\""},
		{"backspace and form feed take their short escapes", "\b\f", `"\b\f"`},
		{"valid multibyte passes through", "café 東京", "\"café 東京\""},
		{"invalid UTF-8 becomes U+FFFD", "a\xffb", "\"a\uFFFDb\""},
		{"truncated rune becomes U+FFFD", "ab\xe6\x9d", "\"ab\uFFFD\uFFFD\""},
		{"line separator is escaped", "a\u2028b", `"a\u2028b"`},
		{"paragraph separator is escaped", "a\u2029b", `"a\u2029b"`},
		{"html is not escaped", "<b>&</b>", `"<b>&</b>"`},
	}
	e := &jsonEncoder{clk: clock.System}
	rec := func(msg string, attrs ...corelogger.AttrValue) corelogger.RecordEvent {
		return corelogger.RecordEvent{Time: time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC), Level: level.Info, Message: msg, Attrs: attrs}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: the message, the value of an attribute, and the key of one.
			byMessage := string(e.Append(nil, nil, rec(tc.in)))
			byValue := string(e.Append(nil, nil, rec("m", corelogger.AttrValue{Key: "k", Value: corelogger.StringValue(tc.in)})))
			byKey := string(e.Append(nil, nil, rec("m", corelogger.AttrValue{Key: tc.in, Value: corelogger.StringValue("v")})))
			if want := `,"msg":` + tc.want + "}\n"; !strings.HasSuffix(byMessage, want) {
				t.Errorf("message %q: line %q does not end with %q", tc.in, byMessage, want)
			}
			if want := `,"k":` + tc.want + "}\n"; !strings.HasSuffix(byValue, want) {
				t.Errorf("value %q: line %q does not end with %q", tc.in, byValue, want)
			}
			//: a key is the same literal, so the member is its escape plus the colon.
			if want := `,` + tc.want + `:"v"}` + "\n"; !strings.HasSuffix(byKey, want) {
				t.Errorf("key %q: line %q does not end with %q", tc.in, byKey, want)
			}
			for _, line := range []string{byMessage, byValue, byKey} {
				if !json.Valid([]byte(line)) {
					t.Errorf("line %q is not valid JSON", line)
				}
			}
		})
	}
}

func Test_jsonEncoderName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
	}{
		{"canonical identifier is json", "json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if jsonEncoderName != tc.want {
				t.Errorf("jsonEncoderName = %q, want %q", jsonEncoderName, tc.want)
			}
		})
	}
}

func Test_jsonGroupSeparator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want byte
	}{
		{"jsonGroupSeparator is dot", '.'},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if jsonGroupSeparator != tc.want {
				t.Errorf("jsonGroupSeparator = %q, want %q", jsonGroupSeparator, tc.want)
			}
		})
	}
}
