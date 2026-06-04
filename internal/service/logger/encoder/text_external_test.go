package encoder_test

import (
	"strings"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/logger/encoder"
)

func TestNewText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		clk  clock.Clock
	}{
		{"system clock", clock.System},
		{"nil clock falls back to system", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			enc := encoder.NewText(tc.clk)
			if enc == nil {
				t.Fatal("NewText returned nil")
			}
			if enc.Name() != "text" {
				t.Errorf("Name = %q, want text", enc.Name())
			}
		})
	}
}

func TestTextEncoder_Append(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		groups      []string
		rec         corelogger.RecordEvent
		wantInLine  []string
		wantSuffixN bool
	}{
		{
			name:        "header + string attr renders unquoted level",
			groups:      nil,
			rec:         corelogger.RecordEvent{Level: level.Info, Message: "m", Attrs: []corelogger.AttrValue{{Key: "k", Value: corelogger.StringValue("v")}}},
			wantInLine:  []string{"INFO", "m", `k="v"`},
			wantSuffixN: true,
		},
		{
			name:        "groups prefix the attribute key",
			groups:      []string{"http"},
			rec:         corelogger.RecordEvent{Level: level.Warn, Message: "audit", Attrs: []corelogger.AttrValue{{Key: "method", Value: corelogger.StringValue("GET")}}},
			wantInLine:  []string{"WARN", "audit", `http.method="GET"`},
			wantSuffixN: true,
		},
		{
			name:        "duration renders as quoted string",
			groups:      nil,
			rec:         corelogger.RecordEvent{Level: level.Debug, Message: "x", Attrs: []corelogger.AttrValue{{Key: "d", Value: corelogger.DurationValue(time.Second)}}},
			wantInLine:  []string{"DEBUG", `d="1s"`},
			wantSuffixN: true,
		},
		{
			name:        "time renders with the header timestampLayout (RFC3339-with-millis)",
			groups:      nil,
			rec:         corelogger.RecordEvent{Level: level.Info, Message: "x", Attrs: []corelogger.AttrValue{{Key: "t", Value: corelogger.TimeValue(time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC))}}},
			wantInLine:  []string{"t=2026-04-20T12:00:00.000Z"},
			wantSuffixN: true,
		},
		{
			name:        "unknown kind degrades to ?",
			groups:      nil,
			rec:         corelogger.RecordEvent{Level: level.Info, Message: "x", Attrs: []corelogger.AttrValue{{Key: "z", Value: corelogger.AnyValue(struct{}{})}}},
			wantInLine:  []string{"z=?"},
			wantSuffixN: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			enc := encoder.NewText(clock.System)
			line := string(enc.Append(nil, tc.groups, tc.rec))
			for _, needle := range tc.wantInLine {
				if !strings.Contains(line, needle) {
					t.Errorf("line missing %q: %q", needle, line)
				}
			}
			if tc.wantSuffixN && !strings.HasSuffix(line, "\n") {
				t.Errorf("line missing trailing newline: %q", line)
			}
		})
	}
}

// TestTextEncoder_Append_StripsFramingSensitiveBytes asserts that Message
// bytes CR, LF, and NUL never reach the encoded line — otherwise a syslog
// or line-tailed-file sink could see attacker-injected frame boundaries.
func TestTextEncoder_Append_StripsFramingSensitiveBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{
			name: "every framing-sensitive byte collapses to space",
			//: this message contains every framing-sensitive byte the sanitizer
			//: must replace; a naive append would spray newlines into the frame.
			message: "before\nmid\rend\x00tail",
			want:    "before mid end tail",
		},
		{
			name:    "lone LF between tokens scrubs to single space",
			message: "alpha\nbeta",
			want:    "alpha beta",
		},
		{
			name:    "lone CR between tokens scrubs to single space",
			message: "alpha\rbeta",
			want:    "alpha beta",
		},
		{
			name:    "lone NUL between tokens scrubs to single space",
			message: "alpha\x00beta",
			want:    "alpha beta",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			enc := encoder.NewText(clock.System)
			rec := corelogger.RecordEvent{Level: level.Info, Message: tc.message}
			line := string(enc.Append(nil, nil, rec))
			//: the trailing record-terminator newline is expected; strip it for
			//: the check so a newline from the Message would be caught.
			payload := strings.TrimSuffix(line, "\n")
			if strings.ContainsAny(payload, "\n\r\x00") {
				t.Errorf("sanitizer leaked a framing byte: %q", payload)
			}
			//: each scrubbed byte must become a single space so the Message stays
			//: recoverable in the line ordering; no collapse / no deletion.
			if !strings.Contains(payload, tc.want) {
				t.Errorf("sanitizer did not replace framing bytes with space: got %q, want substring %q", payload, tc.want)
			}
		})
	}
}

// TestTextEncoder_Append_StripsFramingBytesFromKeysAndGroups is the V110
// regression: attribute KEYS and GROUP names were appended verbatim, so a raw
// CR/LF/NUL in either field forged a second RFC5424/file-tail frame even though
// the Message and string values were already scrubbed. It FAILS before the fix
// (the framing byte leaks into the payload) and PASSES after.
func TestTextEncoder_Append_StripsFramingBytesFromKeysAndGroups(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		groups []string
		key    string
	}{
		{
			name: "LF in attribute key cannot forge a frame",
			//: a naive append of this key sprays a newline straight into the frame.
			groups: nil,
			key:    "user\nadmin",
		},
		{
			name:   "CR in attribute key cannot forge a frame",
			groups: nil,
			key:    "user\radmin",
		},
		{
			name:   "NUL in attribute key cannot forge a frame",
			groups: nil,
			key:    "user\x00admin",
		},
		{
			name:   "LF in group name cannot forge a frame",
			groups: []string{"http\ninjected"},
			key:    "method",
		},
		{
			name:   "CR + NUL across group and key are both scrubbed",
			groups: []string{"svc\rauth"},
			key:    "id\x00leak",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			enc := encoder.NewText(clock.System)
			rec := corelogger.RecordEvent{
				Level:   level.Info,
				Message: "m",
				Attrs:   []corelogger.AttrValue{{Key: tc.key, Value: corelogger.StringValue("v")}},
			}
			line := string(enc.Append(nil, tc.groups, rec))
			//: the sole legitimate newline is the record terminator; strip it so a
			//: framing byte that leaked from the key or group name is caught here.
			payload := strings.TrimSuffix(line, "\n")
			if strings.ContainsAny(payload, "\n\r\x00") {
				t.Errorf("V110: framing byte leaked from key/group into frame: %q", payload)
			}
		})
	}
}
