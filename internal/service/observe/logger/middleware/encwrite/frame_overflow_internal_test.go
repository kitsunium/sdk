//go:build !race

package encwrite

import (
	"math"
	"testing"
	"unsafe"

	coreencwrite "github.com/kitsunium/sdk/internal/core/observe/logger/middleware/encwrite"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// wordIsWide reports that an int can hold a value larger than MaxUint32, which
// is the only shape of machine where the guard below can ever fire: where int
// is 32 bits, len never reaches MaxUint32 and the branch is unreachable by
// construction rather than untested.
const wordIsWide bool = math.MaxInt > math.MaxUint32

// Test_frame_overflow drives the box length > MaxUint32 guard in stampLength.
// A real 4 GiB allocation is infeasible, so it synthesises an oversized slice
// header over a single live byte; stampLength reads only the length on the
// failure path, never the contents, so no out-of-bounds access occurs. The file is gated to the
// non-race build because the race detector's checkptr rejects an unsafe.Slice
// whose length straddles the backing allocation.
func Test_frame_overflow(t *testing.T) {
	t.Parallel()
	//: table of oversized box lengths that must trip the len>MaxUint32 guard.
	tests := []struct {
		name string
		n    uint64
	}{
		{"MaxUint32 plus one", math.MaxUint32 + 1},
	}
	//: on a 32-bit platform the slice header below cannot be built at all —
	//: "unsafe.Slice: len out of range", observed under GOARCH=386 — and there
	//: is nothing to drive: len(box) cannot exceed MaxUint32 there.
	if !wordIsWide {
		t.Skip("len cannot exceed MaxUint32 where int is 32 bits")
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: synthesise an oversized slice header over a single live byte;
			//: stampLength only reads the length on the failure path, never
			//: the contents, so no out-of-bounds access occurs.
			ptr := unsafe.Pointer(new([1]byte))
			framed := unsafe.Slice((*byte)(ptr), tc.n+uint64(lenPrefixBytes))
			err := stampLength(framed)
			//: a box longer than a uint32 can index must surface the framing sentinel.
			if !errs.HasCode(err, coreencwrite.CodeFramingFailed) {
				t.Fatalf("stampLength(box of %d) err=%v want CodeFramingFailed", tc.n, err)
			}
		})
	}
}
