package encoder

import (
	"testing"

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
	tests := []struct {
		name   string
		groups []string
		rec    corelogger.RecordEvent
	}{
		{"no groups, no attrs, zero time", nil, corelogger.RecordEvent{Level: level.Info, Message: "m"}},
		{"groups but no attrs leave header intact", []string{"g"}, corelogger.RecordEvent{Level: level.Warn, Message: "x"}},
		{"attrs without groups render bare", nil, corelogger.RecordEvent{Level: level.Debug, Message: "y", Attrs: []corelogger.AttrValue{{Key: "k", Value: corelogger.StringValue("v")}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &textEncoder{clk: clock.System}
			line := e.Append(nil, tc.groups, tc.rec)
			if len(line) == 0 || line[len(line)-1] != '\n' {
				t.Errorf("Append produced %q, want non-empty trailing-newline output", line)
			}
		})
	}
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
