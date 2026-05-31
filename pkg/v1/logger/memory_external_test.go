package logger_test

import (
	"testing"

	logger "github.com/kitsunium/sdk/pkg/v1/logger"
)

// Test_NewMemorySink verifies that the facade hands back an empty, usable
// MemorySink that records written events and re-exposes them via Records.
func Test_NewMemorySink(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message string
	}{
		{name: "records a single message", message: "hello"},
	}

	//: each facade scenario is independent.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := logger.NewMemorySink()
			//: a fresh sink must report no records.
			if got := m.Records(); got != nil {
				t.Fatalf("expected nil records, got %v", got)
			}

			n, err := m.Write(t.Context(), logger.RecordSnapshot{Message: tc.message}, nil)
			//: Write through the facade alias must succeed.
			if err != nil {
				t.Fatalf("Write returned error: %v (n=%d)", err, n)
			}

			got := m.Records()
			//: the written record must be retrievable through the snapshot accessor.
			if len(got) != 1 || got[0].Message != tc.message {
				t.Fatalf("expected one record %q, got %v", tc.message, got)
			}

			m.Reset()
			//: Reset must clear the buffer.
			if got := m.Records(); got != nil {
				t.Fatalf("expected nil records after Reset, got %v", got)
			}
		})
	}
}
