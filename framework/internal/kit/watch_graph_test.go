package kit_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// What the graph says of a watch (ADR 0008) — its mark, its stores, their
// edges; a product's watch; what a product that mounts none pays; and what
// the start refuses of one.

// The graph says what a watch hears — its mark, the stores that feed it,
// each a declared delivers edge — and nothing of the stores that do not.
func TestTheGraphSaysWhatFeedsAWatch(t *testing.T) {
	g := startReviews(t).Graph()
	n := g.Node(watchID)
	if n == nil || n.Kind != model.KindSubscription || n.Subscription == nil {
		t.Fatalf("the watch: %+v", n)
	}
	want := model.SubscriptionInfo{Mark: "moderated", Stores: []string{"ratings/store/stars", "shop/store/items"}, MaxDeliveries: 5, Parallelism: 1}
	got := *n.Subscription
	got.DeadLetters = nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the watch's info:\n got %+v\nwant %+v", got, want)
	}
	if feeds := storesDelivering(g, watchID); !slices.Equal(feeds, want.Stores) {
		t.Errorf("the declared edges feed the watch from %v, want %v", feeds, want.Stores)
	}
	if !strings.Contains(model.Mermaid(g), "moderation · watches moderated") {
		t.Error("Mermaid does not say what the watch watches")
	}
}

// storesDelivering are the nodes a declared delivers edge goes from to id,
// sorted: the stores that feed a watch.
func storesDelivering(g *model.Graph, id string) []string {
	var out []string
	for _, e := range g.Edges {
		if e.To == id && e.Kind == model.EdgeDelivers && e.Declared {
			out = append(out, e.From)
		}
	}
	slices.Sort(out)
	return out
}

// A product's watch hears the modules' marked stores, never the product's
// own: a watch hears the writes it does not make itself.
func TestAProductsWatchHearsTheModules(t *testing.T) {
	board := kit.NewService("board", "The product's own moderation.")
	board.Watch("content", kit.Moderated, func(context.Context, kit.WrittenEvent) error { return nil })
	g := startApp(t, []*kit.Service{Shop, Audit, Members, board}, kit.Mount(ReviewsModule)).Graph()
	info := g.Node("board/subscription/content").Subscription
	if want := []string{"ratings/store/stars", "reviews/store/reviews"}; info == nil || !slices.Equal(info.Stores, want) {
		t.Errorf("the product's watch hears %+v, want %v", info, want)
	}
}

// A product that mounts no watch pays nothing for its marks: its graph says
// no mark and no store delivers, and its daemon runs no consumer but its
// subscription's.
func TestMarksCostNothingWithoutAWatch(t *testing.T) {
	shop := startApp(t, []*kit.Service{Shop, Audit, Members})
	g := shop.Graph()
	if !g.Node("shop/store/items").Store.Entity.Fields[1].Moderated {
		t.Fatal("the shop's items are not marked: this test proves nothing")
	}
	if said := watchesSaid(g); len(said) > 0 {
		t.Errorf("the graph says a watch's mark, stores or feeds: %v", said)
	}
	subs := slices.DeleteFunc(daemonOf(t, shop).components, func(c string) bool { return !strings.HasPrefix(c, "subscription:") })
	if !slices.Equal(subs, []string{"subscription:audit/subscription/record"}) {
		t.Errorf("the consumers of a product with no watch: %v", subs)
	}
}

// watchesSaid is what a graph says of watches: a subscription's mark or
// stores, and a store's delivers edge.
func watchesSaid(g *model.Graph) []string {
	var out []string
	for _, n := range g.Nodes {
		if s := n.Subscription; s != nil && (s.Mark != "" || len(s.Stores) > 0) {
			out = append(out, n.ID)
		}
	}
	for _, e := range g.Edges {
		if from := g.Node(e.From); e.Kind == model.EdgeDelivers && from != nil && from.Kind == model.KindStore {
			out = append(out, e.ID)
		}
	}
	return out
}

// A watch's declaration is refused at the start when its mark is none of
// kit's, or it has no handler; kit.RecordsOf is refused outside a running
// app.
func TestAWatchsMistakesAreRefused(t *testing.T) {
	desk := kit.NewService("desk", "Watches wrongly.")
	desk.Watch("public", kit.Mark("public"), func(context.Context, kit.WrittenEvent) error { return nil })
	desk.Watch("nobody", kit.Moderated, nil)
	desk.Watch("never", kit.Moderated, func(context.Context, kit.WrittenEvent) error { return nil }, kit.MaxDeliveries(0))
	de := startRefused(t, kit.NewApp("desk", desk))
	for _, want := range []string{
		`watch "public" watches "public", which is no mark`,
		`watch "nobody" has a nil handler`,
		`subscription "never" needs MaxDeliveries ≥ 1`,
	} {
		if !slices.ContainsFunc(de.Diagnostics, func(d model.Diagnostic) bool { return strings.Contains(d.Message, want) }) {
			t.Errorf("no problem says %q: %+v", want, de.Diagnostics)
		}
	}
	var ke *kit.Error
	if _, err := kit.RecordsOf(t.Context(), "shop/store/items"); !errors.As(err, &ke) || ke.Code != kit.WireUnavailable {
		t.Errorf("kit.RecordsOf outside a running app: %v", err)
	}
}
