// Package profiling_test — Fold over profiles built by hand, where every total
// is known exactly.
package profiling_test

import (
	"math"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/profiling"
)

// stack builds a stack from function names, innermost first.
func stack(names ...string) []profiling.FrameValue {
	out := make([]profiling.FrameValue, len(names))
	for i, n := range names {
		out[i] = profiling.FrameValue{Function: n, File: "/src/" + n + ".go", StartLine: 10 + i}
	}
	return out
}

// sample builds a CPU sample: a count and nanoseconds, with a node label.
func sample(node string, ns int64, names ...string) profiling.SampleValue {
	s := profiling.SampleValue{Stack: stack(names...), Values: []int64{1, ns}}
	if node != "" {
		s.Labels = map[string][]string{"node": {node}}
	}
	return s
}

// cpuProfile is a CPU profile with the samples given.
func cpuProfile(samples ...profiling.SampleValue) *profiling.ProfileValue {
	return &profiling.ProfileValue{
		SampleTypes: []profiling.SampleTypeValue{{Type: "samples", Unit: "count"}, {Type: "cpu", Unit: "nanoseconds"}},
		Samples:     samples,
	}
}

// byNode charges a sample to its node label.
func byNode(s profiling.SampleValue) string {
	if v := s.Labels["node"]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// TestFoldAddsUpExactly pins the arithmetic: owners and the unattributed part
// add up to the total, flat is the innermost frame, cum counts a recursive
// function once, and the rankings order by flat, then cum, then name.
func TestFoldAddsUpExactly(t *testing.T) {
	t.Parallel()
	p := cpuProfile(
		sample("shop", 50, "shop.burn", "shop.handle", "main.main"),
		sample("shop", 30, "shop.recurse", "shop.recurse", "shop.handle", "main.main"),
		sample("audit", 15, "audit.tally", "main.main"),
		sample("", 5, "runtime.gcBgMarkWorker"),
		sample("shop", 0, "shop.never"),
	)
	f, err := profiling.Fold(p, profiling.FoldConfig{Attribute: byNode})
	if err != nil {
		t.Fatal(err)
	}
	if f.SampleType.Type != "cpu" || f.Total != 100 || f.Unattributed != 5 {
		t.Fatalf("totals: %+v", f)
	}
	if len(f.Owners) != 2 || f.Owners[0].Owner != "shop" || f.Owners[0].Value != 80 || f.Owners[1].Value != 15 {
		t.Fatalf("owners: %+v", f.Owners)
	}
	shopTop := f.Owners[0].Top
	if shopTop[0].Function != "shop.burn" || shopTop[0].Flat != 50 || shopTop[1].Function != "shop.recurse" || shopTop[1].Cum != 30 {
		t.Errorf("shop's top: %+v", shopTop)
	}
	if i := slices.IndexFunc(f.Top, func(c profiling.FunctionCostValue) bool { return c.Function == "main.main" }); i < 0 || f.Top[i].Cum != 95 || f.Top[i].Flat != 0 {
		t.Errorf("main.main in the top: %+v", f.Top)
	}
	if f.Top[0].File != "/src/shop.burn.go" || f.Top[0].StartLine != 10 {
		t.Errorf("the top function's source: %+v", f.Top[0])
	}
	if slices.ContainsFunc(f.Top, func(c profiling.FunctionCostValue) bool { return c.Function == "shop.never" }) {
		t.Error("a zero sample was folded")
	}
}

// TestTheFlameIsRootedPrunedAndBounded pins the flame graph: the root stands
// for the total, children go outermost first and costliest first, a frame
// under the share is pruned, and the depth is bounded.
func TestTheFlameIsRootedPrunedAndBounded(t *testing.T) {
	t.Parallel()
	deep := make([]string, 0, 10)
	for i := range 10 {
		deep = append(deep, string(rune('a'+i)))
	}
	p := cpuProfile(
		sample("", 900, "main.hot", "main.main"),
		sample("", 99, "main.warm", "main.main"),
		sample("", 1, "main.cold", "main.main"),
		sample("", 100, deep...),
	)
	f, err := profiling.Fold(p, profiling.FoldConfig{FlameMinShare: 0.01, FlameMaxDepth: 3})
	if err != nil {
		t.Fatal(err)
	}
	root := f.Flame
	if root.Name != profiling.FlameRoot || root.Value != 1100 || len(root.Children) != 2 || root.Children[0].Name != "main.main" {
		t.Fatalf("root: %+v", root)
	}
	mainKids := root.Children[0].Children
	if len(mainKids) != 2 || mainKids[0].Name != "main.hot" || mainKids[1].Name != "main.warm" {
		t.Errorf("main.main's children: %+v; want hot then warm, cold pruned", mainKids)
	}
	depth, at := 0, root.Children[1]
	for at != nil {
		depth++
		if len(at.Children) == 0 {
			break
		}
		at = at.Children[0]
	}
	if depth != 3 || root.Children[1].Name != "j" {
		t.Errorf("the deep stack is %d frames deep from %q; want 3 from the outermost", depth, root.Children[1].Name)
	}
}

// TestFoldPicksItsSampleType pins the default — the profile's own, else the
// last — an explicit choice, and the refusal of a type the profile lacks.
func TestFoldPicksItsSampleType(t *testing.T) {
	t.Parallel()
	p := cpuProfile(sample("", 7, "main.x"))
	if f, err := profiling.Fold(p, profiling.FoldConfig{SampleType: "samples"}); err != nil || f.Total != 1 {
		t.Errorf("samples: %+v, %v", f, err)
	}
	p.DefaultSampleType = "samples"
	if f, err := profiling.Fold(p, profiling.FoldConfig{}); err != nil || f.SampleType.Type != "samples" {
		t.Errorf("the profile's default: %+v, %v", f, err)
	}
	if _, err := profiling.Fold(p, profiling.FoldConfig{SampleType: "inuse_space"}); !errs.HasCode(err, profiling.CodeSampleTypeMissing) {
		t.Errorf("a missing type = %v", err)
	}
}

// TestFoldWithoutAttributionAndWithOddConfig pins that a nil Attribute
// charges everything to nobody, that an unsymbolized frame is left out, and
// that unusable settings fall back to the defaults.
func TestFoldWithoutAttributionAndWithOddConfig(t *testing.T) {
	t.Parallel()
	s := sample("x", 10, "main.f")
	s.Stack = append([]profiling.FrameValue{{Address: 0xdead}}, s.Stack...)
	f, err := profiling.Fold(cpuProfile(s), profiling.FoldConfig{FlameMinShare: math.NaN(), TopFunctions: -1, TopPerOwner: -1, FlameMaxDepth: -1})
	if err != nil {
		t.Fatal(err)
	}
	if f.Unattributed != 10 || len(f.Owners) != 0 || len(f.Top) != 1 || f.Top[0].Function != "main.f" || f.Top[0].Flat != 10 {
		t.Errorf("fold: %+v", f)
	}
	empty, err := profiling.Fold(cpuProfile(), profiling.FoldConfig{})
	if err != nil || empty.Flame != nil || len(empty.Owners) != 0 {
		t.Errorf("an empty profile: %+v, %v", empty, err)
	}
}
