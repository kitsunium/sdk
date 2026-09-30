package kit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// The watch (ADR 0008): the reviews module (module_test.go) hears the
// writes of what the shop marks moderated — an item's name — and of what
// the ratings mark — a star's note —, never of its own reviews. Every
// funnel of a write tells it, once; a delete says so; its own module's
// stores and an unmarked one say nothing; a failed delivery is retried; the
// delivery continues the write's trace. watch_graph_test.go says what the
// graph shows of it, and what a product that mounts no watch pays.

// watchID is the reviews' watch.
const watchID = "reviews.screening/subscription/moderation"

// heard is what the reviews' watch was told, in order, and the marked
// values it read back, by store and key.
var heard struct {
	sync.Mutex
	notices []kit.WrittenEvent
	values  map[string]string
	// down fails every notice before it is kept: the moderation is down.
	down bool
}

func resetHeard() {
	heard.Lock()
	defer heard.Unlock()
	heard.notices, heard.values, heard.down = nil, map[string]string{}, false
}

// moderationDown makes the watch fail every notice, or lets them through.
func moderationDown(down bool) {
	heard.Lock()
	heard.down = down
	heard.Unlock()
}

// Hear keeps what a notice says, and reads the record back through the
// records port: the value of its first marked field. A record gone by the
// time it reads is no failure: a later notice says it was deleted.
func Hear(ctx context.Context, w kit.WrittenEvent) error {
	heard.Lock()
	if heard.down {
		heard.Unlock()
		return kit.Unavailable("the moderation is down")
	}
	heard.notices = append(heard.notices, w)
	heard.Unlock()
	if w.Deleted || len(w.Fields) == 0 {
		return nil
	}
	recs, err := kit.RecordsOf(ctx, w.Store)
	if err != nil {
		return err
	}
	raw, err := recs.Get(ctx, w.Key)
	var ke *kit.Error
	switch {
	case errors.As(err, &ke) && ke.Code == kit.WireNotFound:
		return nil
	case err != nil:
		return err
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		return err
	}
	value, _ := record[strings.TrimPrefix(w.Fields[0].Path, "/")].(string)
	heard.Lock()
	heard.values[w.Store+"|"+w.Key] = value
	heard.Unlock()
	return nil
}

// noticesOf are the notices heard of one record.
func noticesOf(store, key string) []kit.WrittenEvent {
	heard.Lock()
	defer heard.Unlock()
	var out []kit.WrittenEvent
	for _, w := range heard.notices {
		if w.Store == store && w.Key == key {
			out = append(out, w)
		}
	}
	return out
}

// heardFrom reports whether a notice came from store.
func heardFrom(store string) bool {
	heard.Lock()
	defer heard.Unlock()
	return slices.ContainsFunc(heard.notices, func(w kit.WrittenEvent) bool { return w.Store == store })
}

// valueOf is the marked value the watch read back of a record.
func valueOf(store, key string) string {
	heard.Lock()
	defer heard.Unlock()
	return heard.values[store+"|"+key]
}

// settled writes a last item and waits for its notice: one consumer that
// never failed hears its notices in the order they were queued, so every
// notice queued before it has been heard.
func settled(t *testing.T, app *kit.App) {
	t.Helper()
	last := create(t, app, "last", 1)
	eventually(t, "the last notice", func() bool { return len(noticesOf("shop/store/items", last.ID)) > 0 })
}

// itemsName is the items' marked field, as a notice gives it.
var itemsName = []kit.FieldRefValue{{Store: "shop/store/items", Path: "/name"}}

// Each of the four funnels a write goes through tells the watch once: the
// workflow's insert and replacement, an update, a put and a delete — which
// says it deleted. The watch reads the record itself, through the records
// port.
func TestEveryWriteOfAMarkedFieldIsHeardOnce(t *testing.T) {
	resetHeard()
	app := startReviews(t)
	it := create(t, app, "a lamp", 1)
	if r := call(t, app, "PUT /items/"+it.ID, map[string]string{"name": "a red lamp"}); r.status != http.StatusOK {
		t.Fatalf("rename: %d %s", r.status, r.body)
	}
	if r := call(t, app, "POST /items/"+it.ID+"/publish", noBody); r.status != http.StatusOK {
		t.Fatalf("publish: %d %s", r.status, r.body)
	}
	put := Item{ID: "put", Name: "a chair", Price: 3, State: Draft}
	if err := Items.Put(t.Context(), put); err != nil {
		t.Fatal(err)
	}
	if r := call(t, app, "DELETE /items/"+it.ID, noBody); r.status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	settled(t, app)
	want := []kit.WrittenEvent{
		{Store: "shop/store/items", Key: it.ID, Fields: itemsName},
		{Store: "shop/store/items", Key: it.ID, Fields: itemsName},
		{Store: "shop/store/items", Key: it.ID, Fields: itemsName},
		{Store: "shop/store/items", Key: it.ID, Deleted: true, Fields: itemsName},
	}
	if got := noticesOf("shop/store/items", it.ID); !equalNotices(got, want) {
		t.Errorf("the item's notices:\n got %+v\nwant %+v", got, want)
	}
	if got := noticesOf("shop/store/items", put.ID); len(got) != 1 {
		t.Errorf("a put's notices: %+v", got)
	}
	if v := valueOf("shop/store/items", put.ID); v != "a chair" {
		t.Errorf("the watch read back %q", v)
	}
}

// equalNotices compares two lists of notices, fields included.
func equalNotices(got, want []kit.WrittenEvent) bool {
	return slices.EqualFunc(got, want, func(a, b kit.WrittenEvent) bool {
		return a.Store == b.Store && a.Key == b.Key && a.Deleted == b.Deleted && slices.Equal(a.Fields, b.Fields)
	})
}

// A watch hears another module's marked store, never its own module's, nor
// an unmarked store — the audit's log, written for every item's event.
func TestAWatchHearsTheOthersOnly(t *testing.T) {
	resetHeard()
	app := startReviews(t)
	if r := call(t, app, "POST /reviews/reviews", Review{ID: "r1", Text: "sturdy and bright"}); r.status != http.StatusOK {
		t.Fatalf("POST /reviews/reviews: %d %s", r.status, r.body)
	}
	if err := Stars.Put(t.Context(), Star{Item: "lamp", Count: 5, Note: "bright"}); err != nil {
		t.Fatal(err)
	}
	it := create(t, app, "a lamp", 1)
	eventually(t, "the audit's entry", func() bool {
		_, err := Log.Get(context.Background(), it.ID+".create")
		return err == nil
	})
	settled(t, app)
	if got := noticesOf("ratings/store/stars", "lamp"); !equalNotices(got, []kit.WrittenEvent{
		{Store: "ratings/store/stars", Key: "lamp", Fields: []kit.FieldRefValue{{Store: "ratings/store/stars", Path: "/note"}}},
	}) {
		t.Errorf("another module's notices: %+v", got)
	}
	if valueOf("ratings/store/stars", "lamp") != "bright" {
		t.Errorf("the watch read back %q", valueOf("ratings/store/stars", "lamp"))
	}
	for _, store := range []string{"reviews/store/reviews", "reviews.screening/store/verdicts", "audit/store/log"} {
		if heardFrom(store) {
			t.Errorf("the watch heard %s", store)
		}
	}
}

// A delivery that fails is retried; the record is read at the retry, and a
// handler that is back lets it through.
func TestAFailedNoticeIsRetried(t *testing.T) {
	resetHeard()
	app := startReviews(t)
	moderationDown(true)
	it := create(t, app, "flaky", 1)
	eventually(t, "a failed delivery", func() bool {
		n := app.Graph().Node(watchID)
		return n.Stats != nil && n.Stats.Errors > 0
	})
	if got := noticesOf("shop/store/items", it.ID); len(got) != 0 {
		t.Fatalf("a faulted delivery ran its handler: %+v", got)
	}
	moderationDown(false)
	eventually(t, "the retried delivery", func() bool { return valueOf("shop/store/items", it.ID) == "flaky" })
	if got := noticesOf("shop/store/items", it.ID); len(got) != 1 {
		t.Errorf("the retry was heard %d times", len(got))
	}
}

// The delivery continues the write's trace: a span on the watch, from the
// store that was written, over a delivers edge — which the graph then says
// both declared and observed.
func TestANoticeContinuesTheWritesTrace(t *testing.T) {
	resetHeard()
	app := startReviews(t)
	create(t, app, "traced", 1)
	var delivery *model.Span
	eventually(t, "the delivery in the create's trace", func() bool {
		delivery = deliveryIn(t, app, "shop/endpoint/CreateItem")
		return delivery != nil
	})
	if delivery.From != "shop/store/items" || delivery.Edge != model.EdgeDelivers || delivery.Op != model.OpDeliver {
		t.Fatalf("the delivery in the create's trace: %+v", delivery)
	}
	eventually(t, "the observed edge", func() bool {
		e := app.Graph().Edge("shop/store/items|delivers|" + watchID)
		return e != nil && e.Declared && e.Observed != nil
	})
}

// deliveryIn is the watch's span in a trace rooted at root, once it ended;
// nil before.
func deliveryIn(t *testing.T, app *kit.App, root string) *model.Span {
	t.Helper()
	var traces []model.Trace
	call(t, app, "GET /_kit/api/traces?node="+watchID, noBody).json(t, &traces)
	for i := range traces {
		for j, s := range traces[i].Spans {
			if traces[i].Root == root && s.Node == watchID {
				return &traces[i].Spans[j]
			}
		}
	}
	return nil
}
