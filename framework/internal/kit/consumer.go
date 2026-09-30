// Package kit — the consumer that drains a queue into its handler.
package kit

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/queue"
	"github.com/kitsunium/sdk/pkg/v1/trace"
)

// A queue's consumer, whoever's queue it is — a subscription's, a watch's
// (watch.go), a queued command's (queued.go): where its queue lies, and the
// one way a subscription and a watch run a delivery.

// pollInterval bounds how long an idle consumer waits before it looks
// again. The SDK wakes it at once on a publication or a nack in this
// process, and at the instant a retry falls due or a lease lapses (its ADR
// 0104): what is left to the poll is a message another process published
// into a durable queue.
const pollInterval time.Duration = 5 * time.Second

// defaultMaxDeliveries is how often a consumer is handed a message before it
// is dead-lettered, unless its declaration says otherwise.
const defaultMaxDeliveries int = 5

// consumerBatch is how many messages a consumer takes from its queue at once.
const consumerBatch int = 8

// consumerHow is how a consumer loop is said to wake, in the Studio.
var consumerHow string = "on publish · poll " + pollInterval.String()

// openQueue opens a consumer's queue — a subscription's, a watch's, a queued
// command's —: a file queue at dir under the data directory, memory without
// one. A message is leased for thirty seconds, retried half a second after
// a failure at least, and dead-lettered after maxDeliveries attempts.
func (a *App) openQueue(dir string, maxDeliveries int) (queue.Broker, error) {
	policy := queue.Policy{VisibilityTimeout: 30 * time.Second, RetryDelay: 500 * time.Millisecond, MaxDeliveries: maxDeliveries}
	if a.dataDir == "" {
		return queue.NewMemory(queue.MemoryConfig{Clock: a.clock, Policy: policy})
	}
	return queue.NewFile(queue.FileConfig{Clock: a.clock, Dir: filepath.Join(a.dataDir, dir), Policy: policy})
}

// consumer is who runs a queue's deliveries — a subscription, a watch —:
// its node and name, and the loop it reports to, woken for wake.
type consumer struct {
	a                *App
	loop             *loopState
	node, name, wake string
}

// origin is where a delivered message comes from: the trace it continues —
// the publisher's, the writer's — and the node that sent it.
type origin struct{ trace, node string }

// deliver runs one delivery in the trace its message carries, in a span on
// the consumer's node from the node the message came from, over a delivers
// edge, and reports it to the consumer's loop. A panic in handle is the
// handler's failure.
func (c *consumer) deliver(ctx context.Context, d *queue.Delivery, from origin, handle func(context.Context, *span) error) (err error) {
	if from.trace != "" {
		if sc, perr := trace.ParseTraceParent(from.trace); perr == nil {
			ctx = trace.ContextWithSpanContext(ctx, sc)
		}
	}
	a := c.a
	c.loop.begin(a, c.wake)
	started := a.clock.Now()
	ctx, sp := a.begin(ctx, &spanStart{node: c.node, from: from.node, edge: model.EdgeDelivers, op: model.OpDeliver, name: c.name})
	sp.attr("delivery", strconv.Itoa(d.Deliveries))
	defer func() {
		if p := recover(); p != nil {
			err = failure(CodeHandlerPanic, "HANDLER_PANICKED", "the subscription's handler panicked", nil,
				errs.String("subscription", c.node), errs.String("panic", fmt.Sprint(p)))
		}
		sp.end(err)
		c.loop.ran(a, started, a.clock.Now(), err)
	}()
	return handle(ctx, sp)
}

// defaultDeliveries are a consumer's delivery options until one is given:
// defaultMaxDeliveries attempts, one message at a time.
func defaultDeliveries() deliveryOptions {
	return deliveryOptions{maxDeliveries: defaultMaxDeliveries, parallelism: 1}
}
