// Package kit — a watch in a running app: its queue and its consumer.
package kit

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/queue"
	"github.com/kitsunium/sdk/pkg/v1/trace"
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
// Goroutine lifecycle: one goroutine consumes the watch's queue until stop
// cancels its context; it closes r.done, which stop waits for.
func (w *Watch) start(_ context.Context, a *App) error {
	broker, err := a.openQueue(filepath.Join(w.svc.name, "queues", w.name), w.maxDeliver)
	if err != nil {
		return failure(CodeWatchQueue, "WATCH_QUEUE", "a watch's queue cannot be opened", err, errs.String("watch", w.id))
	}
	r := &watchRun{broker: broker, done: make(chan struct{}), fields: map[string][]FieldRefValue{}}
	feeders := w.feeders(a)
	for _, st := range feeders {
		r.fields[st.base().id] = markedFields(st, w.mark)
	}
	w.run.Store(r)
	for _, st := range feeders {
		r.unfeed = append(r.unfeed, st.feed(w))
	}
	loop := a.loop(w.id+" consumer", w.id, model.LoopConsumer, consumerHow)
	ctx, cancel := context.WithCancel(context.WithoutCancel(a.baseCtx))
	r.cancel = cancel
	go func() {
		done := r.done
		defer close(done)
		w.consume(a.asLoop(ctx, loop.Name), a, r, loop)
	}()
	return nil
}

// consume runs the watch's consumer until ctx ends: one notice at a time
// per worker, as a queued command's consumer does — a watch's handler reads
// and may call out, and a batch would hold notices behind a slow one, their
// leases lapsing.
func (w *Watch) consume(ctx context.Context, a *App, r *watchRun, loop *loopState) {
	err := queue.Consume(ctx, r.broker, queue.ConsumerConfig{
		Handler:             w.deliver(a, r, loop),
		Clock:               a.clock,
		PollInterval:        pollInterval,
		Parallelism:         w.parallelism,
		BatchSize:           1,
		HandlerIsIdempotent: true,
	})
	if err != nil && ctx.Err() == nil {
		a.problem(w.id, say("subscription.stopped", "subscription", w.id, "detail", errs.PublicOf(err)))
		loop.died(a, err)
	}
}

// deliver is the queue's handler: in the write's trace, a span on the watch
// from the store that was written — the delivers edge —, then the
// handler, told what the notice says and the store's marked fields, in a
// context marked as its own (handling): what it writes there,
// a watch that hears its own module's stores does not hear back.
func (w *Watch) deliver(a *App, r *watchRun, loop *loopState) queue.Handler {
	c := consumer{a: a, loop: loop, node: w.id, name: w.name, wake: model.WakeChange}
	return func(ctx context.Context, d queue.Delivery) error {
		var n watchNotice
		decodeErr := json.Unmarshal(d.Message.Payload, &n)
		if decodeErr != nil {
			n = watchNotice{}
		}
		return c.deliver(ctx, &d, origin{trace: n.Trace, node: n.Store}, func(ctx context.Context, sp *span) error {
			if decodeErr != nil {
				return failure(CodeUndecodable, "MESSAGE_UNDECODABLE", "the notice does not decode", decodeErr,
					errs.String("watch", w.id), errs.String("message", d.Message.ID))
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
	if r == nil || r.cancel == nil {
		return nil
	}
	for _, unfeed := range r.unfeed {
		unfeed()
	}
	r.cancel()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// notice queues in the watch's queue what a store says it wrote: which
// store, which key, whether it was deleted, and the write's trace. The
// write stands whatever happens here: a queue that refuses the notice loses
// it, and kit says so.
func (w *Watch) notice(ctx context.Context, store, key string, deleted bool) {
	r, a := w.run.Load(), w.app()
	if r == nil || a == nil {
		return
	}
	header, _ := trace.FormatTraceParent(trace.SpanContextFromContext(ctx))
	payload, err := json.Marshal(watchNotice{Trace: header, Store: store, Key: key, Deleted: deleted})
	if err == nil {
		// The write is done: a caller that gives up now does not take its
		// notice back.
		_, err = r.broker.Publish(context.WithoutCancel(ctx), payload)
	}
	if err != nil {
		lost := failure(CodeWatchQueue, "WATCH_NOTICE_LOST", "the watch's queue refused the notice", err,
			errs.String("watch", w.id), errs.String("store", store))
		a.problem(w.id, say("watch.lost", "watch", w.id, "store", store, "detail", errs.PublicOf(lost)))
	}
}
