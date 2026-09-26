// Package profiling — hosts Fold: a profile's samples charged to owners, the
// costliest functions, and the flame graph, in the profile's own unit.
package profiling

import (
	"cmp"
	"maps"
	"math"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Fold's defaults, taken when a FoldConfig field is not positive.
const (
	// DefaultTopFunctions is how many functions FoldedValue.Top lists.
	DefaultTopFunctions int = 25
	// DefaultTopPerOwner is how many functions each owner's Top lists.
	DefaultTopPerOwner int = 8
	// DefaultFlameMinShare prunes a flame frame costing less than this share
	// of the total: half a percent.
	DefaultFlameMinShare float64 = 0.005
	// DefaultFlameMaxDepth is how many frames deep the flame graph goes.
	DefaultFlameMaxDepth int = 64
)

// FlameRoot is the name of a flame graph's root frame, which stands for the
// whole profile.
const FlameRoot string = "root"

// FoldConfig says how Fold reads a profile: which sample type, whose each
// sample is, how many functions to rank and how much of the flame to keep.
// Every zero field has a default.
type FoldConfig struct {
	// Attribute returns the owner a sample is charged to — a pprof label's
	// value, the component whose code is on the stack — or "" when the
	// sample belongs to nobody in particular. The attribution is the
	// caller's: Fold only adds up. Nil charges every sample to nobody.
	Attribute func(sample SampleValue) string
	// SampleType names the value folded — "cpu", "inuse_space". Empty is
	// the profile's default sample type, else its last one.
	SampleType string
	// TopFunctions is how many functions FoldedValue.Top lists; not
	// positive means DefaultTopFunctions.
	TopFunctions int
	// TopPerOwner is how many functions each owner's Top lists; not positive
	// means DefaultTopPerOwner.
	TopPerOwner int
	// FlameMinShare prunes a flame frame costing less than this share of
	// the total. Outside (0, 1), or NaN, means DefaultFlameMinShare.
	FlameMinShare float64
	// FlameMaxDepth is how deep the flame graph goes from its root; not
	// positive means DefaultFlameMaxDepth.
	FlameMaxDepth int
}

// FoldedValue is a profile folded: in the sample type's own unit —
// nanoseconds of CPU, bytes of heap — never rounded.
type FoldedValue struct {
	// Flame is the flame graph, rooted at [FlameRoot], its children the
	// outermost frames; nil when Total is zero.
	Flame *FlameNodeValue
	// Owners holds each owner's cost, the costliest first.
	Owners []OwnerCostValue
	// Top holds the costliest functions: by flat cost, then cumulative, then
	// name.
	Top []FunctionCostValue
	// SampleType is what was folded.
	SampleType SampleTypeValue
	// Total is the sum of every sample's value.
	Total int64
	// Unattributed is the part of Total charged to nobody. Total is exactly
	// Unattributed plus every owner's Value.
	Unattributed int64
}

// OwnerCostValue is what one owner cost: the sum of the samples charged to
// it, and its own costliest functions.
type OwnerCostValue struct {
	// Owner is the name Attribute returned.
	Owner string
	// Top holds the owner's costliest functions.
	Top []FunctionCostValue
	// Value is the sum of the samples charged to the owner.
	Value int64
}

// FunctionCostValue is what one function cost, flat and cumulative, and where
// it is defined.
type FunctionCostValue struct {
	// Function is the function's name, as the runtime spells it.
	Function string
	// File and StartLine say where it is defined, when the profile knows.
	File string
	// Flat is the cost of samples whose innermost frame is the function.
	Flat int64
	// Cum is the cost of samples with the function anywhere on the stack,
	// each sample counted once however many times the function recurses.
	Cum int64
	// StartLine is the function's first line; zero when unknown.
	StartLine int
}

// FlameNodeValue is one frame of a flame graph: a function reached along one
// path, and what the samples through it cost.
type FlameNodeValue struct {
	// Name is the function's name; [FlameRoot] for the root.
	Name string
	// Children are the functions it called, costliest first.
	Children []*FlameNodeValue
	// Value is the cost of every sample through this path.
	Value int64
}

// Fold charges every sample of p to an owner and adds it all up: each owner's
// cost and costliest functions, the costliest functions overall, and a flame
// graph pruned of what costs less than the configured share. A frame with no
// function name — an unsymbolized one — is left out of the stacks it is in.
//
// It refuses a sample type the profile does not measure with
// [SampleTypeMissing], and a nil profile or a sample with fewer values than
// the profile has sample types — which a profile Parse returned never has, a
// hand-built one may — with [ProfileMalformed].
func Fold(p *ProfileValue, cfg FoldConfig) (FoldedValue, error) {
	//: nothing to fold.
	if p == nil {
		//: named like any other part that could not be read.
		return FoldedValue{}, malformed("profile")
	}
	cfg = cfg.normal()
	typ := cfg.SampleType
	//: the caller did not say: the profile's own default.
	if typ == "" {
		typ = p.defaultType()
	}
	idx := p.sampleType(typ)
	//: the profile does not measure it.
	if idx < 0 {
		//: the name asked for is a field; it came from the caller.
		return FoldedValue{}, errs.Wrap(SampleTypeMissing, errs.WrapParams{}, errs.String("sample_type", typ))
	}
	f := newFolder(p)
	//: every sample with a cost.
	for i := range p.Samples {
		s := &p.Samples[i]
		//: a sample with no value for the type folded.
		if idx >= len(s.Values) {
			//: nothing partial is returned.
			return FoldedValue{}, malformed("sample values")
		}
		//: a zero value adds nothing to anything.
		if s.Values[idx] == 0 {
			//: next sample.
			continue
		}
		owner := ""
		//: the caller decides whose it is.
		if cfg.Attribute != nil {
			owner = cfg.Attribute(*s)
		}
		f.add(s.Stack, owner, s.Values[idx], cfg.FlameMaxDepth)
	}
	//: the totals, the rankings and the flame.
	return f.finish(p.SampleTypes[idx], cfg), nil
}

// normal returns c with every default applied.
func (c FoldConfig) normal() FoldConfig {
	//: a list of no function says nothing.
	if c.TopFunctions <= 0 {
		c.TopFunctions = DefaultTopFunctions
	}
	//: the same for each owner.
	if c.TopPerOwner <= 0 {
		c.TopPerOwner = DefaultTopPerOwner
	}
	//: NaN compares false everywhere, so it is caught first.
	if math.IsNaN(c.FlameMinShare) || c.FlameMinShare <= 0 || c.FlameMinShare >= 1 {
		c.FlameMinShare = DefaultFlameMinShare
	}
	//: a flame with no depth has no frames.
	if c.FlameMaxDepth <= 0 {
		c.FlameMaxDepth = DefaultFlameMaxDepth
	}
	//: every field usable.
	return c
}

// costs accumulates flat and cumulative cost per function.
type costs struct {
	flat, cum map[string]int64
}

// newCosts returns empty costs.
func newCosts() *costs {
	//: both maps ready.
	return &costs{flat: make(map[string]int64), cum: make(map[string]int64)}
}

// flameFold is a flame frame being accumulated.
type flameFold struct {
	children map[string]*flameFold
	name     string
	value    int64
}

// folder accumulates a profile's samples.
type folder struct {
	all          *costs
	owners       map[string]*costs
	ownerValue   map[string]int64
	sources      map[string]FrameValue
	flame        *flameFold
	seen         map[string]struct{}
	names        []string
	total        int64
	unattributed int64
}

// newFolder returns an empty folder for p.
func newFolder(p *ProfileValue) *folder {
	//: every map ready, the flame rooted.
	return &folder{
		all: newCosts(), owners: make(map[string]*costs), ownerValue: make(map[string]int64),
		sources: make(map[string]FrameValue), seen: make(map[string]struct{}),
		flame: &flameFold{name: FlameRoot, children: make(map[string]*flameFold)},
		names: make([]string, 0, len(p.Samples)),
	}
}

// add takes one sample: its stack, innermost first, the owner it is charged
// to, and its value.
func (f *folder) add(stack []FrameValue, owner string, v int64, depth int) {
	f.total += v
	var mine *costs
	//: charged to nobody, or to an owner.
	if owner == "" {
		f.unattributed += v
	} else {
		mine = f.owners[owner]
		//: the owner's first sample.
		if mine == nil {
			mine = newCosts()
			f.owners[owner] = mine
		}
		f.ownerValue[owner] += v
	}
	names := f.namesOf(stack)
	//: an empty stack costs, and belongs to no function.
	if len(names) > 0 {
		f.all.flat[names[0]] += v
		//: the owner's own flat cost.
		if mine != nil {
			mine.flat[names[0]] += v
		}
	}
	f.cumulate(names, mine, v)
	f.climb(names, v, depth)
}

// namesOf returns the named frames of stack, innermost first, remembering
// where each function is defined.
func (f *folder) namesOf(stack []FrameValue) []string {
	f.names = f.names[:0]
	//: every frame with a name; an unsymbolized one is left out.
	for _, frame := range stack {
		//: no symbol: nothing to charge.
		if frame.Function == "" {
			//: next frame.
			continue
		}
		f.names = append(f.names, frame.Function)
		//: the first frame seen of a function says where it is.
		if _, known := f.sources[frame.Function]; !known {
			f.sources[frame.Function] = frame
		}
	}
	//: reused between samples; the caller does not keep it.
	return f.names
}

// cumulate charges v to each distinct function of names, once per sample.
func (f *folder) cumulate(names []string, mine *costs, v int64) {
	clear(f.seen)
	//: a recursive function is charged once per sample.
	for _, name := range names {
		//: already charged for this sample.
		if _, dup := f.seen[name]; dup {
			//: next frame.
			continue
		}
		f.seen[name] = struct{}{}
		f.all.cum[name] += v
		//: and to the owner.
		if mine != nil {
			mine.cum[name] += v
		}
	}
}

// climb adds v along the flame path of names, from the outermost frame in, no
// deeper than depth frames.
func (f *folder) climb(names []string, v int64, depth int) {
	at := f.flame
	at.value += v
	//: outermost first; the innermost frames go when the stack is too deep.
	for i := len(names) - 1; i >= 0 && len(names)-i <= depth; i-- {
		child := at.children[names[i]]
		//: a path not taken before.
		if child == nil {
			child = &flameFold{name: names[i], children: make(map[string]*flameFold)}
			at.children[names[i]] = child
		}
		child.value += v
		at = child
	}
}

// finish writes the accumulated costs into a FoldedValue.
func (f *folder) finish(typ SampleTypeValue, cfg FoldConfig) FoldedValue {
	out := FoldedValue{SampleType: typ, Total: f.total, Unattributed: f.unattributed}
	//: one entry per owner, with its own top functions.
	for owner, c := range f.owners {
		out.Owners = append(out.Owners, OwnerCostValue{Owner: owner, Value: f.ownerValue[owner], Top: f.top(c, cfg.TopPerOwner)})
	}
	slices.SortFunc(out.Owners, func(x, y OwnerCostValue) int {
		//: costliest first; the name orders equal costs.
		return cmp.Or(cmp.Compare(y.Value, x.Value), cmp.Compare(x.Owner, y.Owner))
	})
	out.Top = f.top(f.all, cfg.TopFunctions)
	//: an empty profile has no flame.
	if f.total > 0 {
		out.Flame = prune(f.flame, float64(f.total)*cfg.FlameMinShare)
	}
	//: folded.
	return out
}

// top returns the n costliest functions of c: by flat cost, then cumulative,
// then name.
func (f *folder) top(c *costs, n int) []FunctionCostValue {
	//: every function charged at all is in cum.
	names := slices.Collect(maps.Keys(c.cum))
	slices.SortFunc(names, func(x, y string) int {
		//: flat first: where the time is spent, not merely passed through.
		return cmp.Or(cmp.Compare(c.flat[y], c.flat[x]), cmp.Compare(c.cum[y], c.cum[x]), cmp.Compare(x, y))
	})
	out := make([]FunctionCostValue, 0, min(n, len(names)))
	//: the first n.
	for _, name := range names[:min(n, len(names))] {
		src := f.sources[name]
		out = append(out, FunctionCostValue{Function: name, Flat: c.flat[name], Cum: c.cum[name], File: src.File, StartLine: src.StartLine})
	}
	//: ranked.
	return out
}

// prune converts a flame frame, dropping every child costing less than
// cutoff, and orders the children costliest first.
func prune(ff *flameFold, cutoff float64) *FlameNodeValue {
	out := &FlameNodeValue{Name: ff.name, Value: ff.value}
	kids := make([]*flameFold, 0, len(ff.children))
	//: the children worth drawing.
	for _, c := range ff.children {
		//: below the share, a frame is noise on a flame graph.
		if float64(c.value) >= cutoff {
			kids = append(kids, c)
		}
	}
	slices.SortFunc(kids, func(x, y *flameFold) int {
		//: costliest first; the name orders equal costs.
		return cmp.Or(cmp.Compare(y.value, x.value), cmp.Compare(x.name, y.name))
	})
	//: each child pruned the same way.
	for _, c := range kids {
		out.Children = append(out.Children, prune(c, cutoff))
	}
	//: the subtree.
	return out
}
