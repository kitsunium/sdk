//go:build !race

package multipart_test

import (
	"io"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/service/data/codec/multipart"
)

// allocSink defeats dead-code elimination in the AllocsPerRun probes. Storing
// a []byte in it boxes the slice header, so the Marshal ceiling carries that
// one allocation on top of the codec's own.
var allocSink any

// TestAllocBudget pins multipart's per-call allocation ceilings — the one
// codec of the sixteen that had none (#907). The encoder writes each part's
// delimiter and header block itself instead of building a textproto.MIMEHeader
// for mime/multipart.Writer.CreatePart, which formatted and sorted it through
// fmt: Marshal of the four-field text form allocated 69 times, and the
// ceiling is three per part. Carries //go:build !race (testing.AllocsPerRun is
// +1 under -race) and no t.Parallel (AllocsPerRun reads a process-global
// counter). Budgets are ceilings — re-pin with intent on a toolchain bump.
func TestAllocBudget(t *testing.T) {
	c := multipart.New()
	form := benchTextForm()
	appender, ok := c.(codec.Appender)
	if !ok {
		t.Fatal("multipart is not an Appender")
	}
	streaming, ok := c.(codec.StreamingCodec)
	if !ok {
		t.Fatal("multipart is not a StreamingCodec")
	}
	dst := make([]byte, 0, 4096)
	tests := []struct {
		name string
		ceil float64
		fn   func()
	}{
		{"marshal-text-form", 3 * float64(len(form.Parts)), func() {
			out, err := c.Marshal(form)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			allocSink = out
		}},
		{"append-text-form", 3 * float64(len(form.Parts)), func() {
			out, err := appender.Append(dst[:0], form)
			if err != nil {
				t.Fatalf("Append: %v", err)
			}
			allocSink = out
		}},
		{"stream-encode-text-form", 3 * float64(len(form.Parts)), func() {
			enc := streaming.NewEncoder(io.Discard)
			for i := range form.Parts {
				if err := enc.Encode(&form.Parts[i]); err != nil {
					t.Fatalf("Encode: %v", err)
				}
			}
			allocSink = enc
		}},
	}
	for _, tc := range tests {
		got := testing.AllocsPerRun(100, tc.fn)
		if got > tc.ceil {
			t.Errorf("%s: %.1f allocs/op, ceiling %.0f", tc.name, got, tc.ceil)
		}
	}
}
