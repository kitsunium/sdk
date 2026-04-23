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
			name:        "time renders RFC3339Nano",
			groups:      nil,
			rec:         corelogger.RecordEvent{Level: level.Info, Message: "x", Attrs: []corelogger.AttrValue{{Key: "t", Value: corelogger.TimeValue(time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC))}}},
			wantInLine:  []string{"t=2026-04-20T12:00:00Z"},
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
// Regresses finding #2 from the post-#12 audit.
func TestTextEncoder_Append_StripsFramingSensitiveBytes(t *testing.T) {
	t.Parallel()
	enc := encoder.NewText(clock.System)
	rec := corelogger.RecordEvent{
		Level: level.Info,
		//: this message contains every framing-sensitive byte the sanitizer
		//: must replace; a naive append would spray newlines into the frame.
		Message: "before\nmid\rend\x00tail",
	}
	line := string(enc.Append(nil, nil, rec))
	//: the trailing record-terminator newline is expected; strip it for
	//: the check so a newline from the Message would be caught.
	payload := strings.TrimSuffix(line, "\n")
	if strings.ContainsAny(payload, "\n\r\x00") {
		t.Errorf("sanitizer leaked a framing byte: %q", payload)
	}
	//: each scrubbed byte must become a single space so the Message stays
	//: recoverable in the line ordering; no collapse / no deletion.
	if !strings.Contains(payload, "before mid end tail") {
		t.Errorf("sanitizer did not replace framing bytes with space: %q", payload)
	}
}
