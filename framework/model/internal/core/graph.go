// The graph's identities and its canonical form: node and edge IDs,
// Normalize, Merge and the files a graph names.

package core

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// NodeID builds the ID of a node declared in a service. A service's own ID is
// its name.
func NodeID(service string, kind NodeKind, name string) string {
	if kind == KindService {
		return service
	}
	return service + "/" + string(kind) + "/" + name
}

// EdgeID builds the ID of an edge. The label is part of the identity: two
// transitions of one workflow fired from one endpoint are two edges.
func EdgeID(from string, kind EdgeKind, to, label string) string {
	id := from + "|" + string(kind) + "|" + to
	if label != "" {
		id += "|" + label
	}
	return id
}

// Node returns the node with the given ID, or nil.
func (g *GraphMessage) Node(id string) *NodeEntity {
	for i := range g.Nodes {
		if g.Nodes[i].ID == id {
			return &g.Nodes[i]
		}
	}
	return nil
}

// Edge returns the edge with the given ID, or nil.
func (g *GraphMessage) Edge(id string) *EdgeMessage {
	for i := range g.Edges {
		if g.Edges[i].ID == id {
			return &g.Edges[i]
		}
	}
	return nil
}

// Files returns every file the graph points at, each with its root — the
// product's module, or another Go module: a module's (ADR 0008), a
// library's —, sorted and deduplicated. It is the complete list of files a
// reader of this graph may legitimately ask to see, and the source endpoint
// serves nothing else.
func (g *GraphMessage) Files() []FileMessage {
	seen := map[FileMessage]bool{}
	add := func(s *SourceMessage) {
		if s != nil && s.File != "" {
			seen[FileMessage{GoModule: s.GoModule, File: s.File}] = true
		}
	}
	for i := range g.Nodes {
		nodeSources(&g.Nodes[i], add)
	}
	g.graphSources(add)
	files := slices.Collect(maps.Keys(seen))
	slices.SortFunc(files, compareFiles)
	return files
}

// graphSources hands add every position the graph carries outside its
// nodes: containers, the code behind static edges, diagnostics, modules.
func (g *GraphMessage) graphSources(add func(*SourceMessage)) {
	if g.Architecture != nil {
		for i := range g.Architecture.Containers {
			add(g.Architecture.Containers[i].Source)
		}
	}
	for i := range g.Edges {
		for j := range g.Edges[i].Static {
			add(&g.Edges[i].Static[j])
		}
	}
	for i := range g.Diagnostics {
		add(g.Diagnostics[i].Source)
	}
	for i := range g.Modules {
		add(g.Modules[i].Source)
		add(g.Modules[i].Mount)
	}
}

// nodeSources hands add every position a node carries.
func nodeSources(n *NodeEntity, add func(*SourceMessage)) {
	add(n.Source)
	add(n.Handler)
	if n.Workflow != nil {
		workflowSources(n.Workflow, add)
	}
	if n.Store != nil {
		storeSources(n.Store, add)
	}
	if n.Loop != nil {
		for j := range n.Loop.Wakes {
			add(n.Loop.Wakes[j].Func)
		}
		for j := range n.Loop.Selects {
			add(n.Loop.Selects[j].Source)
		}
	}
	add(n.authorization())
	if n.Code != nil {
		codeSources(n.Code, add)
	}
}

// storeSources hands add every position of a store: its privacy's options
// (ADR 0006) and its password policies (ADR 0007).
func storeSources(st *StoreSpec, add func(*SourceMessage)) {
	if st.Privacy != nil {
		for _, src := range st.Privacy.sources() {
			add(src)
		}
	}
	for _, src := range st.History.sources() {
		add(src)
	}
}

// workflowSources hands add every position of a workflow: its states'
// entry hooks, its transitions and their guards.
func workflowSources(w *WorkflowSpec, add func(*SourceMessage)) {
	for _, st := range w.States {
		for j := range st.OnEnter {
			add(&st.OnEnter[j])
		}
	}
	for _, tr := range w.Transitions {
		add(tr.Guard)
		add(tr.Source)
	}
}

// codeSources hands add every position of a node's code level.
func codeSources(c *CodeResult, add func(*SourceMessage)) {
	for j := range c.Funcs {
		add(c.Funcs[j].Source)
	}
	for j := range c.Uses {
		for k := range c.Uses[j].Sites {
			add(&c.Uses[j].Sites[k])
		}
	}
}

// Normalize sorts nodes, edges and call sites into their canonical order,
// fills each workflow transition's callers from the edges, derives the
// connectors from the nodes, and recomputes Revision. Two graphs describing the same structure normalize to
// the same bytes, whatever order they were built in.
func (g *GraphMessage) Normalize() {
	g.Version = Version
	// A product with no edge still serves a list: the Studio refuses null.
	if g.Edges == nil {
		g.Edges = []EdgeMessage{}
	}
	slices.SortFunc(g.Nodes, func(a, b NodeEntity) int { return cmp.Compare(a.ID, b.ID) })
	for i := range g.Edges {
		e := &g.Edges[i]
		if e.ID == "" {
			e.ID = EdgeID(e.From, e.Kind, e.To, e.Label)
		}
		slices.SortFunc(e.Static, CompareSource)
		e.Static = slices.CompactFunc(e.Static, func(a, b SourceMessage) bool { return CompareSource(a, b) == 0 })
	}
	slices.SortFunc(g.Edges, func(a, b EdgeMessage) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortStableFunc(g.Diagnostics, func(a, b DiagnosticMessage) int {
		return cmp.Or(cmp.Compare(a.Severity, b.Severity), cmp.Compare(a.Node, b.Node), cmp.Compare(a.Message, b.Message))
	})
	normalizeModules(g.Modules)
	linkCallers(g)
	g.Connectors = ConnectorsOf(g.Nodes)
	g.ConnectorCatalog = ConnectorCatalog()
	g.Revision = g.structureHash()
}

// CompareSource orders sources by root, file, then line.
func CompareSource(a, b SourceMessage) int {
	return cmp.Or(cmp.Compare(a.GoModule, b.GoModule), cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.EndLine, b.EndLine))
}

// structuralNode is n without what changes while the product runs: its
// counters, a store's count, privacy counters and what its history weighs,
// a subscription's and a queued command's dead letters, a mailer's tallies,
// a workflow's census.
func structuralNode(in *NodeEntity) NodeEntity {
	n := *in
	n.Stats = nil
	if n.Store != nil {
		st := *n.Store
		st.Count = nil
		st.Privacy = st.Privacy.structure()
		st.History = st.History.structure()
		n.Store = &st
	}
	if n.Subscription != nil {
		sub := *n.Subscription
		sub.DeadLetters = nil
		n.Subscription = &sub
	}
	if n.Mailer != nil {
		m := *n.Mailer
		m.Queued, m.Sent, m.DeadLetters = nil, nil, nil
		n.Mailer = &m
	}
	if n.Command != nil {
		c := *n.Command
		c.DeadLetters = nil
		n.Command = &c
	}
	if n.Workflow != nil {
		wf := *n.Workflow
		wf.States = slices.Clone(wf.States)
		for j := range wf.States {
			wf.States[j].Count = nil
		}
		n.Workflow = &wf
	}
	return n
}

// structureHash hashes what the graph IS, leaving out what it has observed.
// Layout is cached on it, so a counter ticking must not invalidate it.
func (g *GraphMessage) structureHash() string {
	type structural struct {
		App         string
		Nodes       []NodeEntity
		Edges       []EdgeMessage
		Diagnostics []DiagnosticMessage
		Modules     []ModuleMessage
	}
	nodes := make([]NodeEntity, 0, len(g.Nodes))
	for i := range g.Nodes {
		nodes = append(nodes, structuralNode(&g.Nodes[i]))
	}
	edges := make([]EdgeMessage, 0, len(g.Edges))
	for _, e := range g.Edges {
		e.Observed = nil
		edges = append(edges, e)
	}
	s := structural{App: g.App.Name, Nodes: nodes, Edges: edges, Diagnostics: g.Diagnostics, Modules: g.Modules}
	raw, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:6])
}

// Merge enriches a graph with what another description of the same product
// knows. base is authoritative on which nodes exist — a runtime graph describes
// what the process actually serves —, on the modules it mounts, on what each
// of its ports calls and on which stores feed each of its watches: the app
// chose them when it started, where the analysis only reads the same rules
// over the whole module. extra, typically
// the static analysis, contributes documentation, handler ranges — an
// authorization function's too —, transition callers and the edges it
// found. Nodes only extra knows about are dropped: code that is compiled but
// not mounted in this app is not part of this product — save a node of a
// module's service the base mounts, which a package the binary does not link
// declares: a warning says so.
func Merge(base, extra *GraphMessage) *GraphMessage {
	out := *base
	out.Nodes = slices.Clone(base.Nodes)
	out.Edges = slices.Clone(base.Edges)
	out.Modules = slices.Clone(base.Modules)
	if extra == nil {
		out.Normalize()
		return &out
	}
	chosen := chosenByBase(out.Nodes)
	known := mergeNodes(out.Nodes, extra)
	out.Edges = mergeEdges(out.Edges, extra.Edges, known, chosen)
	out.Diagnostics = append(out.Diagnostics, mergedDiagnostics(known, extra)...)
	out.Normalize()
	return &out
}

// mergeNodes enriches each of the base's nodes with what extra knows of it,
// and returns the IDs of the base's nodes: the only ones the merge keeps.
func mergeNodes(nodes []NodeEntity, extra *GraphMessage) map[string]bool {
	known := make(map[string]bool, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		known[n.ID] = true
		if x := extra.Node(n.ID); x != nil {
			mergeNode(n, x)
		}
	}
	return known
}

// mergeEdges adds to the base's edges what extra found between two known
// nodes: the evidence of an edge the base has, or an edge the base does not
// have and did not rule out by its own choice.
func mergeEdges(edges, extra []EdgeMessage, known map[string]bool, chosen func(EdgeMessage) bool) []EdgeMessage {
	index := indexEdges(edges)
	for _, e := range extra {
		if !known[e.From] || !known[e.To] {
			continue
		}
		if e.ID == "" {
			e.ID = EdgeID(e.From, e.Kind, e.To, e.Label)
		}
		if i, ok := index[e.ID]; ok {
			cur := &edges[i]
			cur.Static = append(cur.Static, e.Static...)
			cur.Declared = cur.Declared || e.Declared
			continue
		}
		// A binding the base did not choose — an implementation this app does
		// not mount, a kit.Bind another app gives —, or a store the base does
		// not hear with a watch.
		if !chosen(e) {
			e.Observed = nil
			index[e.ID] = len(edges)
			edges = append(edges, e)
		}
	}
	return edges
}

// indexEdges gives each edge its derived ID when it has none, and indexes
// it: an unnormalized base would otherwise index every edge under "" and
// the merge append the extra graph's copy of an edge instead of enriching
// it.
func indexEdges(edges []EdgeMessage) map[string]int {
	index := make(map[string]int, len(edges))
	for i := range edges {
		e := &edges[i]
		if e.ID == "" {
			e.ID = EdgeID(e.From, e.Kind, e.To, e.Label)
		}
		index[e.ID] = i
	}
	return index
}

// chosenByBase reports whether a declared edge is one the base alone says: a
// port's binding, a store feeding a watch — what the app chose at its start.
func chosenByBase(nodes []NodeEntity) func(EdgeMessage) bool {
	ports, watches := map[string]bool{}, map[string]bool{}
	for _, n := range nodes {
		switch {
		case n.Kind == KindPort:
			ports[n.ID] = true
		case n.Subscription != nil && n.Subscription.Mark != "":
			watches[n.ID] = true
		}
	}
	return func(e EdgeMessage) bool { return e.Declared && (ports[e.From] || watches[e.To]) }
}

// mergedDiagnostics are what extra says of the nodes base knows, or of no
// node — and of the nodes it finds on a mounted module's service that base
// lacks.
func mergedDiagnostics(known map[string]bool, extra *GraphMessage) []DiagnosticMessage {
	var out []DiagnosticMessage
	for _, d := range extra.Diagnostics {
		if d.Node == "" || known[d.Node] {
			out = append(out, d)
		}
	}
	return append(out, unmountedNodes(known, extra)...)
}

// mergeNode copies into n what x knows and n does not.
func mergeNode(n, x *NodeEntity) {
	mergeDocs(n, x)
	mergeLoop(n, x)
	mergeSources(n, x)
	mergeAuthorize(n, x)
	mergeTransitions(n, x)
}

// mergeDocs takes x's documentation and code level where n has none.
func mergeDocs(n, x *NodeEntity) {
	if n.Doc == "" {
		n.Doc = x.Doc
	}
	if n.Docs == nil {
		n.Docs = x.Docs
	}
	if n.Code == nil {
		n.Code = x.Code
	}
}

// mergeLoop takes the select cases x read in a hand-written loop's body.
func mergeLoop(n, x *NodeEntity) {
	if n.Loop == nil || x.Loop == nil || len(n.Loop.Selects) > 0 || len(x.Loop.Selects) == 0 {
		return
	}
	l := *n.Loop // n shares its pointers with the base graph: copy, then change
	l.Selects = x.Loop.Selects
	n.Loop = &l
}

// mergeSources takes x's ranges where n knows a first line only.
func mergeSources(n, x *NodeEntity) {
	if wider(n.Source, x.Source) {
		n.Source = x.Source
	}
	if wider(n.Handler, x.Handler) {
		n.Handler = x.Handler
	}
}

// wider reports whether read is a range where have is none or a first
// line only.
func wider(have, read *SourceMessage) bool {
	return read != nil && (have == nil || have.EndLine == 0)
}

// mergeTransitions gives each of a workflow's transitions the position x
// found for it, where n has none. n shares its pointers with the base
// graph: copy, then change.
func mergeTransitions(n, x *NodeEntity) {
	if n.Workflow == nil || x.Workflow == nil {
		return
	}
	wf := *n.Workflow
	wf.Transitions = slices.Clone(wf.Transitions)
	for i := range wf.Transitions {
		t := &wf.Transitions[i]
		if t.Source == nil {
			t.Source = transitionSource(x.Workflow.Transitions, t)
		}
	}
	n.Workflow = &wf
}

// transitionSource is the position of the transition of found that is t.
func transitionSource(found []TransitionSpec, t *TransitionSpec) *SourceMessage {
	for i := range found {
		xt := &found[i]
		if xt.Event == t.Event && xt.From == t.From && xt.To == t.To {
			return xt.Source
		}
	}
	return nil
}

// mergeAuthorize gives the authorization function of a command or a query
// the range the analysis read, where the runtime knows its first line only.
// n shares its pointers with the base graph: copy, then change.
func mergeAuthorize(n, x *NodeEntity) {
	if n.Command != nil && x.Command != nil && widerRange(n.Command.Authorize, x.Command.Authorize) {
		c := *n.Command
		c.Authorize = x.Command.Authorize
		n.Command = &c
	}
	if n.Query != nil && x.Query != nil && widerRange(n.Query.Authorize, x.Query.Authorize) {
		q := *n.Query
		q.Authorize = x.Query.Authorize
		n.Query = &q
	}
}

// widerRange reports whether read is a whole range where have is none or a
// first line only.
func widerRange(have, read *SourceMessage) bool {
	return read != nil && read.EndLine > 0 && (have == nil || have.EndLine == 0)
}

// linkCallers fills each workflow transition's Callers from the transitions
// edges that name its event.
func linkCallers(g *GraphMessage) {
	callers := map[string][]string{} // workflow|event → caller node IDs
	for _, e := range g.Edges {
		if e.Kind == EdgeTransitions && e.Label != "" {
			k := e.To + "|" + e.Label
			callers[k] = append(callers[k], e.From)
		}
	}
	for i := range g.Nodes {
		wf := g.Nodes[i].Workflow
		if wf == nil {
			continue
		}
		for j := range wf.Transitions {
			t := &wf.Transitions[j]
			if t.Trigger != TriggerEvent {
				continue
			}
			c := slices.Clone(callers[g.Nodes[i].ID+"|"+t.Event])
			slices.Sort(c)
			t.Callers = slices.Compact(c)
		}
	}
}

// ServiceOf returns the service part of a node ID.
func ServiceOf(id string) string {
	svc, _, _ := strings.Cut(id, "/")
	return svc
}
