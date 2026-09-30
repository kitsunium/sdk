// Package kit — queued commands: a command run later, from its queue.
package kit

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/queue"
	"github.com/kitsunium/sdk/pkg/v1/trace"
)

// Queued commands (kit.Queued): Dispatch checks the command as its caller —
// its validate tags, its authorization, its key — and puts it in the
// command's own queue, the SDK's: a file queue under
// <data>/<service>/commands/<name>, memory without a data directory. A
// consumer, a loop of the daemon, handles it in the dispatcher's trace, as
// the user who dispatched it, the policies around the handling; a failure is
// retried after the queue's delay, then dead-lettered. Nothing of it exists
// for a command that is not queued.

// commandQueue is a queued command's queue in one run of the app.
type commandQueue struct {
	broker queue.Broker
	cancel context.CancelFunc
	done   chan struct{}

	mu sync.Mutex
	// pending are the keys queued or being handled: a dispatch with one of
	// them is not queued again.
	pending map[string]bool
}

// claim takes key for a dispatch about to be queued, and reports false
// when a dispatch with it waits or runs.
func (q *commandQueue) claim(key string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending[key] {
		return false
	}
	q.pending[key] = true
	return true
}

// release gives key back, once its dispatch is handled or dead-lettered, or
// could not be queued.
func (q *commandQueue) release(key string) {
	if key == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.pending, key)
}

// queuedEnvelope is what a queued command's queue carries: the input, and
// what its handling needs of its dispatch — the trace, the user, the node
// that dispatched it, the key. It is decoded as encoding/json decodes,
// unknown members ignored: a queued command's input is a contract with the
// messages already queued.
type queuedEnvelope[C any] struct {
	Trace string `json:"trace,omitempty"`
	User  string `json:"user,omitempty"`
	From  string `json:"from,omitempty"`
	Key   string `json:"key,omitempty"`
	Input C      `json:"input"`
}

// accept checks a dispatch of the queued command as its caller — the
// input's validate tags, its authorization, its key — and queues it. A
// dispatch whose key waits or runs is not queued again: it is answered as
// accepted, and its span says it was deduplicated.
func (c *Command[C, R]) accept(ctx context.Context, a *App, from string, in C) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = panicked(ctx, a, c.id, "command panicked at its dispatch", p)
		}
	}()
	q := c.queue.Load()
	if q == nil {
		return unmounted(&c.nodeBase)
	}
	if err := c.pipe.validate(in); err != nil {
		return err
	}
	if err := c.check(ctx, a, c.id, in); err != nil {
		return err
	}
	key, fresh, err := c.claim(ctx, a, q, in)
	if err != nil || !fresh {
		return err
	}
	if err := c.put(ctx, q, key, queuedEnvelope[C]{From: from, Key: key, Input: in}); err != nil {
		q.release(key)
		return err
	}
	return nil
}

// claim is the key of a dispatch about to be queued, taken; fresh is false
// when a dispatch with it waits or runs. A command without a key claims
// nothing and is always fresh.
func (c *Command[C, R]) claim(ctx context.Context, a *App, q *commandQueue, in C) (key string, fresh bool, err error) {
	if c.key == nil {
		return "", true, nil
	}
	if key, err = c.keyFor(ctx, a, in); err != nil {
		return "", false, err
	}
	switch {
	case key == "":
		return "", false, c.noKey()
	case holds(ctx, c, key):
		return "", false, reentrant(c)
	case !q.claim(key):
		markSpan(ctx, c.id, "deduplicated", "true")
		return key, false, nil
	}
	return key, true, nil
}

// put queues env, with the dispatch's trace and user — inside a
// transaction, encoded now and queued once it commits: a rollback drops it,
// and gives its key back.
func (c *Command[C, R]) put(ctx context.Context, q *commandQueue, key string, env queuedEnvelope[C]) error {
	env.Trace, _ = trace.FormatTraceParent(trace.SpanContextFromContext(ctx))
	if uid, ok := UserID(ctx); ok {
		env.User = string(uid)
	}
	payload, err := c.encode(ctx, c.app(), env)
	switch {
	case errs.HasCode(err, CodeSealWrite), errs.HasCode(err, CodeSealKey):
		return err
	case err != nil:
		return failure(CodeCommandEncode, "COMMAND_ENCODE", "the command cannot be queued", err, errs.String("command", c.id))
	}
	queue := func(ctx context.Context) error {
		if _, err := q.broker.Publish(ctx, payload); err != nil {
			return failure(CodeCommandQueue, "COMMAND_QUEUE", "the command's queue refused it", err, errs.String("command", c.id))
		}
		return nil
	}
	release := func() error {
		err := queue(withoutUnit(context.WithoutCancel(ctx)))
		if err != nil {
			q.release(key)
		}
		return err
	}
	if hold(ctx, heldEffect{node: c.id, release: release, drop: func() { q.release(key) }}) {
		markSpan(ctx, c.id, "held", "commit")
		return nil
	}
	return queue(ctx)
}

// handler is the consumer's: it restores the dispatch's trace and user,
// runs the command inside its node — the policies around its key and its
// handler — and reports the run to the consumer's loop. A dispatch handled,
// or dead-lettered after its last attempt, gives its key back.
func (c *Command[C, R]) handler(a *App, q *commandQueue, loop *loopState) queue.Handler {
	return func(ctx context.Context, d queue.Delivery) (err error) {
		env, decodeErr, openErr := c.decode(ctx, a, d.Message.Payload)
		if decodeErr == nil {
			ctx = env.restore(ctx)
		}
		loop.begin(a, model.WakeTopic)
		started := a.clock.Now()
		ctx, sp := c.handleSpan(ctx, a, &env, &d)
		defer func() {
			if p := recover(); p != nil {
				err = panicked(ctx, a, c.id, "handler panicked", p)
			}
			sp.end(err)
			loop.ran(a, started, a.clock.Now(), err)
			if err == nil || d.Deliveries >= c.opts.deliveries.maxDeliveries {
				q.release(env.Key)
			}
		}()
		switch {
		case openErr != nil:
			return openErr
		case decodeErr != nil:
			return failure(CodeUndecodable, "MESSAGE_UNDECODABLE", "the queued command does not decode into its input", decodeErr,
				errs.String("command", c.id), errs.String("message", d.Message.ID))
		}
		return c.handle(ctx, a, sp, env)
	}
}

// handleSpan begins the span of a handling: on the command, from the node
// that dispatched it — no edge: the dispatch drew it —, its delivery and
// its key.
func (c *Command[C, R]) handleSpan(ctx context.Context, a *App, env *queuedEnvelope[C], d *queue.Delivery) (context.Context, *span) {
	ctx, sp := a.begin(ctx, &spanStart{node: c.id, from: env.From, op: model.OpHandle, name: c.name})
	sp.attr("delivery", strconv.Itoa(d.Deliveries))
	if env.Key != "" {
		sp.attr("instance", clip(env.Key))
	}
	return ctx, sp
}

// handle runs a queued dispatch: the policies around its key and its
// handler.
func (c *Command[C, R]) handle(ctx context.Context, a *App, sp *span, env queuedEnvelope[C]) error {
	if sp.detailed() {
		sp.request(env.Input)
	}
	_, err := c.pipe.around(withCommand(ctx, c.id), a, c, env.Input, c.handling(a, env.Key))
	return err
}

// restore is the context of the handling: the dispatcher's trace, and its
// user.
func (qe queuedEnvelope[C]) restore(ctx context.Context) context.Context {
	if qe.Trace != "" {
		if sc, err := trace.ParseTraceParent(qe.Trace); err == nil {
			ctx = trace.ContextWithSpanContext(ctx, sc)
		}
	}
	if qe.User != "" {
		ctx = context.WithValue(ctx, authKey{}, principal{uid: UID(qe.User)})
	}
	return ctx
}

// handling are a queued command's steps at its handling: its key, then its
// transaction (ADR 0004), around its handler.
func (c *Command[C, R]) handling(a *App, key string) steps[C] {
	if key == "" && c.opts.noTransaction {
		return nil
	}
	return func(ctx context.Context, _ C, handle func(context.Context) error) error {
		run := c.transacted(a, handle)
		if key == "" {
			return run(ctx)
		}
		return c.keys.run(ctx, a, c, key, run)
	}
}

// start opens a queued command's queue and starts its consumer; a command
// that is not queued starts nothing, and is not a component of the app.
//
// Goroutine lifecycle: one goroutine consumes the queue until stop cancels its
// context; it closes q.done, which stop waits for.
func (c *Command[C, R]) start(_ context.Context, a *App) error {
	if !c.opts.queued {
		return nil
	}
	broker, err := c.open(a)
	if err != nil {
		return failure(CodeCommandQueue, "COMMAND_QUEUE", "a queued command's queue cannot be opened", err, errs.String("command", c.id))
	}
	q := &commandQueue{broker: broker, pending: map[string]bool{}, done: make(chan struct{})}
	loop := a.loop(c.id+" consumer", c.id, model.LoopConsumer, consumerHow)
	ctx, cancel := context.WithCancel(context.WithoutCancel(a.baseCtx))
	q.cancel = cancel
	c.queue.Store(q)
	go func() {
		done := q.done
		defer close(done)
		c.consume(a.asLoop(ctx, loop.Name), a, q, loop)
	}()
	return nil
}

// open opens the command's queue: a file queue under the data directory,
// memory without one; a subscription's lease and retry delay.
func (c *Command[C, R]) open(a *App) (queue.Broker, error) {
	return a.openQueue(filepath.Join(c.svc.name, "commands", c.name), c.opts.deliveries.maxDeliveries)
}

// consume runs the consumer until ctx ends. One at a time per worker: a
// worker handles its batch in turn, so a batch of several would hold
// dispatches behind a slow one — their leases lapsing — and Parallelism
// would not say how many run.
func (c *Command[C, R]) consume(ctx context.Context, a *App, q *commandQueue, loop *loopState) {
	err := queue.Consume(ctx, q.broker, queue.ConsumerConfig{
		Handler:             c.handler(a, q, loop),
		Clock:               a.clock,
		PollInterval:        pollInterval,
		Parallelism:         c.opts.deliveries.parallelism,
		BatchSize:           1,
		HandlerIsIdempotent: true,
	})
	if err != nil && ctx.Err() == nil {
		a.problem(c.id, say("command.stopped", "command", c.id, "detail", errs.PublicOf(err)))
		loop.died(a, err)
	}
}

// stop ends a queued command's consumer; what waits in a file queue is
// handled at the next start.
func (c *Command[C, R]) stop(ctx context.Context, _ *App) error {
	q := c.queue.Load()
	if q == nil || q.cancel == nil {
		return nil
	}
	q.cancel()
	select {
	case <-q.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// describeQueue fills what a queued command's node says of its queue: its
// attempts, its parallelism, where it waits and, running, its dead letters.
func (c *Command[C, R]) describeQueue(a *App, info *model.CommandInfo) {
	d := c.opts.deliveries
	info.Mode, info.MaxDeliveries, info.Parallelism, info.Queue = model.ModeQueued, d.maxDeliveries, d.parallelism, queueOf(a)
	q := c.queue.Load()
	if q == nil || a == nil || !a.running() {
		return
	}
	if r, ok := q.broker.(queue.DeadLetterReader); ok {
		if dl, err := r.DeadLetters(context.Background(), 1000); err == nil {
			info.DeadLetters = new(len(dl))
		}
	}
}

// queuePlace says where a queue of each kind waits, in a mechanic's label.
var queuePlace = map[string]string{"file": "on disk", "memory": "in memory"}

// queueOf is where a's queues are: "file" under a data directory,
// "memory" without one.
func queueOf(a *App) string {
	if a != nil && a.dataDir != "" {
		return "file"
	}
	return "memory"
}

// queueMechanic is a queued command's queue, among the steps of its
// pipeline: where it waits, and how many attempts it gets.
func (c *Command[C, R]) queueMechanic(a *App) model.Mechanic {
	d := c.opts.deliveries
	where := queueOf(a)
	return model.Mechanic{
		Kind: "queue", Label: fmt.Sprintf("%s · %d attempts", queuePlace[where], d.maxDeliveries), Package: "github.com/kitsunium/sdk/pkg/v1/queue",
		Config: map[string]string{"queue": where, "maxDeliveries": strconv.Itoa(d.maxDeliveries), "parallelism": strconv.Itoa(d.parallelism)},
	}
}
