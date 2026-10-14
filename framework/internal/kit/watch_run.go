package kit

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
)

// A watch in its app's run (watch.go declares it): its queue and consumer,
// the stores it puts itself on, the notices the stores' funnels queue, and
// their delivery.

// watchNotice is what travels through a watch's queue: a write's store, key
// and kind, and the write's trace context, so the delivery continues its
// trace across the queue. Never a value.
type watchNotice struct {
	Trace   string `json:"trace,omitempty"`
	Store   string `json:"store"`
	Key     string `json:"key"`
	Deleted bool   `json:"deleted,omitempty"`
}

// start opens the watch's queue, puts the watch on the stores that feed it,
// and starts its consumer. The stores start before the subscriptions: every
// write they confirm from now on is noticed.
//
// Goroutine lifecycle: the supervisor's goroutine consumes the watch's
// queue until stop cancels it and waits for it; a consumer whose queue
// fails comes back after kit's backoff.
func (w *Watch) start(_ context.Context, a *App) error {
	broker, db, err := a.openConsumerQueue(&w.nodeBase, w.queueUnder(), w.maxDeliver)
	if err != nil {
		return failure(CodeWatchQueue, "WATCH_QUEUE", "a watch's queue cannot be opened", err, errs.String("watch", w.id))
	}
	r := &watchRun{broker: broker, db: db, fields: map[string][]FieldRefValue{}}
	feeders := w.feeders(a)
	for _, st := range feeders {
		r.fields[st.base().id] = markedFields(st, w.mark)
	}
	w.run.Store(r)
	for _, st := range feeders {
		r.unfeed = append(r.unfeed, st.feed(w))
	}
	loop := a.loop(w.id+" consumer", w.id, model.LoopConsumer, consumerHow)
	c := consumer{a: a, loop: loop, node: w.id, name: w.name, wake: model.WakeChange}
	handler := w.deliver(&c, r)
	r.sup, err = c.supervise(func(ctx context.Context) error { return w.consume(ctx, a, r, handler) })
	if err != nil {
		for _, unfeed := range r.unfeed {
			unfeed()
		}
		return failure(CodeLoopStart, "LOOP_START", "a watch's consumer cannot start", err, errs.String("watch", w.id))
	}
	return nil
}

// queueUnder is what the watch's queue is kept under.
func (w *Watch) queueUnder() string { return underQueues }

// consume runs the watch's consumer until ctx ends or its queue fails: one
// notice at a time per worker, as a queued command's consumer does — a
// watch's handler reads and may call out, and a batch would hold notices
// behind a slow one, their leases lapsing.
func (w *Watch) consume(ctx context.Context, a *App, r *watchRun, handler queue.Handler) error {
	return queue.Consume(ctx, r.broker, queue.ConsumerConfig{
		Handler:             handler,
		Clock:               a.clock,
		PollInterval:        pollInterval,
		Parallelism:         w.parallelism,
		BatchSize:           1,
		HandlerIsIdempotent: true,
	})
}

// deliver is the queue's handler: in the write's trace, a span on the watch
// from the store that was written — the delivers edge —, then the
// handler, told what the notice says and the store's marked fields, in a
// context marked as its own (handling): what it writes there,
// a watch that hears its own module's stores does not hear back. A notice
// that does not decode is dead-lettered on its first delivery (ADR 0151
// §D8).
func (w *Watch) deliver(c *consumer, r *watchRun) queue.Handler {
	return func(ctx context.Context, d queue.Delivery) error {
		var n watchNotice
		decodeErr := json.Unmarshal(d.Message.Payload, &n)
		if decodeErr != nil {
			n = watchNotice{}
		}
		return c.deliver(ctx, &d, origin{trace: n.Trace, node: n.Store}, func(ctx context.Context, sp *span) error {
			if decodeErr != nil {
				return queue.DoNotRetry(failure(CodeUndecodable, "MESSAGE_UNDECODABLE", "the notice does not decode", decodeErr,
					errs.String("watch", w.id), errs.String("message", d.Message.ID)))
			}
			written := WrittenEvent{Store: n.Store, Key: n.Key, Deleted: n.Deleted, Fields: slices.Clone(r.fields[n.Store])}
			if sp.detailed() {
				sp.request(written)
			}
			return w.handler(handling(ctx, w), written)
		})
	}
}

// stop takes the watch off its stores, then ends its consumer: what waits
// in a file queue is delivered at the next start.
func (w *Watch) stop(ctx context.Context, _ *App) error {
	r := w.run.Load()
	if r == nil || r.sup == nil {
		return nil
	}
	for _, unfeed := range r.unfeed {
		unfeed()
	}
	return r.sup.Stop(ctx)
}

// noticeOf is what a watch's queue carries of a write a store says it made:
// which store, which key, whether it was deleted, and the write's trace —
// read from ctx, the write's context.
func noticeOf(ctx context.Context, store, key string, deleted bool) ([]byte, error) {
	header, _ := trace.FormatTraceParent(trace.SpanContextFromContext(ctx))
	return json.Marshal(watchNotice{Trace: header, Store: store, Key: key, Deleted: deleted})
}

// notice queues payload, a notice of a write to store, in the watch's queue
// r, once the write stands — outside a transaction, or after its commit: a
// queue that refuses the notice loses it, and kit says so.
func (w *Watch) notice(ctx context.Context, r *watchRun, store string, payload []byte) {
	// The write is done: a caller that gives up now does not take its notice
	// back.
	if err := w.post(context.WithoutCancel(ctx), r, store, payload); err != nil {
		w.lost(store, err)
	}
}

// post puts payload in the watch's queue r: in the transaction ctx carries,
// when it carries the one of the database r lives on (ADR 0178 C1). Its
// refusal is the watch's.
func (w *Watch) post(ctx context.Context, r *watchRun, store string, payload []byte) error {
	if _, err := r.broker.Publish(ctx, payload); err != nil {
		return failure(CodeWatchQueue, "WATCH_NOTICE_REFUSED", "the watch's queue refused the notice", err,
			errs.String("watch", w.id), errs.String("store", store))
	}
	return nil
}

// lost says that the notice of a write to store, which stands, is lost.
func (w *Watch) lost(store string, err error) {
	a := w.app()
	if a == nil {
		return
	}
	lost := failure(CodeWatchQueue, "WATCH_NOTICE_LOST", "the watch's queue refused the notice", err,
		errs.String("watch", w.id), errs.String("store", store))
	a.problem(w.id, say("watch.lost", "watch", w.id, "store", store, "detail", errs.PublicOf(lost)))
}
