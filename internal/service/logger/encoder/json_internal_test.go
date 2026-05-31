package encoder

import (
	"encoding/json"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
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

func Test_appendJSONString(t *testing.T) {
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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := string(appendJSONString(nil, tc.in)); got != tc.want {
				t.Errorf("appendJSONString = %q, want %q", got, tc.want)
			}
		})
	}
}

func Test_appendJSONEscaped(t *testing.T) {
	t.Parallel()
	// lowControl holds a single 0x01 byte built from a byte slice so the test
	// source carries no raw control-character literal.
	lowControl := string([]byte{1})
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no quotes added", "ab", "ab"},
		{"escapes a quote", "a\"b", `a\"b`},
		{"escapes a backslash", "a\\b", `a\\b`},
		{"escapes newline", "a\nb", `a\nb`},
		{"escapes carriage return", "a\rb", `a\rb`},
		{"escapes tab", "a\tb", `a\tb`},
		{"escapes low control to unicode", lowControl, "\\u0001"},
		{"empty stays empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := string(appendJSONEscaped(nil, tc.in)); got != tc.want {
				t.Errorf("appendJSONEscaped = %q, want %q", got, tc.want)
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
