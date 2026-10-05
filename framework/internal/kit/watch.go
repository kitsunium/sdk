package kit

import (
	"context"
	"slices"
	"sync/atomic"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
)

// Watches (ADR 0008). A module hears of the writes of the fields a product
// marks, without importing the product: Service.Watch declares a
// subscription whose deliveries come from stores instead of a topic. Every
// store whose entity holds a field with the watch's mark (ADR 0006:
// kit.Personal, kit.Special, kit.Moderated) feeds it — the product's and the
// other modules', never those of the watch's own module; for a watch of the
// product's, never the product's own, unless the watch asks for them
// (OwnStores) —, and draws a declared delivers edge to it. kit's own stores
// feed none. A watch that hears its own module's stores never hears the
// writes its own handler makes: the handler's context says whose it is
// (handling), and the funnels skip them.
//
// A store tells its watches where kit confirms a write: in Store.write,
// modify and remove, and in a workflow's transitionWrite — the four funnels
// every write goes through, ADR 0006's erasures and retention included —,
// with the write's context. Each watch queues a notice in its own queue,
// the SDK's, as each subscription has its own (root invariant 7), and its
// delivery continues the write's trace. A notice carries no value — which
// store, which key, whether the record was deleted —: the watch reads the
// record itself, through the records port (RecordsOf), so nothing of the
// record is copied into a queue. It is delivered at least once, retried and
// dead-lettered, like a subscription's message.
//
// The gap: the notice is queued right after the write, not in its
// transaction — kit keeps none until ADR 0004's step 2 —, so a crash
// between the two loses it, and so does a queue that refuses it: the write
// stands, and kit says the notice was lost. A notice queued is kept as the
// queue keeps it: on disk under a data directory, in memory without one. A
// store no watch hears pays one atomic load per write, and a product that
// mounts no watch nothing else.

// WrittenEvent is what a watch is told of a write: which store, which record, and
// whether it was deleted — never a value. The watch reads the record itself
// ([RecordsOf]): without its secret members, and journaled.
type WrittenEvent struct {
	// Store is the store's node ID: "shop/store/reviews".
	Store string `json:"store"`
	// Key is the record's key in the store.
	Key string `json:"key"`
	// Deleted is set when the write deleted the record: there is nothing
	// left to read.
	Deleted bool `json:"deleted,omitempty"`
	// Fields are the store's fields that carry the watch's mark, as
	// [App.Fields] lists them. Which of them the write changed, the notice
	// does not say.
	Fields []FieldRefValue `json:"fields,omitempty"`
}

// Watch is a subscription fed by stores instead of a topic: its handler is
// told of every write kit confirms on a store whose entity holds a field
// with its mark. [Service.Watch] declares one.
type Watch struct {
	nodeBase
	mark        Mark
	handler     func(context.Context, WrittenEvent) error
	maxDeliver  int
	parallelism int
	// ownStores: it hears its own module's stores too, and never the
	// writes of its own handler (OwnStores).
	ownStores bool

	// run is the watch in its app's current run, or its last one.
	run atomic.Pointer[watchRun]
}

// marks are the marks a watch may hear.
var marks = []Mark{Personal, Special, Moderated}

// Watch declares a watch of the service: handler is told of every write kit
// confirms on a store whose entity holds a field that carries mark — the
// product's stores and the other modules', never those of the module the
// service belongs to (for a service of the product's, never the product's
// own) unless [OwnStores] says so. The notice says which store, which
// record and whether it was deleted; the handler reads the record itself
// ([RecordsOf]). Delivery is at least once and in no set order, retried and
// dead-lettered after [MaxDeliveries] attempts: handler must be idempotent,
// and it hears the writes it makes itself on another's store — with
// OwnStores, never a write its own handler makes.
//
//	var Content = Service.Watch("content", kit.Moderated, Screen)
//	var Posts = Service.Watch("posts", kit.Moderated, Stamp, kit.OwnStores())
//
//go:noinline
func (s *Service) Watch(name string, mark Mark, handler func(context.Context, WrittenEvent) error, opts ...SubscriptionConfigurer) *Watch {
	o := subscriptionOptions{deliveryOptions: defaultDeliveries()}
	for _, opt := range opts {
		opt.subscriptionConfigure(&o)
	}
	w := &Watch{mark: mark, handler: handler, maxDeliver: o.maxDeliveries, parallelism: o.parallelism, ownStores: o.ownStores}
	w.kind, w.name, w.decl = model.KindSubscription, name, callerPos()
	if p, _ := funcInfo(handler); p.file() != "" {
		w.body = &p
	}
	s.add(w, true)
	switch {
	case !slices.Contains(marks, mark):
		s.problem(w.decl, w.id, "watch.mark", "name", name, "mark", string(mark))
	case handler == nil:
		s.problem(w.decl, w.id, "watch.nil-handler", "name", name)
	case o.maxDeliveries < 1 || o.parallelism < 1:
		s.problem(w.decl, w.id, "subscription.limits", "name", name)
	}
	return w
}

// marking is a store, whatever its entity type, as its marks say it: its
// node, and its type's classification.
type marking interface {
	base() *nodeBase
	plan() *classPlan
}

// markedFields are the fields of st that carry m, in its entity's order.
func markedFields(st marking, m Mark) []FieldRefValue {
	plan := st.plan()
	var out []FieldRefValue
	for _, mb := range plan.members {
		if marked(&mb.tag, m) {
			out = append(out, FieldRefValue{Store: st.base().id, Path: mb.pointer, Class: mb.tag.effective(), Subject: plan.subjectPointer()})
		}
	}
	return out
}

// feeder is a store, whatever its entity type, as a watch hears it.
type feeder interface {
	marking
	// feed puts w among the watches the store's writes are told to, and
	// returns what takes exactly it off.
	feed(w *Watch) (unfeed func())
}

// Mark is what a module looks for in the product's fields: kit.Personal,
// kit.Special or kit.Moderated. There is no mark for public or secret:
// there is nothing to find in the first, and nothing may watch the second.
type Mark string

// FieldRefValue is one field of one store that carries a mark: where the field
// is, and what its tag says of it.
type FieldRefValue struct {
	// Store is the store's node ID.
	Store string `json:"store"`
	// Path is the field's JSON pointer in the entity: "/body". A "*"
	// stands for every element of an array or every value of a map:
	// "/replies/*/body".
	Path string `json:"path"`
	// Class is the field's class; "" when it is unclassified.
	Class string `json:"class,omitempty"`
	// Subject is the pointer of the store's subject — for moderated
	// content, its author — or "" when the store has none.
	Subject string `json:"subject,omitempty"`
}

// watchRun is a watch in one run of its app: its queue and its consumer, and
// what it hears — the marked fields of each store that feeds it.
type watchRun struct {
	broker queue.Broker
	cancel context.CancelFunc
	done   chan struct{}
	// fields are the marked fields of each store that feeds the watch, by
	// store ID; unfeed takes the watch off those stores.
	fields map[string][]FieldRefValue
	unfeed []func()
}

// feeders are the stores of a that feed w, in the order the app mounts
// them: those whose entity holds a field with its mark, but its own
// module's — unless it hears its own (OwnStores) —; kit's own are never
// among them.
func (w *Watch) feeders(a *App) []feeder {
	var out []feeder
	for _, st := range a.productStores() {
		ws, ok := st.(feeder)
		if ok && (w.ownStores || st.base().svc.module != w.svc.module) && len(markedFields(st, w.mark)) > 0 {
			out = append(out, ws)
		}
	}
	return out
}

// handlingKey marks the context of a watch's handler with its watch.
type handlingKey struct{}

// handling returns ctx marked as the context of w's handler: what the
// handler writes in it is its own.
func handling(ctx context.Context, w *Watch) context.Context {
	return context.WithValue(ctx, handlingKey{}, w)
}

// ownWrite reports whether a write made in ctx is w's own handler's, which
// a watch that hears its own module's stores never hears.
func (w *Watch) ownWrite(ctx context.Context) bool {
	h, _ := ctx.Value(handlingKey{}).(*Watch)
	return w.ownStores && h == w
}

// describe says the watch as the graph draws it: a subscription with a mark,
// the stores that feed it — each a declared delivers edge —, its attempts,
// and on a runtime graph its dead letters.
func (w *Watch) describe(a *App, out *model.Node) []model.Edge {
	info := &model.SubscriptionInfo{Mark: string(w.mark), OwnStores: w.ownStores, MaxDeliveries: w.maxDeliver, Parallelism: w.parallelism}
	out.Subscription = info
	var edges []model.Edge
	if a != nil {
		for _, st := range w.feeders(a) {
			info.Stores = append(info.Stores, st.base().id)
			edges = append(edges, model.Edge{From: st.base().id, To: w.id, Kind: model.EdgeDelivers, Declared: true})
		}
		slices.Sort(info.Stores)
	}
	if r := w.run.Load(); r != nil {
		if dl, ok := r.broker.(queue.DeadLetterReader); ok {
			if dead, err := dl.DeadLetters(context.Background(), 1000); err == nil {
				info.DeadLetters = new(len(dead))
			}
		}
	}
	return edges
}

// feed puts w among the watches the store's writes are told to, and returns
// the function that takes exactly it off: a watch that stops takes back its
// own, never another's. Each is one read-modify-write of the copy-on-write
// list, serialised with every other, so two watches starting at once both
// stay.
func (s *StoreService[T]) feed(w *Watch) (unfeed func()) {
	s.feeds.Update(func(cur *[]*Watch) *[]*Watch {
		return feedsOf(append(watchesIn(cur), w))
	})
	return func() {
		s.feeds.Update(func(cur *[]*Watch) *[]*Watch {
			return feedsOf(slices.DeleteFunc(watchesIn(cur), func(x *Watch) bool { return x == w }))
		})
	}
}

// watchesIn is a copy of the watches cur holds, for a feed to change: the
// list a write may be reading is never touched.
func watchesIn(cur *[]*Watch) []*Watch {
	if cur == nil {
		return nil
	}
	return slices.Clone(*cur)
}

// feedsOf is ws as a store keeps it: nil for none, so that a write no watch
// hears stops at one load.
func feedsOf(ws []*Watch) *[]*Watch {
	if len(ws) == 0 {
		return nil
	}
	return &ws
}

// notify tells the watches the store feeds that kit confirmed a write under
// key — a deletion when deleted is set —, with the write's context: each
// queues its notice, once the transaction the write runs in commits — a
// rollback drops it —, at once outside any; a watch that hears its own
// module's stores is not told of its own handler's write. A store no watch
// hears pays this one load.
func (s *StoreService[T]) notify(ctx context.Context, key string, deleted bool) {
	feeds := s.feeds.Load()
	if feeds == nil {
		return
	}
	for _, w := range *feeds {
		if w.ownWrite(ctx) {
			continue
		}
		release := func() error {
			w.notice(withoutUnit(ctx), s.id, key, deleted)
			return nil
		}
		if !hold(ctx, heldEffect{node: w.id, release: release}) {
			w.notice(ctx, s.id, key, deleted)
		}
	}
}
