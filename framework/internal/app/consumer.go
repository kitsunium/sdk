package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/lifecycle"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
)

// A queue's consumer, whoever's queue it is — a subscription's, a watch's
// (watch.go), a queued command's (queued.go): where its queue lies, and the
// one way a subscription and a watch run a delivery. The queue itself is the
// queue subsystem's (queue_plug.go).

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

// What a consumer's queue is kept under, beside its consumer's name: a
// subscription's and a watch's, a queued command's, and a mailer's outbox
// (mailer_outbox.go).
const (
	underQueues   string = "queues"
	underCommands string = "commands"
	underOutbox   string = "outbox"
)

// queueTableService is what a queue's table is named under, in place of a
// service: kit_queue__<service>__<consumer>.
const queueTableService string = "kit_queue"

var (
	// consumerHow is how a consumer loop is said to wake, in the Studio.
	consumerHow string = "on publish · poll " + pollInterval.String()
	// fileQueueStates are the state directories of the SDK's file broker.
	fileQueueStates = []string{"ready", "inflight", "dead"}
)

// queueOwner is a consumer, whatever its message type, as a database's
// tables see it: its node, and what its queue is kept under — "" for a
// command that is not queued, which has none.
type queueOwner interface {
	base() *nodeBase
	queueUnder() string
}

// queueTable is the table of the queue of the consumer b, kept under under,
// on the database that keeps it (ADR 0178 C1): kit_queue__<service>__<name>,
// a queued command's ending __command and a mailer's outbox __outbox, so
// that a subscription, a command and a mailer of one name keep three queues
// as their directories do.
func queueTable(b *nodeBase, under string) string {
	suffix := "__" + b.name
	switch under {
	case underCommands:
		suffix += "__command"
	case underOutbox:
		suffix += "__outbox"
	default:
		// A subscription's or a watch's: the consumer's name alone.
	}
	return tableName(queueTableService, b.svc.name, suffix)
}

// consumerPolicy is the policy of every consumer's queue: a message is leased
// for thirty seconds, retried half a second after its first failure, the
// wait doubling after each — the queue's own growing delay (ADR 0151 §D7),
// never more than kit's backoff ceiling, a minute —, and dead-lettered after
// maxDeliveries attempts, or at its first failure when no retry can help
// (doNotRetry).
func consumerPolicy(maxDeliveries int) plug.QueuePolicyValue {
	return plug.QueuePolicyValue{VisibilityTimeout: 30 * time.Second, RetryDelay: 500 * time.Millisecond, MaxRetryDelay: loopBackoffMax, MaxDeliveries: maxDeliveries}
}

// openConsumerQueue opens the queue of the consumer b — a subscription's, a
// watch's, a queued command's —, kept under under: on the database that
// keeps the consumer when it is open (ADR 0178 C1), one table of the SDK's
// SQL broker, and the database it returns is where a publication made in a
// transaction on it joins that transaction; else a file or a memory queue
// (openQueue), and no database. A test's wrapper stands in front of either.
func (a *App) openConsumerQueue(b *nodeBase, under string, maxDeliveries int) (plug.Broker, *databaseRun, error) {
	r := a.queueDatabase(b)
	if r == nil {
		broker, err := a.openQueue(b, filepath.Join(b.svc.name, under, b.name), maxDeliveries)
		return broker, nil, err
	}
	broker, err := a.openSQLQueue(b, under, r, plug.QueueConfig{Policy: consumerPolicy(maxDeliveries)})
	if err != nil {
		return nil, nil, err
	}
	return broker, r, nil
}

// openSQLQueue opens the queue of the consumer b, kept under under, on the
// open database r as cfg says the rest — one table of the SDK's SQL broker,
// which joins the transaction its context carries —, and says when b's
// queue left messages in the data directory it moved from. A test's wrapper
// stands in front of it.
func (a *App) openSQLQueue(b *nodeBase, under string, r *databaseRun, cfg plug.QueueConfig) (plug.Broker, error) {
	if err := sqlQueueConfig(&cfg, b, r, queueTable(b, under)); err != nil {
		return nil, err
	}
	cfg.Wrap = a.wrapQueue
	broker, err := a.openQueueWith(cfg)
	if err != nil {
		return nil, err
	}
	a.warnFileQueueLeft(b, under, r)
	return broker, nil
}

// sqlQueueConfig places the queue of cfg, the consumer b's, in table on the
// open database r: the SDK's SQL broker over the database the store
// subsystem opened, which a publication in a transaction on it joins.
func sqlQueueConfig(cfg *plug.QueueConfig, b *nodeBase, r *databaseRun, table string) error {
	db := r.database()
	if db == nil {
		return Unavailable(fmt.Sprintf("database %q is not open", r.d.name))
	}
	cfg.Name, cfg.Database, cfg.Table = b.id, db, table
	return nil
}

// warnFileQueueLeft says, at its start, that a consumer's queue moved from
// the data directory to a database and left messages behind: they wait in
// the file queue, which nothing consumes now, until a copy moves them.
func (a *App) warnFileQueueLeft(b *nodeBase, under string, r *databaseRun) {
	if !fileQueueHolds(filepath.Join(a.dataDir, b.svc.name, under, b.name)) {
		return
	}
	a.problem(b.id, say("consumer.file-queue-left", "node", b.id, "database", r.d.name))
}

// fileQueueHolds reports whether the file queue at dir holds a message —
// waiting, leased or dead: a ".msg" or ".dead" file of the SDK's file
// broker in one of its state directories, which it keeps once empty, a
// mailer's outbox among them since its first run. A file another tool left
// there counts too: a copy is what decides what it is.
func fileQueueHolds(dir string) bool {
	for _, state := range fileQueueStates {
		entries, err := os.ReadDir(filepath.Join(dir, state))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && (strings.HasSuffix(e.Name(), ".msg") || strings.HasSuffix(e.Name(), ".dead")) {
				return true
			}
		}
	}
	return false
}

// openQueue opens the queue of the consumer b that no open database keeps:
// a file queue at dir under the data directory, memory without one, under
// consumerPolicy, behind a test's wrapper.
func (a *App) openQueue(b *nodeBase, dir string, maxDeliveries int) (plug.Broker, error) {
	cfg := plug.QueueConfig{Name: b.id, Policy: consumerPolicy(maxDeliveries), Wrap: a.wrapQueue}
	if a.dataDir != "" {
		cfg.Dir = filepath.Join(a.dataDir, dir)
	}
	return a.openQueueWith(cfg)
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
func (c *consumer) deliver(ctx context.Context, d *plug.DeliveryValue, from origin, handle func(context.Context, *span) error) (err error) {
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

// supervise runs consume — its queue's Consume — under the SDK's
// supervisor (ADR 0112) until the returned supervisor is stopped. Consume
// returns when its queue's storage fails, which no poll gets out of: the
// supervisor runs it again after kit's backoff — one second, doubling, up
// to a minute —, and the consumer's loop shows the failure and counts the
// restart, where an unsupervised consumer stayed down while its app served.
// The consumer's goroutine carries its loop's label (dev).
func (c *consumer) supervise(consume func(context.Context) error) (*lifecycle.Supervisor, error) {
	a, loop := c.a, c.loop
	sup, err := lifecycle.NewSupervisor(loop.Name, func(ctx context.Context) error {
		return consume(a.asLoop(ctx, loop.Name))
	}, lifecycle.SupervisorConfig{
		Clock:        a.clock,
		Backoff:      loopBackoff,
		HealthyAfter: healthyRun,
		Observe: func(e lifecycle.SupervisionEvent) {
			c.observe(&e)
		},
	})
	if err != nil {
		return nil, err
	}
	if err := sup.Start(context.WithoutCancel(a.baseCtx)); err != nil {
		return nil, err
	}
	return sup, nil
}

// observe draws what the supervisor did on the consumer's loop. It runs on
// the supervisor's goroutine, one event at a time. A run is not one of the
// loop's runs — each delivery is (deliver) —, so only its failure, its
// restart and the end of the supervision show.
func (c *consumer) observe(e *lifecycle.SupervisionEvent) {
	a, loop := c.a, c.loop
	switch e.Phase {
	case lifecycle.SupervisionRunStarted:
		if e.Run > 1 {
			loop.restarted(a)
		}
	case lifecycle.SupervisionRunEnded:
		if e.Err != nil && !e.Stopping {
			a.problem(c.node, say("consumer.failed", "node", c.node, "detail", errs.PublicOf(e.Err)))
			loop.failed(a, e.Err)
		}
	case lifecycle.SupervisionRestarting:
		logger.Warn(context.Background(), a.log, "a consumer stopped; restarting it", logger.String("node", c.node), logger.String("after", e.Delay.String()))
		loop.idle(a, e.At.Add(e.Delay))
		loop.setState(a, model.LoopRestarting)
	case lifecycle.SupervisionStopped:
		loop.setState(a, model.LoopStopped)
	default:
		// Every other phase changes nothing the Studio shows.
	}
}

// defaultDeliveries are a consumer's delivery options until one is given:
// defaultMaxDeliveries attempts, one message at a time.
func defaultDeliveries() deliveryOptions {
	return deliveryOptions{maxDeliveries: defaultMaxDeliveries, parallelism: 1}
}
