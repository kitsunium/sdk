package core_test

import (
	"slices"
	"strings"
	"testing"

	model "github.com/kitsunium/sdk/framework/model/internal/core"
)

// Watches (ADR 0008) in the graph: what feeds one is the runtime's to say,
// its mark and its stores are structure, and Mermaid says what it watches.

// watchGraph is a runtime graph whose module's watch the product's reviews
// feed.
func watchGraph(stores ...string) *model.GraphMessage {
	g := &model.GraphMessage{
		Nodes: []model.NodeEntity{
			{
				ID: "moderation.intake/subscription/content", Kind: model.KindSubscription, Name: "content", Service: "moderation.intake",
				Subscription: &model.SubscriptionSpec{Mark: "moderated", Stores: stores, MaxDeliveries: 5, Parallelism: 1},
			},
			{ID: "moderation.intake/store/flags", Kind: model.KindStore, Name: "flags", Service: "moderation.intake"},
			{ID: "shop/store/reviews", Kind: model.KindStore, Name: "reviews", Service: "shop"},
			{ID: "shop/store/drafts", Kind: model.KindStore, Name: "drafts", Service: "shop"},
		},
	}
	for _, s := range stores {
		g.Edges = append(g.Edges, model.EdgeMessage{From: s, To: "moderation.intake/subscription/content", Kind: model.EdgeDelivers, Declared: true})
	}
	return g
}

// Which stores feed a watch is the runtime's to say, as a port's binding
// is: a store the analysis reads as feeding it, and the running app does
// not, does not join it; the runtime's feeds stay, and what the analysis
// found in the watch's code still comes.
func TestMergeKeepsTheRuntimesFeeds(t *testing.T) {
	base := watchGraph("shop/store/reviews")
	extra := &model.GraphMessage{Edges: []model.EdgeMessage{
		{From: "shop/store/reviews", To: "moderation.intake/subscription/content", Kind: model.EdgeDelivers, Declared: true},
		{From: "shop/store/drafts", To: "moderation.intake/subscription/content", Kind: model.EdgeDelivers, Declared: true},
		{
			From: "moderation.intake/subscription/content", To: "moderation.intake/store/flags", Kind: model.EdgeWrites,
			Static: []model.SourceMessage{{File: "intake/screen.go", Line: 20, GoModule: "example.com/moderation"}},
		},
	}}
	g := model.Merge(base, extra)
	if g.Edge("shop/store/drafts|delivers|moderation.intake/subscription/content") != nil {
		t.Error("a store the runtime does not hear joined the watch")
	}
	if e := g.Edge("shop/store/reviews|delivers|moderation.intake/subscription/content"); e == nil || !e.Declared {
		t.Errorf("the runtime's feed: %+v", e)
	}
	if e := g.Edge("moderation.intake/subscription/content|writes|moderation.intake/store/flags"); e == nil || len(e.Static) != 1 {
		t.Errorf("what the watch's code does: %+v", e)
	}
}

// A watch's mark and stores are structure: a store more changes the
// revision.
func TestAWatchsStoresAreStructure(t *testing.T) {
	one, two := watchGraph("shop/store/reviews"), watchGraph("shop/store/drafts", "shop/store/reviews")
	one.Normalize()
	two.Normalize()
	if one.Revision == two.Revision {
		t.Error("a store more feeding a watch did not change the revision")
	}
}

// A watch is a subscription's parallelogram that says what it watches; its
// stores deliver to it, declared: a solid arrow.
func TestMermaidDrawsAWatch(t *testing.T) {
	g := watchGraph("shop/store/reviews")
	g.Nodes = append(g.Nodes, model.NodeEntity{ID: "shop", Kind: model.KindService, Name: "shop"},
		model.NodeEntity{ID: "moderation.intake", Kind: model.KindService, Name: "moderation.intake", Module: "moderation"})
	g.Normalize()
	out := model.Mermaid(g)
	if !strings.Contains(out, `[/"content · watches moderated"/]`) {
		t.Errorf("the watch's shape:\n%s", out)
	}
	lines := strings.Split(out, "\n")
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "-->|delivers|") }) {
		t.Errorf("no solid delivers arrow:\n%s", out)
	}
}
