package studiokit

import (
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/observe/profiling"
)

const traceback = `goroutine 1 [running]:
main.main()
	/src/main.go:10 +0x1d

goroutine 7 [chan receive, 3 minutes] {kit_loop: http, kit_node: "shop/endpoint/Get-it"}:
github.com/x/shop.(*Store[...]).wait(0x1400012c000, {0x100e0a1d8, 0x140001b4000})
	/src/shop/store.go:40 +0x5e8
github.com/x/shop.Get(...)
	/src/shop/api.go:12
created by net/http.(*Server).Serve in goroutine 5
	/go/src/net/http/server.go:3454 +0x485

goroutine 9 gp=0x1400 m=nil [sync.Mutex.Lock (durable), locked to thread] {"weird key": "a\"b", k: v}:
sync.(*Mutex).Lock(0x1)
	/go/src/sync/mutex.go:10 +0x1
...additional frames elided...

goroutine 11 [chan receive (nil chan)]:
main.f()
	/src/main.go:30 +0x1
`

// The SDK reads the dump; kit groups it by node and loop.
func TestTracebackParsing(t *testing.T) {
	gs := profiling.ParseGoroutines([]byte(traceback))
	if len(gs) != 4 {
		t.Fatalf("%d goroutines: %+v", len(gs), gs)
	}
	if gs[0].State != "running" || len(gs[0].Labels) != 0 || !slices.Equal(functionsOf(gs[0].Stack), []string{"main.main"}) {
		t.Errorf("goroutine 1: %+v", gs[0])
	}
	g := gs[1]
	if g.State != "chan receive" || g.Labels[plug.LabelLoop] != "http" || g.Labels[plug.LabelNode] != "shop/endpoint/Get-it" {
		t.Errorf("goroutine 7: %+v", g)
	}
	if !slices.Equal(functionsOf(g.Stack), []string{"github.com/x/shop.(*Store[...]).wait", "github.com/x/shop.Get"}) {
		t.Errorf("goroutine 7's stack: %q", functionsOf(g.Stack))
	}
	if gs[2].State != "sync.Mutex.Lock" || gs[2].Labels["weird key"] != `a"b` || gs[2].Labels["k"] != "v" {
		t.Errorf("goroutine 9: %+v", gs[2])
	}
	if gs[3].State != "chan receive (nil chan)" {
		t.Errorf("goroutine 11: %+v", gs[3])
	}
	grouped := goroutinesOf(append(gs, gs[1]), time.Unix(0, 0))
	if grouped.Total != 5 || grouped.Groups[0].Count != 2 || grouped.Groups[0].Top != "github.com/x/shop.(*Store[...]).wait" {
		t.Errorf("groups %+v", grouped)
	}
	for _, grp := range grouped.Groups {
		if grp.State == "sync.Mutex.Lock" && grp.Top != "sync.(*Mutex).Lock" {
			t.Errorf("the top frame skips the runtime only: %+v", grp)
		}
	}
}

// kit meets a profile's frames and the graph's code through the SDK's
// spelling of a function.
func TestCanonicalFunc(t *testing.T) {
	for in, want := range map[string]string{
		"github.com/x/shop.Create":                 "github.com/x/shop.Create",
		"github.com/x/shop.Create.func1":           "github.com/x/shop.Create",
		"github.com/x/shop.Create.func1.2":         "github.com/x/shop.Create",
		"github.com/x/shop.(*Store).visible":       "github.com/x/shop.(*Store).visible",
		"(*github.com/x/shop.Store).visible":       "github.com/x/shop.(*Store).visible",
		"(github.com/x/shop.Money).String":         "github.com/x/shop.Money.String",
		"github.com/x/shop.(*Store[...]).Get":      "github.com/x/shop.(*Store).Get",
		"github.com/x/shop.Map[...]":               "github.com/x/shop.Map",
		"github.com/x/shop.(*Handler).Serve-fm":    "github.com/x/shop.(*Handler).Serve",
		"github.com/x/shop.run.gowrap1":            "github.com/x/shop.run",
		"github.com/x/kit_test.glob..func3":        "github.com/x/kit_test.glob.",
		"github.com/x/shop.(*Store[go.shape.int])": "github.com/x/shop.(*Store)",
	} {
		runCase(t, in, want)
	}
}

// runCase checks one name's canonical spelling.
func runCase(t *testing.T, in, want string) {
	t.Helper()
	if got := profiling.CanonicalName(in); got != want {
		t.Errorf("CanonicalName(%q) = %q, want %q", in, got, want)
	}
}

// twoEndpoints is a graph of two endpoints that share a helper: A's code
// reaches a store's method too.
func twoEndpoints() *model.Graph {
	return &model.Graph{Nodes: []model.Node{
		{
			ID: "a/endpoint/A", Handler: &model.Source{File: "a.go", Line: 1, Func: "github.com/x/a.A"},
			Code: &model.CodeInfo{Entry: "github.com/x/a.A", Funcs: []model.CodeFunc{
				{Func: "github.com/x/a.A"}, {Func: "(*github.com/x/a.Store).visible", Source: &model.Source{File: "a/store.go", Line: 9}}, {Func: "github.com/x/a.shared"},
			}},
		},
		{
			ID: "a/endpoint/B", Handler: &model.Source{File: "a.go", Line: 20, Func: "github.com/x/a.B"},
			Code: &model.CodeInfo{Entry: "github.com/x/a.B", Funcs: []model.CodeFunc{{Func: "github.com/x/a.B"}, {Func: "github.com/x/a.shared"}}},
		},
	}}
}

// A function belongs to a node when exactly one node's code holds it; an
// entry beats a function merely reached; the innermost owned frame wins.
func TestFunctionsAreAttributedToTheirOneNode(t *testing.T) {
	o := ownersOf(twoEndpoints())
	runOwner := func(fn, want string) {
		t.Helper()
		if got, _ := o.owner(fn); got != want {
			t.Errorf("owner(%q) = %q, want %q", fn, got, want)
		}
	}
	for fn, want := range map[string]string{
		"github.com/x/a.A":                   "a/endpoint/A",
		"github.com/x/a.A.func2":             "a/endpoint/A",
		"github.com/x/a.(*Store).visible":    "a/endpoint/A",
		"github.com/x/a.shared":              "",
		"github.com/x/a.B":                   "a/endpoint/B",
		"encoding/json.Marshal":              "",
		"github.com/x/a.(*Store[...]).other": "",
	} {
		runOwner(fn, want)
	}
	if got := o.attribute(framesOf("runtime.mallocgc", "github.com/x/a.shared", "github.com/x/a.B", "net/http.(*conn).serve")); got != "a/endpoint/B" {
		t.Errorf("a shared helper under B is B's: %q", got)
	}
	if src := o.source("github.com/x/a.(*Store).visible"); src == nil || src.File != "a/store.go" {
		t.Errorf("source %+v", src)
	}
}

// labelled is a CPU sample of value on stack, labelled with node's work
// unless node is empty.
func labelled(node string, value int64, stack ...string) profiling.Sample {
	s := profiling.Sample{Stack: framesOf(stack...), Values: []int64{value}}
	if node != "" {
		s.Labels = map[string][]string{plug.LabelNode: {node}}
	}
	return s
}

// A fold charges each sample to its node, keeps the rest unattributed, and
// prunes the flame's frames under half a percent: 30 ms of A's, 10 ms of B's,
// 60.1 ms of nobody's.
func TestAFoldChargesEachSampleToItsNode(t *testing.T) {
	ms := int64(time.Millisecond)
	cpu := &profiling.Profile{SampleTypes: []profiling.SampleType{{Type: "cpu", Unit: "nanoseconds"}}, Samples: []profiling.Sample{
		labelled("a/endpoint/A", 30*ms, "runtime.memmove", "github.com/x/a.A"),
		labelled("a/endpoint/B", 10*ms, "github.com/x/a.B"),
		labelled("", 60*ms, "runtime.gcBgMarkWorker"),
		labelled("", ms/10, "github.com/x/a.tiny"),
	}}
	pp, ppErr := foldProfile(cpu, model.ProfileCPU, ownersOf(twoEndpoints()), nil)
	if ppErr != nil {
		t.Fatal(ppErr)
	}
	p := *pp
	if p.Unit != "ms" || p.Total != 100.1 || p.Unattributed != 60.1 || len(p.Nodes) != 2 || p.Nodes[0].Node != "a/endpoint/A" || p.Nodes[0].Share != 0.2997 {
		t.Fatalf("profile %+v", p)
	}
	if p.Top[0].Func != "runtime.gcBgMarkWorker" || p.Top[0].Flat != 60 || p.Nodes[0].Top[0].Func != "runtime.memmove" {
		t.Errorf("top %+v / %+v", p.Top, p.Nodes[0].Top)
	}
	for _, c := range p.Flame.Children {
		if c.Name == "github.com/x/a.tiny" {
			t.Error("a frame under 0.5% survived the pruning")
		}
		if c.Name == "github.com/x/a.A" && (c.Node != "a/endpoint/A" || c.Value != 30 || c.Children[0].Name != "runtime.memmove") {
			t.Errorf("A's frame %+v", c)
		}
	}
}

// framesOf is a stack of the named functions, innermost first.
func framesOf(fns ...string) []profiling.Frame {
	out := make([]profiling.Frame, len(fns))
	for i, fn := range fns {
		out[i] = profiling.Frame{Function: fn}
	}
	return out
}
