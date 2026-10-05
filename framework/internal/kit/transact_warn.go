package kit

import (
	"maps"
	"slices"

	"github.com/kitsunium/sdk/framework/model"
)

// A transaction writes one database (ADR 0004): at run time, a write to a
// store of a second one is refused (CodeTransactionSpan). In dev, once the
// static analysis ran, kit warns of the code that would be: a command whose
// code writes stores of two databases — the data directory counting as
// one —, and a kit.Transact function whose steps do. What the code writes is
// what the diagram shows: the writes and persists edges of a command, the
// analysis's and those a run drew; the effects under a kit.Transact block.

// transactionWarnings are, in dev, the commands and the kit.Transact
// functions of g and static — the static analysis, nil before it ran —
// whose code writes stores of two databases. Outside dev, kit says nothing.
func (a *App) transactionWarnings(g, static *model.Graph) []model.Diagnostic {
	if a.cfg.env != EnvDev {
		return nil
	}
	homes := a.storeHomes(g)
	edges := g.Edges
	if static != nil {
		edges = append(slices.Clip(edges), static.Edges...)
	}
	written := writtenHomes(g, edges, homes)
	var out []model.Diagnostic
	for _, id := range slices.Sorted(maps.Keys(written)) {
		if dbs := written[id]; len(dbs) > 1 {
			n := g.Node(id)
			out = append(out, diagnosticOf("warning", id, n.Source, say("transaction.two-databases", "node", id, "databases", homesText(dbs))))
		}
	}
	if static != nil {
		out = append(out, transactBlockWarnings(static, homes)...)
	}
	return out
}

// writtenHomes are, for each command of g that runs in a transaction, the
// databases whose stores edges say it writes.
func writtenHomes(g *model.Graph, edges []model.Edge, homes map[string]string) map[string]map[string]bool {
	written := map[string]map[string]bool{}
	for _, e := range edges {
		home, stored := homes[e.To]
		if !stored || !writesStore(e.Kind) || !transactional(g.Node(e.From)) {
			continue
		}
		if written[e.From] == nil {
			written[e.From] = map[string]bool{}
		}
		written[e.From][home] = true
	}
	return written
}

// writesStore reports whether an edge of kind k writes the store it reaches.
func writesStore(k model.EdgeKind) bool {
	return k == model.EdgeWrites || k == model.EdgePersists
}

// transactional reports whether n is a command that runs in a
// transaction: one whose pipeline has the transaction step.
func transactional(n *model.Node) bool {
	return n != nil && n.Kind == model.KindCommand && n.Command != nil &&
		slices.ContainsFunc(n.Command.Pipeline, func(m model.Mechanic) bool { return m.Kind == "transaction" })
}

// transactBlockWarnings are the kit.Transact functions of static whose
// steps write stores of two databases.
func transactBlockWarnings(static *model.Graph, homes map[string]string) []model.Diagnostic {
	var out []model.Diagnostic
	for _, n := range static.Nodes {
		if n.Code == nil {
			continue
		}
		for _, f := range n.Code.Funcs {
			out = append(out, funcBlockWarnings(n.ID, f, homes)...)
		}
	}
	return out
}

// funcBlockWarnings are the kit.Transact blocks of f, a function of the node
// id, whose steps write stores of two databases.
func funcBlockWarnings(id string, f model.CodeFunc, homes map[string]string) []model.Diagnostic {
	if f.Source == nil {
		return nil
	}
	var out []model.Diagnostic
	for i, b := range f.Blocks {
		if b.Kind != model.BlockTransaction {
			continue
		}
		if dbs := blockHomes(f, i+1, homes); len(dbs) > 1 {
			at := &model.Source{File: f.Source.File, Line: b.Line, GoModule: f.Source.GoModule}
			out = append(out, diagnosticOf("warning", id, at, say("transaction.two-databases", "node", "kit.Transact", "databases", homesText(dbs))))
		}
	}
	return out
}

// blockHomes are the databases whose stores the steps of f inside block —
// an index in f.Blocks plus one — write.
func blockHomes(f model.CodeFunc, block int, homes map[string]string) map[string]bool {
	dbs := map[string]bool{}
	for _, st := range f.Steps {
		if st.Effect == 0 || !under(f, st.Block, block) {
			continue
		}
		e := f.Effects[st.Effect-1]
		if home, stored := homes[e.Target]; stored && writesStore(e.Kind) {
			dbs[home] = true
		}
	}
	return dbs
}

// under reports whether block — an index in f.Blocks plus one — is target
// or lies inside it.
func under(f model.CodeFunc, block, target int) bool {
	for b := block; b > 0 && b <= len(f.Blocks); b = f.Blocks[b-1].Parent {
		if b == target {
			return true
		}
	}
	return false
}

// storeHomes is where each store of g lives, as a transaction counts it: its
// database, as the app declares it, or the data directory — memory counting
// with it. A cache, kept in memory by its own InMemory, is in no
// transaction: it is left out.
func (a *App) storeHomes(g *model.Graph) map[string]string {
	out := map[string]string{}
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Kind != model.KindStore || n.Store == nil {
			continue
		}
		if s, ok := a.findNode(n.ID).(interface{ memoryOnly() bool }); ok && s.memoryOnly() {
			continue
		}
		out[n.ID] = localName
		if n.Store.Database != "" {
			out[n.ID] = "database " + n.Store.Database
		}
	}
	return out
}

// homesText lists where a transaction's writes go, sorted.
func homesText(homes map[string]bool) phrase {
	var items []phrase
	for _, h := range slices.Sorted(maps.Keys(homes)) {
		items = append(items, plain(h))
	}
	return listOf(items)
}
