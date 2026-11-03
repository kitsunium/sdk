//go:build !race

package pem_test

import (
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/service/data/codec/pem"
)

// appendAllocSink keeps the probes' results alive.
var appendAllocSink []byte

// TestAppendAllocatesLessThanMarshal is #1124's acceptance: for a 1 KiB value
// and a dst with room, Append allocates strictly less than Marshal — it
// writes into the caller's buffer instead of encoding elsewhere and copying.
// Race off (AllocsPerRun counts the race runtime's own), no t.Parallel.
func TestAppendAllocatesLessThanMarshal(t *testing.T) {
	c := pem.New()
	a, ok := c.(corecodec.Appender)
	if !ok {
		t.Fatal("the pem codec does not implement codec.Appender")
	}
	//: boxed once, so the probes count the codec and not the conversion to any.
	var v any = kibValue()
	dst := make([]byte, 0, 8<<10)
	marshal := testing.AllocsPerRun(200, func() {
		out, err := c.Marshal(v)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		appendAllocSink = out
	})
	appended := testing.AllocsPerRun(200, func() {
		out, err := a.Append(dst[:0], v)
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		appendAllocSink = out
	})
	t.Logf("1 KiB value: Marshal %v allocs/op, Append %v allocs/op", marshal, appended)
	if appended >= marshal || appended > 0 {
		t.Fatalf("Append allocs/op = %v, want fewer than Marshal's %v and at most 0", appended, marshal)
	}
}
