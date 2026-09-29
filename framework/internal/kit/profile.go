// Package kit — CPU profiles of the running process, for the Studio.
package kit

import (
	"cmp"
	"errors"
	"math"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/profiling"
)

// Profiles: the process's CPU, heap and goroutines, folded onto the graph.
// The SDK's profiling package captures, decodes, folds and groups them; kit
// says whose work each sample was. A CPU sample is charged to the node whose
// work its goroutine was doing — the kit_node label begin puts on it — and a
// heap sample, which carries no label, to the node whose code is on its
// stack. Dev only: the routes live behind the Studio's guard.

// CodeProfileRead reports a profile of the process that could not be written
// or read back. It is in the framework's 0.4.2.* range.
const CodeProfileRead errs.Code = 0x00_04_02_30 // 0.4.2.48

// Bounds of the profiles.
const (
	// flameMinShare prunes a flame frame costing less than this share of
	// the total.
	flameMinShare float64 = 0.005
	// flameMaxDepth stops a flame graph this deep.
	flameMaxDepth int = 64
	topFuncs      int = 25
	topNodeFuncs  int = 8
)

// Goroutines ----------------------------------------------------------------

// Bounds of the goroutine view.
const (
	maxGoroutineGroups int = 300
	maxGroupStack      int = 32
)

// funcOwners maps the functions of nodes' code to their nodes: the function
// a node runs (its entry), and every function its code reaches.
type funcOwners struct {
	entries map[string][]string
	code    map[string][]string
	sources map[string]*model.Source
}

// serveHeapProfile answers GET /_kit/api/profile/heap: the live heap, after
// a collection, folded onto the graph.
func (a *App) serveHeapProfile(w http.ResponseWriter, r *http.Request) {
	p, err := profiling.CaptureHeap()
	if err != nil {
		a.replyError(r.Context(), w, failure(CodeProfileRead, "PROFILE_UNREADABLE", "the heap profile could not be read", err))
		return
	}
	out, err := a.foldProfile(p, model.ProfileHeap, ownersOf(a.Graph()))
	if err != nil {
		a.replyError(r.Context(), w, err)
		return
	}
	out.At = a.clock.Now().UTC()
	writeJSON(w, http.StatusOK, out)
}

// foldProfile charges every sample of p to a node — by its kit_node label
// for a CPU profile, by the innermost node code on its stack for the heap —
// and answers the fold as the Studio reads it: milliseconds of CPU, bytes of
// heap.
func (a *App) foldProfile(p *profiling.Profile, kind string, owners *funcOwners) (*model.Profile, error) {
	cfg := profiling.FoldConfig{
		SampleType: "cpu", TopFunctions: topFuncs, TopPerOwner: topNodeFuncs, FlameMinShare: flameMinShare, FlameMaxDepth: flameMaxDepth,
		Attribute: func(s profiling.Sample) string {
			if l := s.Labels[labelNode]; len(l) > 0 {
				return l[0]
			}
			return ""
		},
	}
	unit, round := "ms", func(v int64) float64 { return round2(float64(v) / float64(time.Millisecond)) }
	if kind == model.ProfileHeap {
		cfg.SampleType, cfg.Attribute = "inuse_space", func(s profiling.Sample) string { return owners.attribute(s.Stack) }
		unit, round = "bytes", func(v int64) float64 { return float64(v) }
	}
	f, err := profiling.Fold(p, cfg)
	if errors.Is(err, profiling.SampleTypeMissing) {
		return nil, failure(CodeProfileRead, "PROFILE_UNREADABLE", "the profile has no "+cfg.SampleType+" samples", nil)
	}
	if err != nil {
		return nil, failure(CodeProfileRead, "PROFILE_UNREADABLE", "the profile could not be read", err)
	}
	out := &model.Profile{Kind: kind, Unit: unit, Total: round(f.Total), Unattributed: round(f.Unattributed), Nodes: []model.NodeCost{}}
	for _, o := range f.Owners {
		nc := model.NodeCost{Node: o.Owner, Value: round(o.Value), Top: a.funcCosts(o.Top, owners, o.Owner, round)}
		if f.Total > 0 {
			nc.Share = math.Round(float64(o.Value)/float64(f.Total)*10000) / 10000
		}
		out.Nodes = append(out.Nodes, nc)
	}
	slices.SortFunc(out.Nodes, func(x, y model.NodeCost) int {
		return cmp.Or(cmp.Compare(y.Value, x.Value), cmp.Compare(x.Node, y.Node))
	})
	out.Top = a.funcCosts(f.Top, owners, "", round)
	if f.Flame != nil {
		out.Flame = flameOf(f.Flame, owners, round)
	}
	return out, nil
}

// funcCosts are the costliest functions, each with the node it belongs to —
// node, or the one whose code it is — and where it is, when the graph or the
// product's module knows.
func (a *App) funcCosts(top []profiling.FunctionCost, owners *funcOwners, node string, round func(int64) float64) []model.FuncCost {
	out := make([]model.FuncCost, 0, len(top))
	for _, fn := range top {
		fc := model.FuncCost{Func: fn.Function, Flat: round(fn.Flat), Cum: round(fn.Cum), Node: node}
		if fc.Node == "" {
			fc.Node, _ = owners.owner(fn.Function)
		}
		fc.Source = owners.source(fn.Function)
		if fc.Source == nil {
			fc.Source = a.moduleSource(pos{file: fn.File, line: fn.StartLine, fn: fn.Function})
		}
		out = append(out, fc)
	}
	return out
}

// flameOf is a flame frame as the Studio reads it, each frame with the node
// whose code it is.
func flameOf(n *profiling.FlameNode, owners *funcOwners, round func(int64) float64) *model.FlameNode {
	out := &model.FlameNode{Name: n.Name, Value: round(n.Value)}
	if n.Name != profiling.FlameRoot {
		out.Node, _ = owners.owner(n.Name)
	}
	for _, c := range n.Children {
		out.Children = append(out.Children, flameOf(c, owners, round))
	}
	return out
}

// ownersOf reads the owners of functions off the graph: handlers' ranges
// and, once the static analysis has run, every node's code.
func ownersOf(g *model.Graph) *funcOwners {
	o := &funcOwners{entries: map[string][]string{}, code: map[string][]string{}, sources: map[string]*model.Source{}}
	for i := range g.Nodes {
		o.addNode(&g.Nodes[i])
	}
	return o
}

// addNode records the functions node n owns: its handler and entry, whose
// samples are its own, and the code it runs.
func (o *funcOwners) addNode(n *model.Node) {
	if n.Handler != nil && n.Handler.Func != "" {
		ownFunc(o.entries, n.Handler.Func, n.ID)
		o.sources[profiling.CanonicalName(n.Handler.Func)] = n.Handler
	}
	if n.Code == nil {
		return
	}
	if n.Code.Entry != "" {
		ownFunc(o.entries, n.Code.Entry, n.ID)
	}
	for _, fn := range n.Code.Funcs {
		ownFunc(o.code, fn.Func, n.ID)
		if fn.Source != nil {
			o.sources[profiling.CanonicalName(fn.Func)] = fn.Source
		}
	}
}

// ownFunc records node as an owner of fn, under its name and its canonical
// one.
func ownFunc(m map[string][]string, fn, node string) {
	for _, k := range []string{fn, profiling.CanonicalName(fn)} {
		if k != "" && !slices.Contains(m[k], node) {
			m[k] = append(m[k], node)
		}
	}
}

// owner returns the one node whose code fn is, or "" when there is none or
// several: a helper shared by two nodes says nothing about which one ran.
// A node's entry function wins over a function its code merely reaches.
func (o *funcOwners) owner(fn string) (string, bool) {
	for _, k := range []string{fn, profiling.CanonicalName(fn)} {
		if ns := o.entries[k]; len(ns) > 0 {
			if len(ns) == 1 {
				return ns[0], true
			}
			return "", false
		}
		if ns := o.code[k]; len(ns) == 1 {
			return ns[0], true
		}
	}
	return "", false
}

// attribute returns the node of the innermost frame that belongs to exactly
// one node.
func (o *funcOwners) attribute(stack []profiling.Frame) string {
	for _, f := range stack {
		if node, ok := o.owner(f.Function); ok {
			return node
		}
	}
	return ""
}

// source returns where fn is, when the graph knows it.
func (o *funcOwners) source(fn string) *model.Source {
	if s, ok := o.sources[profiling.CanonicalName(fn)]; ok {
		cp := *s
		cp.Func = fn
		return &cp
	}
	return nil
}

// moduleSource locates a function of the product's own module, relative to
// its root; any other file — the standard library, a dependency — has no
// source here, so no path outside the module is ever disclosed.
func (a *App) moduleSource(p pos) *model.Source {
	if p.file == "" {
		return nil
	}
	file := ""
	switch {
	case a.root != "" && filepath.IsAbs(p.file):
		if rel, err := filepath.Rel(a.root, p.file); err == nil && filepath.IsLocal(rel) {
			file = filepath.ToSlash(rel)
		}
	case a.module != "" && strings.HasPrefix(p.file, a.module+"/"):
		file = strings.TrimPrefix(p.file, a.module+"/")
	}
	if file == "" {
		return nil
	}
	return &model.Source{File: file, Line: p.line, Func: p.fn}
}

// serveGoroutines answers GET /_kit/api/goroutines: every goroutine, grouped
// by the node it works for, the loop it belongs to, what it waits on, and
// its innermost frame that is not the runtime's.
func (a *App) serveGoroutines(w http.ResponseWriter, r *http.Request) {
	gs, err := profiling.Goroutines()
	if err != nil {
		a.replyError(r.Context(), w, failure(CodeProfileRead, "PROFILE_UNREADABLE", "the goroutines could not be listed", err))
		return
	}
	writeJSON(w, http.StatusOK, goroutinesOf(gs, a.clock.Now().UTC()))
}

// goroutinesOf groups goroutines by node, loop, state and top frame, largest
// group first.
func goroutinesOf(gs []profiling.Goroutine, at time.Time) *model.Goroutines {
	groups := profiling.GroupGoroutines(gs, profiling.GroupConfig{
		Labels: []string{labelNode, labelLoop}, MaxGroups: maxGoroutineGroups, MaxStack: maxGroupStack,
	})
	out := &model.Goroutines{At: at, Total: len(gs), Groups: make([]model.GoroutineGroup, 0, len(groups))}
	for _, g := range groups {
		out.Groups = append(out.Groups, model.GoroutineGroup{
			Node: g.Labels[labelNode], Loop: g.Labels[labelLoop], State: g.State, Top: g.Top,
			Stack: functionsOf(g.Stack), Count: g.Count,
		})
	}
	return out
}

// functionsOf names a stack's frames, innermost first.
func functionsOf(stack []profiling.Frame) []string {
	out := make([]string, len(stack))
	for i, f := range stack {
		out[i] = f.Function
	}
	return out
}
