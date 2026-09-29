package core_test

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	model "github.com/kitsunium/sdk/framework/model/internal/core"
)

func TestIDs(t *testing.T) {
	if got := model.NodeID("todos", model.KindService, "todos"); got != "todos" {
		t.Errorf("service ID = %q", got)
	}
	if got := model.NodeID("todos", model.KindStore, "todos"); got != "todos/store/todos" {
		t.Errorf("store ID = %q", got)
	}
	if got := model.EdgeID("a", model.EdgeTransitions, "b", "complete"); got != "a|transitions|b|complete" {
		t.Errorf("edge ID = %q", got)
	}
	if got := model.EdgeID("a", model.EdgeReads, "b", ""); got != "a|reads|b" {
		t.Errorf("edge ID without label = %q", got)
	}
	if got := model.ServiceOf("todos/endpoint/GET /todos"); got != "todos" {
		t.Errorf("ServiceOf = %q", got)
	}
}

func sample() *model.GraphMessage {
	return &model.GraphMessage{
		App: model.AppMessage{Name: "todo"},
		Nodes: []model.NodeEntity{
			{ID: "todos/store/todos", Kind: model.KindStore, Name: "todos", Service: "todos", Source: &model.SourceMessage{File: "todos/todos.go", Line: 37}},
			{ID: "todos", Kind: model.KindService, Name: "todos"},
			{
				ID: "todos/endpoint/List", Kind: model.KindEndpoint, Name: "List", Service: "todos",
				Source: &model.SourceMessage{File: "todos/api.go", Line: 15}, Stats: &model.StatsMessage{Count: 3, LastAt: new(time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC))},
			},
		},
		Edges: []model.EdgeMessage{
			{
				From: "todos/endpoint/List", To: "todos/store/todos", Kind: model.EdgeReads,
				Static:   []model.SourceMessage{{File: "todos/api.go", Line: 40}, {File: "todos/api.go", Line: 40}, {File: "todos/api.go", Line: 12}},
				Observed: &model.StatsMessage{Count: 3},
			},
		},
	}
}

func TestNormalizeIsCanonical(t *testing.T) {
	a, b := sample(), sample()
	slices.Reverse(b.Nodes)
	a.Normalize()
	b.Normalize()
	ja, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	jb, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(ja) != string(jb) {
		t.Fatalf("two orders of the same graph normalize differently:\n%s\n%s", ja, jb)
	}
	if a.Nodes[0].ID != "todos" {
		t.Errorf("nodes are not sorted by ID: first is %q", a.Nodes[0].ID)
	}
	e := a.Edges[0]
	if e.ID != "todos/endpoint/List|reads|todos/store/todos" {
		t.Errorf("edge ID not filled: %q", e.ID)
	}
	if len(e.Static) != 2 || e.Static[0].Line != 12 {
		t.Errorf("call sites not sorted and deduplicated: %+v", e.Static)
	}
	if a.Version != model.Version || a.Revision == "" {
		t.Errorf("version %d revision %q", a.Version, a.Revision)
	}
}

// Layout is cached on the revision: counters ticking must not change it,
// and a structural change must.
// A product with no edge serves an empty list, never null: the Studio
// refuses a graph whose edges are null.
func TestAGraphWithoutEdgesServesAnEmptyList(t *testing.T) {
	g := model.GraphMessage{Nodes: []model.NodeEntity{{ID: "todos", Kind: model.KindService, Service: "todos", Name: "todos"}}}
	before := g
	before.Normalize()
	g.Normalize()
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"edges":[]`) {
		t.Fatalf("edges not an empty list: %s", raw)
	}
	withNil := model.GraphMessage{Nodes: before.Nodes}
	withNil.Normalize()
	if withNil.Revision != g.Revision {
		t.Fatalf("revision depends on nil versus empty edges: %s != %s", withNil.Revision, g.Revision)
	}
}

func TestRevisionIgnoresObservations(t *testing.T) {
	a := sample()
	a.Normalize()
	b := sample()
	b.Nodes[2].Stats.Count = 999
	b.Edges[0].Observed.Count = 999
	b.Nodes[0].Store = &model.StoreSpec{Backend: "file", Count: new(5)}
	a.Nodes = sample().Nodes
	a.Nodes[0].Store = &model.StoreSpec{Backend: "file"}
	a.Normalize()
	b.Normalize()
	if a.Revision != b.Revision {
		t.Errorf("observed statistics changed the revision: %s vs %s", a.Revision, b.Revision)
	}
	c := sample()
	c.Edges = append(c.Edges, model.EdgeMessage{From: "todos/endpoint/List", To: "todos/store/todos", Kind: model.EdgeWrites})
	c.Normalize()
	if c.Revision == b.Revision {
		t.Error("a new edge did not change the revision")
	}
}

func TestMerge(t *testing.T) {
	runtime := sample()
	runtime.Normalize()
	static := &model.GraphMessage{
		Nodes: []model.NodeEntity{
			{
				ID: "todos/endpoint/List", Doc: "List returns the todos.",
				Source:  &model.SourceMessage{File: "todos/api.go", Line: 15, EndLine: 15},
				Handler: &model.SourceMessage{File: "todos/api.go", Line: 39, EndLine: 52},
			},
			{ID: "todos/endpoint/Hidden", Kind: model.KindEndpoint},
			{ID: "todos/workflow/lifecycle", Kind: model.KindWorkflow},
		},
		Edges: []model.EdgeMessage{
			{From: "todos/endpoint/List", To: "todos/store/todos", Kind: model.EdgeReads, Static: []model.SourceMessage{{File: "todos/api.go", Line: 41}}},
			{From: "todos/endpoint/List", To: "todos/store/todos", Kind: model.EdgeWrites, Static: []model.SourceMessage{{File: "todos/api.go", Line: 44}}},
			{From: "todos/endpoint/Hidden", To: "todos/store/todos", Kind: model.EdgeReads},
		},
	}
	static.Normalize()
	g := model.Merge(runtime, static)
	if g.Node("todos/endpoint/Hidden") != nil {
		t.Error("a node the runtime does not serve survived the merge")
	}
	list := g.Node("todos/endpoint/List")
	if list.Doc != "List returns the todos." || list.Handler == nil || list.Handler.EndLine != 52 {
		t.Errorf("static knowledge not merged into the node: %+v", list)
	}
	if list.Stats == nil || list.Stats.Count != 3 {
		t.Error("the runtime's statistics were lost")
	}
	reads := g.Edge("todos/endpoint/List|reads|todos/store/todos")
	if reads == nil || len(reads.Static) != 3 || reads.Observed == nil {
		t.Fatalf("the shared edge must carry both evidences: %+v", reads)
	}
	if g.Edge("todos/endpoint/List|writes|todos/store/todos") == nil {
		t.Error("an edge only the static analysis found is missing")
	}
	if len(g.Edges) != 2 {
		t.Errorf("edges = %d, want 2", len(g.Edges))
	}
	if g.Revision == runtime.Revision {
		t.Error("merging new edges did not change the revision")
	}
}

// A base edge without its ID yet is still the edge the extra graph knows:
// the merge enriches it rather than appending a twin.
func TestMergeEnrichesABaseEdgeWithoutID(t *testing.T) {
	base := &model.GraphMessage{
		Nodes: []model.NodeEntity{{ID: "a/endpoint/A", Kind: model.KindEndpoint}, {ID: "a/store/s", Kind: model.KindStore}},
		Edges: []model.EdgeMessage{{From: "a/endpoint/A", To: "a/store/s", Kind: model.EdgeReads, Declared: true}},
	}
	extra := &model.GraphMessage{
		Nodes: []model.NodeEntity{{ID: "a/endpoint/A"}, {ID: "a/store/s"}},
		Edges: []model.EdgeMessage{{From: "a/endpoint/A", To: "a/store/s", Kind: model.EdgeReads, Static: []model.SourceMessage{{File: "a/a.go", Line: 7}}}},
	}
	g := model.Merge(base, extra)
	if len(g.Edges) != 1 {
		t.Fatalf("edges %+v, want one", g.Edges)
	}
	if e := g.Edges[0]; !e.Declared || len(e.Static) != 1 {
		t.Errorf("the edge was not enriched: %+v", e)
	}
}

func TestMergeLinksTransitionCallers(t *testing.T) {
	base := &model.GraphMessage{
		Nodes: []model.NodeEntity{
			{ID: "t/workflow/w", Kind: model.KindWorkflow, Workflow: &model.WorkflowSpec{
				Transitions: []model.TransitionSpec{
					{Event: "complete", From: "open", To: "done", Trigger: model.TriggerEvent},
					{Event: "expire", From: "open", To: "gone", Trigger: model.TriggerTimer},
				},
			}},
			{ID: "t/endpoint/Complete", Kind: model.KindEndpoint},
			{ID: "t/endpoint/Bulk", Kind: model.KindEndpoint},
		},
	}
	extra := &model.GraphMessage{Edges: []model.EdgeMessage{
		{From: "t/endpoint/Complete", To: "t/workflow/w", Kind: model.EdgeTransitions, Label: "complete"},
		{From: "t/endpoint/Bulk", To: "t/workflow/w", Kind: model.EdgeTransitions, Label: "complete"},
	}}
	g := model.Merge(base, extra)
	tr := g.Node("t/workflow/w").Workflow.Transitions
	if want := []string{"t/endpoint/Bulk", "t/endpoint/Complete"}; !slices.Equal(tr[0].Callers, want) {
		t.Errorf("callers = %v, want %v", tr[0].Callers, want)
	}
	if len(tr[1].Callers) != 0 {
		t.Errorf("a timer transition has no callers, got %v", tr[1].Callers)
	}
}

func TestFilesListsEveryReferencedFile(t *testing.T) {
	g := sample()
	g.Nodes = append(g.Nodes, model.NodeEntity{ID: "t/workflow/w", Kind: model.KindWorkflow, Workflow: &model.WorkflowSpec{
		States:      []model.StateSpec{{Name: "done", OnEnter: []model.SourceMessage{{File: "hooks.go", Line: 3}}}},
		Transitions: []model.TransitionSpec{{Guard: &model.SourceMessage{File: "guards.go", Line: 9}}},
	}})
	g.Nodes = append(g.Nodes,
		model.NodeEntity{ID: "t/command/c", Kind: model.KindCommand, Command: &model.CommandSpec{Authorize: &model.SourceMessage{File: "owns.go", Line: 4}}},
		model.NodeEntity{ID: "t/query/q", Kind: model.KindQuery, Query: &model.QuerySpec{Authorize: &model.SourceMessage{File: "member.go", Line: 8}}},
	)
	g.Diagnostics = []model.DiagnosticMessage{{Source: &model.SourceMessage{File: "bad.go", Line: 1}}}
	var want []model.FileMessage
	for _, f := range []string{"bad.go", "guards.go", "hooks.go", "member.go", "owns.go", "todos/api.go", "todos/todos.go"} {
		want = append(want, model.FileMessage{File: f})
	}
	if got := g.Files(); !slices.Equal(got, want) {
		t.Errorf("Files() = %v, want %v", got, want)
	}
}

func TestMermaid(t *testing.T) {
	g := sample()
	g.Nodes = append(g.Nodes, model.NodeEntity{ID: model.ExternalID, Kind: model.KindExternal, Name: "Clients"})
	g.Nodes[2].Endpoint = &model.EndpointSpec{Method: "GET", Path: "/todos"}
	g.Normalize()
	out := model.Mermaid(g)
	for _, want := range []string{"flowchart LR", `subgraph`, `"GET /todos"`, `[("todos")]`, "-->|reads|"} {
		if !strings.Contains(out, want) {
			t.Errorf("mermaid output lacks %q:\n%s", want, out)
		}
	}
}

func TestMermaidDrawsTheV2Kinds(t *testing.T) {
	g := sample()
	g.Nodes = append(g.Nodes,
		model.NodeEntity{ID: "todos/auth/session", Kind: model.KindAuth, Name: "session", Service: "todos"},
		model.NodeEntity{ID: "todos/mailer/mail", Kind: model.KindMailer, Name: "mail", Service: "todos"},
		model.NodeEntity{ID: "todos/loop/reminders", Kind: model.KindLoop, Name: "reminders", Service: "todos", Loop: &model.LoopSpec{Style: model.LoopDeclared}},
		model.NodeEntity{ID: "todos/loop/reaper", Kind: model.KindLoop, Name: "reaper", Service: "todos", Loop: &model.LoopSpec{Style: model.LoopGoroutine}},
		model.NodeEntity{ID: "todos/topic/events", Kind: model.KindTopic, Name: "events", Service: "todos"},
		model.NodeEntity{
			ID: "todos/endpoint/Me", Kind: model.KindEndpoint, Name: "Me", Service: "todos",
			Endpoint: &model.EndpointSpec{Method: "GET", Path: "/me", Auth: model.AuthRequired},
		},
	)
	g.Edges = append(g.Edges,
		model.EdgeMessage{From: "todos/loop/reminders", To: "todos/mailer/mail", Kind: model.EdgeSends, Static: []model.SourceMessage{{File: "todos/loop.go", Line: 9}}},
		model.EdgeMessage{From: "todos/topic/events", To: "todos/loop/reminders", Kind: model.EdgeWakes, Declared: true},
	)
	g.Normalize()
	out := model.Mermaid(g)
	for _, want := range []string{`[/"session"\]`, `[["mail"]]`, `(("reminders"))`, `((("reaper")))`, `"GET /me · auth"`, "-.->|sends|", "-->|wakes|"} {
		if !strings.Contains(out, want) {
			t.Errorf("mermaid output lacks %q:\n%s", want, out)
		}
	}
}

// A port is a socket; its implementation, which has no route, is drawn by
// its name; the binding is a declared arrow from the port.
func TestMermaidDrawsAPortAndItsBinding(t *testing.T) {
	g := &model.GraphMessage{
		Nodes: []model.NodeEntity{
			{ID: "desk", Kind: model.KindService, Name: "desk"},
			{ID: "posts", Kind: model.KindService, Name: "posts"},
			{
				ID: "desk/port/enforcer", Kind: model.KindPort, Name: "enforcer", Service: "desk",
				Port: &model.PortSpec{Bound: "posts/endpoint/Enforce", Via: model.ViaImplement},
			},
			{
				ID: "posts/endpoint/Enforce", Kind: model.KindEndpoint, Name: "Enforce", Service: "posts",
				Endpoint: &model.EndpointSpec{Expose: model.ExposePrivate, Implements: "desk/port/enforcer", Auth: model.AuthRequired},
			},
		},
		Edges: []model.EdgeMessage{{From: "desk/port/enforcer", To: "posts/endpoint/Enforce", Kind: model.EdgeCalls, Declared: true}},
	}
	g.Normalize()
	out := model.Mermaid(g)
	for _, want := range []string{`[\"enforcer"/]`, `["Enforce · auth"]`, "-->|calls|"} {
		if !strings.Contains(out, want) {
			t.Errorf("mermaid output lacks %q:\n%s", want, out)
		}
	}
	// A binding to the port's own fallback is dotted, declared as it is.
	g.Node("desk/port/enforcer").Port.Via = model.ViaFallback
	if out := model.Mermaid(g); !strings.Contains(out, "-.->|calls|") {
		t.Errorf("a fallback binding is drawn solid:\n%s", out)
	}
}

// What a port calls is the runtime's to say: the analysis reads the same
// rule over the whole module, and may see an implementation the app does not
// mount, or a binding another app gives. The runtime's binding stays, the
// analysis's other one does not join it; a call to the port found in the
// code still does.
func TestMergeKeepsTheRuntimesBinding(t *testing.T) {
	base := &model.GraphMessage{
		Nodes: []model.NodeEntity{
			{ID: "desk/port/enforcer", Kind: model.KindPort, Port: &model.PortSpec{Bound: "desk/endpoint/Queue", Via: model.ViaFallback, Fallback: "desk/endpoint/Queue"}},
			{ID: "desk/endpoint/Queue", Kind: model.KindEndpoint},
			{ID: "desk/endpoint/Decide", Kind: model.KindEndpoint},
			{ID: "posts/endpoint/Enforce", Kind: model.KindEndpoint},
		},
		Edges: []model.EdgeMessage{{From: "desk/port/enforcer", To: "desk/endpoint/Queue", Kind: model.EdgeCalls, Declared: true}},
	}
	extra := &model.GraphMessage{Edges: []model.EdgeMessage{
		{From: "desk/port/enforcer", To: "posts/endpoint/Enforce", Kind: model.EdgeCalls, Declared: true},
		{From: "desk/port/enforcer", To: "desk/endpoint/Queue", Kind: model.EdgeCalls, Declared: true},
		{From: "desk/endpoint/Decide", To: "desk/port/enforcer", Kind: model.EdgeCalls, Static: []model.SourceMessage{{File: "desk/decide.go", Line: 12}}},
	}}
	g := model.Merge(base, extra)
	if g.Edge("desk/port/enforcer|calls|posts/endpoint/Enforce") != nil {
		t.Error("the analysis's binding joined the one the runtime chose")
	}
	if e := g.Edge("desk/port/enforcer|calls|desk/endpoint/Queue"); e == nil || !e.Declared {
		t.Errorf("the runtime's binding: %+v", e)
	}
	if e := g.Edge("desk/endpoint/Decide|calls|desk/port/enforcer"); e == nil || len(e.Static) != 1 {
		t.Errorf("a call to the port found in the code: %+v", e)
	}
}

func TestSplitDoc(t *testing.T) {
	for _, c := range []struct {
		name, in, def string
		docs          map[string]string
	}{
		{"one language", "Lifecycle is the life of a task.\nIt ends archived.\n", "Lifecycle is the life of a task. It ends archived.", nil},
		{
			"a French paragraph", "Lifecycle is the life\nof a task.\n\nfr: Lifecycle est la vie\nd’une tâche.\n",
			"Lifecycle is the life of a task.",
			map[string]string{"fr": "Lifecycle est la vie d’une tâche."},
		},
		{"a service string", "Accounts and sessions.\n\nfr: Comptes et sessions.", "Accounts and sessions.", map[string]string{"fr": "Comptes et sessions."}},
		{"tags only", "en: The list.\nfr: La liste.", "The list.", map[string]string{"fr": "La liste."}},
		{"a colon mid-sentence is text", "Read the fr: prefix, or id: the key.", "Read the fr: prefix, or id: the key.", nil},
		{"an unsupported tag is text", "id: the key of the entity.", "id: the key of the entity.", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			def, docs := model.SplitDoc(c.in)
			if def != c.def || !maps.Equal(docs, c.docs) {
				t.Errorf("SplitDoc(%q) = %q, %v; want %q, %v", c.in, def, docs, c.def, c.docs)
			}
		})
	}
}
