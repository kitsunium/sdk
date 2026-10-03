package cbor_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// errWrite is the failure failingWriter returns.
var errWrite = errors.New("write failed")

// failingWriter refuses every write.
type failingWriter struct{}

// Write fails.
func (failingWriter) Write([]byte) (int, error) { return 0, errWrite }

// TestEncoder_stream pins the stream encoder: one item per Encode, nothing
// written for a value that cannot be encoded, and a writer's failure kept as
// the cause.
func TestEncoder_stream(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		values  []any
		want    string
		wantErr bool
	}
	tests := []tc{
		{"items back to back", []any{1, "a", []int{2}}, "0161618102", false},
		{"a refused value writes nothing", []any{1, make(chan int), 2}, "01", true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var buf bytes.Buffer
		enc := streaming(t).NewEncoder(&buf)
		var failed bool
		for _, v := range tc.values {
			if err := enc.Encode(v); err != nil {
				failed = true
				break
			}
		}
		if failed != tc.wantErr {
			t.Fatalf("%s: failed = %v, want %v", tc.name, failed, tc.wantErr)
		}
		if got := mustHex(t, tc.want); !bytes.Equal(buf.Bytes(), got) {
			t.Errorf("%s: wrote %x, want %x", tc.name, buf.Bytes(), got)
		}
		if err := enc.Close(); err != nil {
			t.Errorf("%s: Close = %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestEncoder_writerFailure keeps the writer's error as the cause.
func TestEncoder_writerFailure(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"failing writer"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := streaming(t).NewEncoder(failingWriter{}).Encode(1)
		if !errs.HasReason(err, "MARSHAL_FAILED") || !errors.Is(err, errWrite) {
			t.Errorf("%s: err = %v, want MARSHAL_FAILED caused by the writer", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
