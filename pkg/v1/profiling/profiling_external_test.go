package profiling_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/profiling"
)

// TestTheFacadeReadsTheProcess walks the public surface the way a framework's
// dev tools use it, with nothing internal imported: the heap captured and
// folded by stack, the goroutines grouped, a refusal matched by code, and one
// spelling per function — each case on its own.
func TestTheFacadeReadsTheProcess(t *testing.T) {
	t.Parallel()
	type tc struct {
		check func(t *testing.T)
		name  string
	}
	cases := []tc{
		{name: "the heap folds by stack and adds up", check: func(t *testing.T) {
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
			// A sampled heap may hold no sample at all; the sum must hold anyway.
			if err != nil || f.SampleType.Type != "inuse_space" {
				t.Fatalf("Fold(heap) = %+v, %v", f, err)
			}
			sum := f.Unattributed
			for _, o := range f.Owners {
				sum += o.Value
			}
			if sum != f.Total {
				t.Errorf("owners and unattributed add up to %d, total %d", sum, f.Total)
			}
		}},
		{name: "the goroutines group without losing one", check: func(t *testing.T) {
			gs, err := profiling.Goroutines()
			if err != nil || len(gs) == 0 {
				t.Fatalf("Goroutines() = %d, %v", len(gs), err)
			}
			total := 0
			for _, g := range profiling.GroupGoroutines(gs, profiling.GroupConfig{MaxStack: 4}) {
				total += g.Count
			}
			if total != len(gs) {
				t.Errorf("groups count %d goroutines of %d", total, len(gs))
			}
		}},
		{name: "a refusal matches by code", check: func(t *testing.T) {
			if _, err := profiling.Parse([]byte("not a profile")); !errs.HasCode(err, profiling.CodeProfileMalformed) {
				t.Errorf("Parse(garbage) = %v", err)
			}
		}},
		{name: "a function has one spelling", check: func(t *testing.T) {
			if got := profiling.CanonicalName("(*example.com/a.T).M"); got != "example.com/a.(*T).M" {
				t.Errorf("CanonicalName() = %q", got)
			}
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		c.check(t)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
