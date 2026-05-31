package encoder_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/logger/encoder"
)

func TestNewJSON(t *testing.T) {
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
			enc := encoder.NewJSON(tc.clk)
			if enc == nil {
				t.Fatal("NewJSON returned nil")
			}
			if enc.Name() != "json" {
				t.Errorf("Name = %q, want json", enc.Name())
			}
		})
	}
}

func TestJSONEncoder_Append(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		groups     []string
		rec        corelogger.RecordEvent
		wantFields map[string]any
	}{
		{
			name:       "header round-trips through encoding/json",
			groups:     nil,
			rec:        corelogger.RecordEvent{Time: fixed, Level: level.Info, Message: "m"},
			wantFields: map[string]any{"level": "INFO", "msg": "m", "ts": "2026-04-20T12:00:00.000Z"},
		},
		{
			name:   "grouped attr flattens to dotted key",
			groups: []string{"http"},
			rec: corelogger.RecordEvent{Time: fixed, Level: level.Warn, Message: "audit", Attrs: []corelogger.AttrValue{
				{Key: "method", Value: corelogger.StringValue("GET")},
			}},
			wantFields: map[string]any{"level": "WARN", "msg": "audit", "http.method": "GET"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			enc := encoder.NewJSON(clock.System)
			line := string(enc.Append(nil, tc.groups, tc.rec))
			if !strings.HasSuffix(line, "\n") {
				t.Errorf("line missing trailing newline: %q", line)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSuffix(line, "\n")), &got); err != nil {
				t.Fatalf("output is not valid JSON: %v (%q)", err, line)
			}
			for k, want := range tc.wantFields {
				if got[k] != want {
					t.Errorf("field %q = %v, want %v", k, got[k], want)
				}
			}
		})
	}
}

// TestJSONEncoder_Append_EscapesFramingBytes asserts CR, LF, and NUL in the
// Message are JSON-escaped so a line-framed downstream sink cannot be spoofed
// by attacker-influenced content.
func TestJSONEncoder_Append_EscapesFramingBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		message string
	}{
		{"newline", "before\nafter"},
		{"carriage return", "before\rafter"},
		{"nul", "before\x00after"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			enc := encoder.NewJSON(clock.System)
			rec := corelogger.RecordEvent{Level: level.Info, Message: tc.message}
			line := string(enc.Append(nil, nil, rec))
			payload := strings.TrimSuffix(line, "\n")
			//: the encoded object line must never carry a raw framing byte.
			if strings.ContainsAny(payload, "\n\r\x00") {
				t.Errorf("encoder leaked a framing byte: %q", payload)
			}
			//: it must still parse back to the original message verbatim.
			var got map[string]any
			if err := json.Unmarshal([]byte(payload), &got); err != nil {
				t.Fatalf("output is not valid JSON: %v (%q)", err, payload)
			}
			if got["msg"] != tc.message {
				t.Errorf("msg = %v, want %q", got["msg"], tc.message)
			}
		})
	}
}
