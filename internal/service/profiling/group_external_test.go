// Package profiling_test — grouping goroutines, and one name per function.
package profiling_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/service/profiling"
)

// goroutine builds a goroutine with labels, a state and a stack.
func goroutine(labels map[string]string, state string, names ...string) profiling.GoroutineValue {
	return profiling.GoroutineValue{Labels: labels, State: state, Stack: stack(names...)}
}

// TestGoroutinesGroupByLabelsStateAndTopFrame pins the key — a label absent
// and a label empty are different groups; the top frame skips the runtime's
// machinery — the order, and the two bounds.
func TestGoroutinesGroupByLabelsStateAndTopFrame(t *testing.T) {
	t.Parallel()
	web := map[string]string{"node": "web", "loop": "http"}
	gs := []profiling.GoroutineValue{
		goroutine(web, "select", "runtime.gopark", "runtime.selectgo", "app.Wait", "app.serve"),
		goroutine(web, "select", "runtime.gopark", "runtime.selectgo", "app.Wait", "app.serve"),
		goroutine(web, "IO wait", "runtime.gopark", "internal/poll.runtime_pollWait", "net.(*conn).Read"),
		goroutine(map[string]string{"node": ""}, "select", "runtime.gopark", "app.Wait"),
		goroutine(nil, "select", "runtime.gopark", "app.Wait"),
		goroutine(nil, "running", "runtime.goexit"),
	}
	groups := profiling.GroupGoroutines(gs, profiling.GroupConfig{Labels: []string{"node", "loop"}})
	if len(groups) != 5 {
		t.Fatalf("%d groups: %+v", len(groups), groups)
	}
	if g := groups[0]; g.Count != 2 || g.Top != "app.Wait" || g.Labels["node"] != "web" || g.Labels["loop"] != "http" || len(g.Stack) != 4 {
		t.Errorf("the largest group = %+v", g)
	}
	var io, allRuntime *profiling.GoroutineGroupValue
	for i := range groups {
		switch groups[i].State {
		case "IO wait":
			io = &groups[i]
		case "running":
			allRuntime = &groups[i]
		}
	}
	if io == nil || io.Top != "net.(*conn).Read" {
		t.Errorf("the IO group's top skips the poller hook: %+v", io)
	}
	if allRuntime == nil || allRuntime.Top != "runtime.goexit" {
		t.Errorf("a stack all runtime keeps its innermost frame: %+v", allRuntime)
	}
	bounded := profiling.GroupGoroutines(gs, profiling.GroupConfig{Labels: []string{"node"}, MaxGroups: 2, MaxStack: 1})
	if len(bounded) != 2 || len(bounded[0].Stack) != 1 || bounded[0].Count != 2 {
		t.Errorf("bounded = %+v", bounded)
	}
	if gs[0].Stack[0].Function != "runtime.gopark" {
		t.Error("grouping mutated its input")
	}
}

// TestCanonicalNameMeetsEverySpelling pins one name per function, whoever
// spelled it.
func TestCanonicalNameMeetsEverySpelling(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"pkg.(*T).M":                  "pkg.(*T).M",
		"(*pkg.T).M":                  "pkg.(*T).M",
		"(pkg.T).M":                   "pkg.T.M",
		"example.com/a/b.F[...]":      "example.com/a/b.F",
		"example.com/a/b.F[go.shape]": "example.com/a/b.F",
		"pkg.F.func1.2":               "pkg.F",
		"pkg.F.gowrap1":               "pkg.F",
		"pkg.F.deferwrap2":            "pkg.F",
		"pkg.T.M-fm":                  "pkg.T.M",
		"(*example.com/a/b.T[...]).M": "example.com/a/b.(*T).M",
		"(T).M":                       "(T).M",
		"main.main":                   "main.main",
	}
	for in, want := range cases {
		if got := profiling.CanonicalName(in); got != want {
			t.Errorf("CanonicalName(%q) = %q; want %q", in, got, want)
		}
	}
}
