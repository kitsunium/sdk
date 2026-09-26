// Package profiling — hosts GroupGoroutines: goroutines counted by what they
// work for, what they wait on and where.
package profiling

import (
	"cmp"
	"slices"
	"strings"
)

// GroupConfig says how GroupGoroutines groups: by which labels, and how many
// groups and frames to keep.
type GroupConfig struct {
	// Labels are the label keys a group is keyed by, in order — "kit_node",
	// "kit_loop" — beside the state and the top frame. None groups by state
	// and top frame alone.
	Labels []string
	// MaxGroups keeps the largest groups only; not positive keeps them all.
	MaxGroups int
	// MaxStack cuts each group's stack to its innermost frames; not
	// positive keeps it whole.
	MaxStack int
}

// GoroutineGroupValue is goroutines sharing their labels, their state and
// their top frame.
type GoroutineGroupValue struct {
	// Labels holds the grouping labels the goroutines carry, by key; a key
	// they do not carry is absent.
	Labels map[string]string
	// State is the goroutines' state.
	State string
	// Top is the innermost frame that is not the runtime's own machinery —
	// the code that asked to wait — or the innermost frame when every frame
	// is the runtime's.
	Top string
	// Stack is one member's stack, innermost first.
	Stack []FrameValue
	// Count is how many goroutines share the group.
	Count int
}

// GroupGoroutines groups gs by the configured labels, the state and the top
// frame, and returns the groups largest first — ties by labels in the
// configured order, then state, then top frame. Nothing in gs is mutated.
func GroupGoroutines(gs []GoroutineValue, cfg GroupConfig) []GoroutineGroupValue {
	index := make(map[string]int, len(gs))
	var groups []GoroutineGroupValue
	//: each goroutine joins the group of its key.
	for i := range gs {
		g := &gs[i]
		top := topFrame(g.Stack)
		key := groupKey(g, cfg.Labels, top)
		//: a group already started.
		if at, seen := index[key]; seen {
			groups[at].Count++
			//: counted.
			continue
		}
		index[key] = len(groups)
		groups = append(groups, GoroutineGroupValue{
			Labels: pick(g.Labels, cfg.Labels), State: g.State, Top: top, Count: 1, Stack: cut(g.Stack, cfg.MaxStack),
		})
	}
	slices.SortFunc(groups, func(x, y GoroutineGroupValue) int {
		//: largest first, then a total order over the key.
		return cmp.Or(cmp.Compare(y.Count, x.Count), compareLabels(x.Labels, y.Labels, cfg.Labels),
			cmp.Compare(x.State, y.State), cmp.Compare(x.Top, y.Top))
	})
	//: the largest groups only, when asked.
	if cfg.MaxGroups > 0 && len(groups) > cfg.MaxGroups {
		groups = groups[:cfg.MaxGroups]
	}
	//: grouped.
	return groups
}

// groupKey is a goroutine's group identity: its grouping labels, its state and
// its top frame, joined with a separator none of them contains.
func groupKey(g *GoroutineValue, keys []string, top string) string {
	var b strings.Builder
	//: each grouping label's value, present or not.
	for _, k := range keys {
		v, has := g.Labels[k]
		//: an absent label and an empty one are different groups.
		if has {
			b.WriteString("=")
		}
		b.WriteString(v)
		b.WriteByte(0)
	}
	b.WriteString(g.State)
	b.WriteByte(0)
	b.WriteString(top)
	//: the identity.
	return b.String()
}

// pick returns the grouping labels a goroutine carries.
func pick(labels map[string]string, keys []string) map[string]string {
	out := make(map[string]string, len(keys))
	//: only the keys the caller groups by.
	for _, k := range keys {
		//: carried.
		if v, has := labels[k]; has {
			out[k] = v
		}
	}
	//: a fresh map the group owns.
	return out
}

// compareLabels orders two groups' labels key by key, in the configured
// order; an absent label sorts first.
func compareLabels(x, y map[string]string, keys []string) int {
	//: the first key that differs decides.
	for _, k := range keys {
		xv, xHas := x[k]
		yv, yHas := y[k]
		//: presence first, then the value.
		if c := cmp.Or(cmpBool(xHas, yHas), cmp.Compare(xv, yv)); c != 0 {
			//: decided.
			return c
		}
	}
	//: equal on every grouping label.
	return 0
}

// cmpBool orders false before true.
func cmpBool(x, y bool) int {
	//: equal, or false first.
	switch {
	//: equal.
	case x == y:
		//: undecided.
		return 0
	//: x absent, y present.
	case !x:
		//: x first.
		return -1
	}
	//: x present, y absent.
	return 1
}

// cut returns stack's innermost n frames, copied; the whole stack when n is
// not positive.
func cut(stack []FrameValue, n int) []FrameValue {
	//: no bound, or a stack within it.
	if n <= 0 || len(stack) <= n {
		//: a copy the group owns.
		return slices.Clone(stack)
	}
	//: the innermost frames.
	return slices.Clone(stack[:n])
}

// topFrame returns the innermost frame that is not the runtime's machinery,
// else the innermost frame.
func topFrame(stack []FrameValue) string {
	//: innermost first.
	for _, f := range stack {
		//: the code that asked to wait.
		if !runtimeFrame(f.Function) {
			//: found.
			return f.Function
		}
	}
	//: every frame is the runtime's.
	if len(stack) > 0 {
		//: the innermost, for want of anything better.
		return stack[0].Function
	}
	//: no stack at all.
	return ""
}

// runtimeFrame reports whether a function is the runtime's own machinery
// rather than code that asked to wait: the runtime and its packages, the
// standard library's internal packages, and the runtime_ hooks that sync and
// internal/poll link to.
func runtimeFrame(fn string) bool {
	//: the prefixes and the linkname hooks.
	return strings.HasPrefix(fn, "runtime.") || strings.HasPrefix(fn, "runtime/") ||
		strings.HasPrefix(fn, "internal/") || strings.Contains(fn, ".runtime_")
}
