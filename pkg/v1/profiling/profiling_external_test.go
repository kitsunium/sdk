package profiling_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/profiling"
)

// TestTheFacadeReadsTheProcess walks the public surface the way a framework's
// dev tools use it, with nothing internal imported: the heap captured and
// folded by stack, the goroutines grouped, and a refusal matched by code.
func TestTheFacadeReadsTheProcess(t *testing.T) {
	t.Parallel()
	p, err := profiling.CaptureHeap()
	if err != nil {
		t.Fatal(err)
	}
	f, err := profiling.Fold(p, profiling.FoldConfig{Attribute: func(s profiling.Sample) string {
		for _, frame := range s.Stack {
			if strings.HasPrefix(frame.Function, "runtime.") {
				return "runtime"
			}
		}
		return ""
	}})
	if err != nil || f.SampleType.Type != "inuse_space" || f.Total <= 0 {
		t.Fatalf("Fold(heap) = %+v, %v", f, err)
	}
	sum := f.Unattributed
	for _, o := range f.Owners {
		sum += o.Value
	}
	if sum != f.Total {
		t.Errorf("owners and unattributed add up to %d, total %d", sum, f.Total)
	}
	gs, err := profiling.Goroutines()
	if err != nil || len(gs) == 0 {
		t.Fatalf("Goroutines() = %d, %v", len(gs), err)
	}
	groups := profiling.GroupGoroutines(gs, profiling.GroupConfig{MaxStack: 4})
	total := 0
	for _, g := range groups {
		total += g.Count
	}
	if total != len(gs) {
		t.Errorf("groups count %d goroutines of %d", total, len(gs))
	}
	if _, err := profiling.Parse([]byte("not a profile")); !errs.HasCode(err, profiling.CodeProfileMalformed) {
		t.Errorf("Parse(garbage) = %v", err)
	}
	if got := profiling.CanonicalName("(*example.com/a.T).M"); got != "example.com/a.(*T).M" {
		t.Errorf("CanonicalName() = %q", got)
	}
}
