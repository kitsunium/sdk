// Package kit — the graph: the product as a diagram of what it mounts.
package kit

import (
	"slices"
	"time"

	"github.com/kitsunium/sdk/framework/model"
)

// Graph returns the product as a diagram: every node the app mounts with the
// position of its declaration, the edges that exist by construction, the
// edges observed at runtime with their counters, and — in dev, once the
// static analysis has run — the edges found in the handlers' code with their
// call sites. It is what the Studio draws and what `<app> graph` prints.
func (a *App) Graph() *model.Graph {
	a.mu.Lock()
	static, analysis := a.static, a.analysis
	started := a.startedAt
	a.mu.Unlock()
	graph := &model.Graph{App: a.appInfo(started), Catalog: Catalog(), Modules: a.describeModules()}
	if analysis != nil {
		graph.Analysis = new(*analysis)
	}
	graph.Nodes = append(graph.Nodes, model.Node{
		ID: model.ExternalID, Kind: model.KindExternal, Name: "Clients",
		Doc:  "Everything outside the product that sends it requests.",
		Docs: map[string]string{"fr": "Tout ce qui, hors du produit, lui envoie des requêtes."},
	})
	var edges []model.Edge
	for _, svc := range a.services {
		if svc != nil {
			edges = append(edges, a.describeService(graph, svc)...)
		}
	}
	edges = append(edges, a.describeBinary(graph)...)
	graph.Edges = a.withObserved(edges)
	listStates(graph, a.services)
	graph.Diagnostics = slices.Concat(a.declarationProblems(), a.problemsCopy(), a.passwordWarnings(graph, static))
	if a.running() {
		graph.Runtime = a.runtimeOf(started)
	}
	graph.Normalize()
	graph.Architecture = a.architecture(graph)
	if static != nil {
		return model.Merge(graph, static)
	}
	return graph
}

// describeService adds the service and its nodes to graph, and returns the
// edges they declare.
func (a *App) describeService(graph *model.Graph, svc *Service) []model.Edge {
	doc, docs := model.SplitDoc(svc.doc)
	graph.Nodes = append(graph.Nodes, model.Node{
		ID: svc.name, Kind: model.KindService, Name: svc.name, Module: svc.moduleName(), Doc: doc, Docs: docs, Source: a.source(&svc.decl),
	})
	var edges []model.Edge
	nodes, _ := svc.snapshot()
	for _, n := range nodes {
		b := n.base()
		mn := model.Node{
			ID: b.id, Kind: b.kind, Name: b.name, Service: svc.name, Module: svc.moduleName(),
			Source: a.source(&b.decl), Handler: a.source(b.body),
		}
		edges = append(edges, n.describe(a, &mn)...)
		if a.hub != nil {
			mn.Stats = a.hub.nodeStats(b.id)
		}
		graph.Nodes = append(graph.Nodes, mn)
	}
	return edges
}

// withObserved names the declared edges and adds what the hub observed: the
// traffic of a declared edge, and an edge only a call drew.
func (a *App) withObserved(edges []model.Edge) []model.Edge {
	byID := map[string]int{}
	for i := range edges {
		e := &edges[i]
		e.ID = model.EdgeID(e.From, e.Kind, e.To, e.Label)
		byID[e.ID] = i
	}
	if a.hub == nil {
		return edges
	}
	for _, o := range a.hub.observedEdges() {
		if i, ok := byID[o.ID]; ok {
			edges[i].Observed = o.Observed
			continue
		}
		byID[o.ID] = len(edges)
		edges = append(edges, o)
	}
	return edges
}

// runtimeOf is what the running app says of itself, since started.
func (a *App) runtimeOf(started time.Time) *model.Runtime {
	rt := &model.Runtime{
		Phase:      a.currentPhase(),
		StartedAt:  started,
		Components: a.componentsCopy(),
		Loops:      a.sortedLoops(),
	}
	a.describeRuntime(rt)
	rt.Dev = a.devBuild
	return rt
}

// listStates gives every schema of a workflow's state type the workflow's
// states as its enum. Reflection cannot see Go constants; the workflow
// declares them, so the API view can offer them and the Data view can tell a
// state from a string.
func listStates(g *model.Graph, services []*Service) {
	enums := stateEnums(services)
	if len(enums) == 0 {
		return
	}
	for i := range g.Nodes {
		for _, s := range schemasOf(&g.Nodes[i]) {
			listStatesIn(s, enums)
		}
	}
}

// stateEnums are the values of every workflow's state type, by its name.
func stateEnums(services []*Service) map[string][]string {
	enums := map[string][]string{}
	for _, svc := range services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			w, ok := n.(interface{ stateType() (string, []string) })
			if !ok {
				continue
			}
			if name, values := w.stateType(); name != "" && len(values) > 0 {
				enums[name] = values
			}
		}
	}
	return enums
}

// schemasOf are the schemas a node carries: an endpoint's request and
// response, a store's entity, a topic's message.
func schemasOf(n *model.Node) []*model.Schema {
	var out []*model.Schema
	if n.Endpoint != nil {
		out = append(out, n.Endpoint.Request, n.Endpoint.Response)
	}
	if n.Store != nil {
		out = append(out, n.Store.Entity)
	}
	if n.Topic != nil {
		out = append(out, n.Topic.Message)
	}
	return out
}

// listStatesIn lists, on every schema of a state type in s, its values.
func listStatesIn(s *model.Schema, enums map[string][]string) {
	if s == nil {
		return
	}
	if values, ok := enums[s.Name]; ok && len(s.Enum) == 0 {
		s.Enum = values
	}
	for i := range s.Fields {
		listStatesIn(s.Fields[i].Type, enums)
	}
	listStatesIn(s.Items, enums)
	listStatesIn(s.Values, enums)
}

// appInfo identifies the product. The module root is only disclosed in dev:
// it is what an editor link needs, and what a production graph must not leak.
func (a *App) appInfo(started time.Time) model.App {
	info := model.App{Name: a.name, Module: a.module, Env: a.cfg.env, Kit: kitVersion(), Go: goVersion(), Build: a.build}
	if a.cfg.env == EnvDev {
		info.Root = a.root
	}
	if !started.IsZero() {
		info.StartedAt = &started
	}
	return info
}

// graphNoticeHook runs at the start of every graph notice. Tests widen the
// notice with it, to prove that no notice outlives the run that armed it.
var graphNoticeHook func() = func() {}

// graphChanged tells the Studio, at most every quarter second, that the
// structure of the graph changed: a static analysis finished, an edge was
// seen for the first time.
//
// The notice runs on a timer's goroutine, so it must not outlive the run
// that armed it: a timer from a stopped run does nothing, and one that got
// past that check is counted in a.wg, which Stop waits for before a restart
// may replace the hub it publishes on.
func (a *App) graphChanged() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.graphDirty != nil || a.stopping {
		return
	}
	var t *time.Timer
	t = time.AfterFunc(250*time.Millisecond, func() {
		a.mu.Lock()
		if a.graphDirty != t || a.stopping {
			a.mu.Unlock()
			return
		}
		a.graphDirty = nil
		hub := a.hub
		a.wg.Add(1)
		a.mu.Unlock()
		defer a.wg.Done()
		graphNoticeHook()
		rev := a.Graph().Revision
		a.mu.Lock()
		changed := rev != a.revision
		a.revision = rev
		a.mu.Unlock()
		if changed {
			hub.publish(model.Event{Type: model.EventGraph, Revision: rev})
		}
	})
	a.graphDirty = t
}
