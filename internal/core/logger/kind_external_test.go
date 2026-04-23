package logger_test

import (
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// TestKind_String validates the textual rendering of every Kind variant
// including the out-of-range fallback used for forward compatibility.
func TestKind_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind corelogger.Kind
		want string
	}{
		{"KindAny renders as any", corelogger.KindAny, "any"},
		{"KindBool renders as bool", corelogger.KindBool, "bool"},
		{"KindDuration renders as duration", corelogger.KindDuration, "duration"},
		{"KindFloat64 renders as float64", corelogger.KindFloat64, "float64"},
		{"KindInt64 renders as int64", corelogger.KindInt64, "int64"},
		{"KindString renders as string", corelogger.KindString, "string"},
		{"KindTime renders as time", corelogger.KindTime, "time"},
		{"KindUint64 renders as uint64", corelogger.KindUint64, "uint64"},
		{"KindGroup renders as group", corelogger.KindGroup, "group"},
		{"out-of-range positive falls back to unknown", corelogger.Kind(99), "unknown"},
		{"out-of-range negative falls back to unknown", corelogger.Kind(-1), "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.kind.String()
			if got != tc.want {
				t.Errorf("Kind(%d).String() = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}
