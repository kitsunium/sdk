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
// service: app_queue__<service>__<consumer>.
const queueTableService string = frameworkName + "_queue"

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
// on the database that keeps it (ADR 0178 C1): app_queue__<service>__<name>,
// a queued command's ending __command and a mailer's outbox __outbox, so
// that a subscription, a command and a mailer of one name keep three queues
// as their directories do.
func queueTable(b *nodeBase, under string) string {
	return tableName(queueTableService, b.svc.name, queueSuffix(b, under))
}

// formerQueueTable is the name a former release gave that table:
// kit_queue__<service>__<name>, before ADR 0180 step 5.
func formerQueueTable(b *nodeBase, under string) string {
	return tableName(formerQueueService, b.svc.name, queueSuffix(b, under))
}

// queueSuffix is what a queue's table name ends with after its service's:
// the consumer's name, and what it is kept under.
func queueSuffix(b *nodeBase, under string) string {
	suffix := "__" + b.name
	switch under {
	case underCommands:
		suffix += "__command"
	case underOutbox:
		suffix += "__outbox"
	default:
		// A subscription's or a watch's: the consumer's name alone.
	}
	return suffix
}

// consumerPolicy is the policy of every consumer's queue: a message is leased
// for thirty seconds — its consumer's lease setting says otherwise
// (consumerRun.policy) —, retried half a second after its first failure, the
// wait doubling after each — the queue's own growing delay (ADR 0151 §D7),
// never more than kit's backoff ceiling, a minute —, and dead-lettered after
// maxDeliveries attempts, or at its first failure when no retry can help
// (doNotRetry).
func consumerPolicy(maxDeliveries int) plug.QueuePolicyValue {
	return plug.QueuePolicyValue{VisibilityTimeout: defaultLease, RetryDelay: 500 * time.Millisecond, MaxRetryDelay: loopBackoffMax, MaxDeliveries: maxDeliveries}
}

// openConsumerQueue opens the queue of the consumer b — a subscription's, a
// watch's, a queued command's —, kept under under, under policy: on the database that
// keeps the consumer when it is open (ADR 0178 C1), one table of the SDK's
// SQL broker, and the database it returns is where a publication made in a
// transaction on it joins that transaction; else a file or a memory queue
// (openQueue), and no database. A test's wrapper stands in front of either.
func (a *App) openConsumerQueue(b *nodeBase, under string, policy plug.QueuePolicyValue) (plug.Broker, *databaseRun, error) {
	r := a.queueDatabase(b)
	if r == nil {
		broker, err := a.openQueue(b, filepath.Join(b.svc.name, under, b.name), policy)
		return broker, nil, err
	}
	broker, err := a.openSQLQueue(b, under, r, plug.QueueConfig{Policy: policy})
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
// policy, behind a test's wrapper.
func (a *App) openQueue(b *nodeBase, dir string, policy plug.QueuePolicyValue) (plug.Broker, error) {
	cfg := plug.QueueConfig{Name: b.id, Policy: policy, Wrap: a.wrapQueue}
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
// handler's failure; a handler the app's stop cut short is none — its
// message is handed back uncounted (stoppedBy) —; one that said when to
// try again ([RetryIn]) is handed back by its queue with that delay
// (consumerRun.consume).
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
		c.loop.ended(a, sp, started, err)
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
//
// Past giveUp — more early ends than its Restarts within its Within — the
// supervisor gives the consumer up: its liveness check, "supervised:<node>",
// answers the supervisor's Check from then on (supervision.go).
func (c *consumer) supervise(consume func(context.Context) error, giveUp lifecycle.GiveUp) (*lifecycle.Supervisor, error) {
	a, loop := c.a, c.loop
	var lastErr error
	sup, err := lifecycle.NewSupervisor(loop.Name, func(ctx context.Context) error {
		return consume(a.asLoop(ctx, loop.Name))
	}, lifecycle.SupervisorConfig{
		Clock:        a.clock,
		Backoff:      loopBackoff,
		HealthyAfter: healthyRun,
		GiveUpAfter:  giveUp,
		Observe: func(e lifecycle.SupervisionEvent) {
			lastErr = c.observe(&e, giveUp, lastErr)
		},
	})
	if err != nil {
		return nil, err
	}
	if err := a.supervise(c.node, supervisorCheck(sup)); err != nil {
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
// restart, its giving up and the end of the supervision show. It returns
// the last run's error, which a run's end replaces and a restart reports.
func (c *consumer) observe(e *lifecycle.SupervisionEvent, giveUp lifecycle.GiveUp, lastErr error) error {
	a, loop := c.a, c.loop
	switch e.Phase {
	case lifecycle.SupervisionRunStarted:
		if e.Run > 1 {
			loop.restarted(a)
		}
	case lifecycle.SupervisionRunEnded:
		lastErr = e.Err
		c.ended(e)
	case lifecycle.SupervisionRestarting:
		logger.Warn(context.Background(), a.log, "a consumer stopped; restarting it", logger.String("node", c.node), logger.String("after", e.Delay.String()))
		loop.idle(a, e.At.Add(e.Delay))
		loop.setState(a, model.LoopRestarting)
		a.reportRestart(c.node, lastErr, e.Delay)
	case lifecycle.SupervisionGaveUp:
		a.gaveUp(loop, c.node, giveUp, e.Err)
	case lifecycle.SupervisionStopped:
		loop.setState(a, model.LoopStopped)
	default:
		// Every other phase changes nothing the Studio shows.
	}
	return lastErr
}

// ended draws a run of the consumer that ended: a failure while the app
// still serves is a problem, and its loop shows it.
func (c *consumer) ended(e *lifecycle.SupervisionEvent) {
	if e.Err == nil || e.Stopping {
		return
	}
	c.a.problem(c.node, say("consumer.failed", "node", c.node, "detail", errs.PublicOf(e.Err)))
	c.loop.failed(c.a, e.Err)
}

// defaultDeliveries are a consumer's delivery options until one is given:
// defaultMaxDeliveries attempts, one message at a time, and the default stop
// and lease (consumer_policy.go).
func defaultDeliveries() deliveryOptions {
	return deliveryOptions{maxDeliveries: defaultMaxDeliveries, parallelism: 1, knobs: defaultConsumerKnobs()}
}
