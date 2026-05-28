package logger_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/logger"
)

// TestSinkClass_String verifies the String() representation for every
// declared class value plus the out-of-range fallback path.
func TestSinkClass_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		c    logger.SinkClass
		want string
	}
	tests := []tc{
		{"unknown zero value", logger.UnknownClass, "unknown"},
		{"local", logger.LocalClass, "local"},
		{"async-local", logger.AsyncLocalClass, "async-local"},
		{"remote-batched", logger.RemoteBatchedClass, "remote-batched"},
		{"remote-streaming", logger.RemoteStreamingClass, "remote-streaming"},
		{"out-of-range renders numeric", logger.SinkClass(99), "unknown(99)"},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; SinkClass.String() must hand back the exact documented label.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := tc.c.String()
		//: the diagnostic name must equal the table's expected value.
		if got != tc.want {
			t.Fatalf("SinkClass(%d).String() = %q, want %q", tc.c, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
