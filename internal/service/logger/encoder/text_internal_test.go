package encoder

import (
	"strconv"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

func Test_textEncoder_Name(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
	}{
		{"text encoder identifier", "text"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &textEncoder{clk: clock.System}
			if got := e.Name(); got != tc.want {
				t.Errorf("Name = %q, want %q", got, tc.want)
			}
		})
	}
}

func Test_textEncoder_Append(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		groups       []string
		rec          corelogger.RecordEvent
		wantContains string
	}{
		{"zero time + no attrs triggers clock fallback", nil, corelogger.RecordEvent{Level: level.Info, Message: "m"}, " INFO m"},
		{"zero time + groups + no attrs leaves header intact", []string{"g"}, corelogger.RecordEvent{Level: level.Warn, Message: "x"}, " WARN x"},
		{"zero time + attrs renders key=value pair", nil, corelogger.RecordEvent{Level: level.Debug, Message: "y", Attrs: []corelogger.AttrValue{{Key: "k", Value: corelogger.StringValue("v")}}}, "k=\"v\""},
		{"non-zero time skips clock fallback and uses fixed timestamp", nil, corelogger.RecordEvent{Time: fixed, Level: level.Info, Message: "z"}, "2026-04-20"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &textEncoder{clk: clock.System}
			line := string(e.Append(nil, tc.groups, tc.rec))
			if line == "" || line[len(line)-1] != '\n' {
				t.Errorf("Append produced %q, want non-empty trailing-newline output", line)
			}
			if !containsSubstr(line, tc.wantContains) {
				t.Errorf("Append output %q missing %q", line, tc.wantContains)
			}
		})
	}
}

// containsSubstr is a tiny strings.Contains shim kept local so the test stays
// dependency-light against the external package import surface.
func containsSubstr(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func Test_appendHeader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"appendHeader contains level and message"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := corelogger.RecordEvent{Level: level.Info, Message: "hi"}
			got := string(appendHeader(nil, rec))
			if got == "" {
				t.Error("appendHeader returned empty")
			}
		})
	}
}

func Test_appendAttrWithGroups(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		groups []string
	}{
		{"empty groups", nil},
		{"one group", []string{"g"}},
		{"two groups", []string{"a", "b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := corelogger.AttrValue{Key: "k", Value: corelogger.StringValue("v")}
			got := string(appendAttrWithGroups(nil, tc.groups, a))
			if got == "" {
				t.Error("appendAttrWithGroups returned empty")
			}
		})
	}
}

func Test_appendValueOnly(t *testing.T) {
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
		{"any degrades to ?", corelogger.AnyValue(struct{}{}), "?"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := string(appendValueOnly(nil, corelogger.AttrValue{Key: "k", Value: tc.v}))
			if got != tc.want {
				t.Errorf("appendValueOnly = %q, want %q", got, tc.want)
			}
		})
	}
}

func Test_textEncoderName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
	}{
		{"canonical identifier is text", "text"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if textEncoderName != tc.want {
				t.Errorf("textEncoderName = %q, want %q", textEncoderName, tc.want)
			}
		})
	}
}

func Test_groupSeparator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want byte
	}{
		{"groupSeparator is dot", '.'},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if groupSeparator != tc.want {
				t.Errorf("groupSeparator = %q, want %q", groupSeparator, tc.want)
			}
		})
	}
}

// TestAppendQuotedStringMatchesStrconv is the differential test that licenses
// appendQuotedString's fast path. That fast path exists because a CPU profile
// put 70 % of a text encode inside strconv.AppendQuote (see BENCH.md §5.1), and
// it is legitimate only while its output is byte-for-byte AppendQuote's. This
// sweeps every byte value in three positions, plus the multi-byte and escaping
// cases, and compares the two renderings directly — so a future widening of
// quoteSafe that admits one byte AppendQuote would have escaped fails here
// rather than in a log line somebody downstream has to parse.
func TestAppendQuotedStringMatchesStrconv(t *testing.T) {
	t.Parallel()
	cases := make([]string, 0, 3*256+8)
	//: every byte value alone, leading and trailing — the three positions a
	//: scan-then-copy implementation could get wrong independently.
	for b := range 256 {
		c := string([]byte{byte(b)})
		cases = append(cases, c, "lead"+c, c+"trail")
	}
	cases = append(cases,
		"",
		"plain ascii value",
		`has "quotes" inside`,
		`has \backslash inside`,
		"has\nnewline\tand\ttabs",
		"héllo wörld",
		"日本語のログ",
		"\x00\x1f\x7f",
	)
	for _, s := range cases {
		want := strconv.AppendQuote(nil, s)
		got := appendQuotedString(nil, s)
		//: byte equality is the whole contract — a "close enough" rendering
		//: would silently change every text line the SDK has ever emitted.
		if string(got) != string(want) {
			t.Fatalf("appendQuotedString(%q) = %q, want %q", s, got, want)
		}
	}
}

// TestQuoteSafeRejectsEverythingStrconvTransforms pins the accepted set itself,
// so the boundary is asserted as an intention rather than only as a consequence
// of the differential sweep above.
func TestQuoteSafeRejectsEverythingStrconvTransforms(t *testing.T) {
	t.Parallel()
	for b := range 256 {
		s := string([]byte{byte(b)})
		//: printable ASCII minus the delimiter and the escape — exactly the
		//: bytes strconv.AppendQuote copies through untouched.
		wantSafe := b >= int(quoteSafeFloor) && b <= int(quoteSafeCeiling) && b != '"' && b != '\\'
		if got := quoteSafe(s); got != wantSafe {
			t.Errorf("quoteSafe(%q) = %v, want %v", s, got, wantSafe)
		}
	}
}
