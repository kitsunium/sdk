//go:build !race

package http1_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// TestTrace_allocatesNothing pins the link's 0-allocation budget: reading the
// field, parsing it and writing the answer's line costs no allocation, on the
// path every traced request takes.
func TestTrace_allocatesNothing(t *testing.T) {
	var req corenet.HTTP1RequestValue
	var res corenet.HTTP1ResponseValue
	layer := http1.Trace()(corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, r *corenet.HTTP1ResponseValue) {
		r.Status = 200
	}))
	in := []byte(withTrace(validTP))
	p := http1.Parser{}
	if _, r := p.Parse(in, &req); r != http1.RefusalNone {
		t.Fatal(r)
	}
	if allocs := testing.AllocsPerRun(1000, func() {
		res = corenet.HTTP1ResponseValue{}
		layer.ServeHTTP1(&req, &res)
	}); allocs != 0 {
		t.Fatalf("the Trace link: %v allocations a request, want 0", allocs)
	}
	if !req.Trace.IsValid() {
		t.Fatal("the link read no trace")
	}
}
