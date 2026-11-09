package batcher

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Sink delivers one coalesced batch. A non-nil error fails the batch: it is
// returned to the caller of Flush / Close and routed to Config.OnError on the
// background ticker path.
type Sink[T any] func(ctx context.Context, batch []T) error

// Batcher coalesces Add'd items into batches delivered through its Sink.
// It owns its own flush ticker and guards the pending batch with a mutex, so
// Add / Flush / Close are safe under concurrent callers (ADR 0014).
type Batcher[T any] struct {
	// deliver is the batch delivery seam supplied at construction.
	deliver Sink[T]
	// cfg holds the caps, weight function, interval and error hook.
	cfg Config[T]

	// mu guards buf + weight + closed + ready + room against Add, Flush, Close
	// and the batcher's goroutine.
	mu     sync.Mutex
	buf    []T
	weight int64
	closed bool
	// ready is the batch a cap filled under FlushBackground, waiting for the
	// batcher's goroutine to take it; nil when none is. room is opened when it
	// is taken, waking the producers that found it occupied.
	ready []T
	room  *gate

	// deliverMu serializes Sink invocations (V6) so two flush paths never
	// enter the deliver closure concurrently. It is held only across the call
	// and is deliberately separate from mu so a slow Sink never blocks a
	// producer appending into the next batch.
	deliverMu sync.Mutex
	// freed carries "deliverMu was released" to the waits that must be able
	// to give up — the batcher's goroutine and Close —, counted in bounded;
	// a release sends only while one is counted, so the common path pays an
	// atomic load. One buffered slot.
	freed   chan struct{}
	bounded atomic.Int32

	// life is the context of every delivery the batcher's own goroutine makes —
	// the ticker's and FlushBackground's —, cancelled by Close: when Close's
	// context ends before the goroutine has stopped, and on every Close's exit.
	life       context.Context
	lifeCancel context.CancelFunc

	// kick tells the batcher's goroutine a batch is ready. One buffered slot:
	// a second kick before the first is taken has nothing more to say.
	kick chan struct{}

	// stop/done drive the batcher's goroutine; the Once guards make the
	// channel closes safe under concurrent Close calls. looping is set when
	// NewBatcher started it: a FlushEvery ticker, FlushBackground, or both.
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	doneOnce sync.Once
	looping  bool
	// busy is set while the batcher's goroutine is in a delivery — waiting for
	// the Sink or inside it —, the one state in which joining it may wait on
	// a Sink, and the one Close bounds by its context.
	busy atomic.Bool
}

// NewBatcher builds a Batcher over deliver with cfg. When cfg.FlushEvery > 0
// or cfg.FlushOn is FlushBackground it spawns the batcher's goroutine, which
// flushes on the interval, ticking on cfg.Clock (the wall clock when nil),
// and delivers the batches a cap made ready; Close joins it. Every delivery
// that goroutine makes runs under the batcher's lifetime context, which
// Close cancels. A nil cfg.OnError is degraded to a no-op so the flush paths
// stay branch-free.
func NewBatcher[T any](deliver Sink[T], cfg Config[T]) *Batcher[T] {
	//: degrade a nil error hook to a no-op so background flush stays branch-free.
	if cfg.OnError == nil {
		//: discard ticker-flush errors when the caller wired no observer.
		cfg.OnError = func(error) {}
	}
	//: no clock named: the wall clock, the only non-arbitrary default.
	if cfg.Clock == nil {
		cfg.Clock = clock.System
	}
	life, lifeCancel := context.WithCancel(context.Background())
	b := &Batcher[T]{
		deliver:    deliver,
		cfg:        cfg,
		freed:      make(chan struct{}, 1),
		life:       life,
		lifeCancel: lifeCancel,
		kick:       make(chan struct{}, 1),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	//: a goroutine only for a positive interval or a background delivery.
	if cfg.FlushEvery > 0 || cfg.FlushOn == FlushBackground {
		//: mark looping so Close knows to join the goroutine.
		b.looping = true
		//: background flusher bounds how long a partial batch waits.
		go b.loop(cfg.FlushEvery)
	}
	//: hand back the constructed batcher.
	return b
}

// Add appends item to the pending batch and eagerly flushes when the batch
// reaches MaxItems or MaxWeight. It returns BatcherClosed if Close has run.
// Under FlushCaller the capped batch is delivered on this goroutine and a
// deliver failure is returned wrapped as BatcherDeliverFailed; under
// FlushBackground it is handed to the batcher's goroutine and Add returns
// nil — waiting first, within ctx, while an earlier capped batch has not
// been taken, the item staying pending when ctx ends.
func (b *Batcher[T]) Add(ctx context.Context, item T) error {
	//: append under the lock; the ticker / Flush share the pending batch.
	b.mu.Lock()
	//: reject Add after Close so no item is silently dropped.
	if b.closed {
		b.mu.Unlock()
		//: surface the closed-batcher sentinel to the caller.
		return BatcherClosed
	}
	b.buf = append(b.buf, item)
	b.weight += b.weigh(item)
	//: under cap — the item waits for the next flush.
	if !b.full() {
		b.mu.Unlock()
		//: accepted.
		return nil
	}
	//: a cap was hit: FlushBackground hands the batch to the batcher's goroutine.
	if b.cfg.FlushOn == FlushBackground {
		//: handOff releases mu.
		return b.handOff(ctx)
	}
	b.mu.Unlock()
	//: FlushCaller: deliver on this goroutine, the failure to this caller.
	return b.flushOnce(ctx)
}

// handOff is FlushBackground's cap path, entered with mu held and the pending
// batch at a cap, and returns with it released: the batch becomes the ready
// one and the batcher's goroutine is kicked — or, while a ready batch is
// still waiting, the producer waits for it to be taken, so no more than one
// batch waits beside the one being delivered. The item is accepted either
// way: a producer whose ctx ends while it waits leaves it pending, delivered
// with the next batch.
func (b *Batcher[T]) handOff(ctx context.Context) error {
	//: until the batch is handed over, or no longer full.
	for {
		//: the slot is free: the batch is the ready one.
		if b.ready == nil {
			b.ready, b.buf, b.weight = b.buf, nil, 0
			b.room = newGate()
			b.mu.Unlock()
			select {
			case b.kick <- struct{}{}:
				//: the batcher's goroutine will take it.
			default:
				//: a kick is already pending.
			}
			//: accepted, and handed over.
			return nil
		}
		room := b.room.ch
		b.mu.Unlock()
		select {
		case <-room:
			//: the ready batch was taken: look again.
		case <-ctx.Done():
			//: the producer stops waiting; its item stays pending.
			return nil
		}
		b.mu.Lock()
		//: Close took the batch, or a producer before this one handed it over.
		if b.closed || !b.full() {
			b.mu.Unlock()
			//: accepted.
			return nil
		}
	}
}

// Flush delivers the pending batch synchronously, returning any deliver error.
// It returns BatcherClosed if Close has run.
func (b *Batcher[T]) Flush(ctx context.Context) error {
	//: take what is pending — a ready batch first — under the lock.
	b.mu.Lock()
	//: a closed batcher has nothing more to deliver.
	if b.closed {
		b.mu.Unlock()
		//: surface the closed-batcher sentinel to the caller.
		return BatcherClosed
	}
	ready := b.takeReady()
	batch := b.take()
	b.mu.Unlock()
	//: nothing pending.
	if len(ready)+len(batch) == 0 {
		//: empty-batch fast path.
		return nil
	}
	//: caller-facing flush propagates the error rather than routing to OnError.
	b.deliverMu.Lock()
	defer b.unlockDeliver()
	//: the ready batch is the older one: it is delivered first.
	return either(b.deliverBatch(ctx, ready), b.deliverBatch(ctx, batch))
}

// Close stops the batcher's goroutine (if any), delivers the final batch
// with ctx — the caller's context, which bounds the whole of Close — and
// marks the batcher closed. The goroutine is joined within ctx: when ctx
// ends while it is still inside a delivery, Close cancels that delivery's
// context and returns BatcherCloseTimedOut without waiting further, the
// final batch undelivered; a Sink that ignores its context keeps the
// goroutine until it returns. It is idempotent: a second Close returns nil
// after the first has drained the buffer.
func (b *Batcher[T]) Close(ctx context.Context) error {
	//: every exit releases the lifetime context: no delivery of the batcher's
	//: own outlives Close.
	defer b.lifeCancel()
	//: join the batcher's goroutine first so it cannot race the final flush.
	joinErr := b.join(ctx)
	//: flip closed and capture the final batch atomically under the lock so a
	//: concurrent Add either lands in this batch or is rejected — never stranded.
	b.mu.Lock()
	b.closed = true
	ready := b.takeReady()
	batch := b.take()
	b.mu.Unlock()
	undelivered := len(ready) + len(batch)
	//: the goroutine outlived ctx: report what is left, delivered to nobody.
	if joinErr != nil {
		//: the timed-out join, with what it left undelivered.
		return errs.Wrap(joinErr, BatcherCloseTimedOut.Params(), errs.Int("undelivered", undelivered))
	}
	//: nothing buffered — closed-flag flip is the only work to do.
	if undelivered == 0 {
		//: empty-batch fast path (also the idempotent second-Close path).
		return nil
	}
	//: the Sink may still be held by a caller's flush: wait for it within ctx.
	if !b.acquire(ctx) {
		//: ctx ended behind another delivery: report what is left.
		return errs.Wrap(ctx.Err(), BatcherCloseTimedOut.Params(), errs.Int("undelivered", undelivered))
	}
	defer b.unlockDeliver()
	//: deliver the captured final batches outside the lock, the ready one first.
	return either(b.deliverBatch(ctx, ready), b.deliverBatch(ctx, batch))
}

// join stops the batcher's goroutine and waits for it within ctx. When ctx
// ends first it cancels the lifetime context — a Sink that honours its context
// returns, and the goroutine exits after it — and returns ctx's error without
// waiting further.
func (b *Batcher[T]) join(ctx context.Context) error {
	//: no goroutine was started.
	if !b.looping {
		//: nothing to join.
		return nil
	}
	//: idempotent stop signal so a double Close never panics.
	b.stopOnce.Do(func() { close(b.stop) })
	//: an exited goroutine is joined whatever state ctx is in.
	select {
	case <-b.done:
		//: already gone.
		return nil
	default:
	}
	select {
	case <-b.done:
		//: the goroutine has exited.
		return nil
	case <-ctx.Done():
	}
	//: ctx is over: no delivery of the goroutine's may start or go on.
	b.lifeCancel()
	//: idle, it sees stop at once — and a delivery it begins now gives up on
	//: the cancelled context before reaching the Sink.
	if !b.busy.Load() {
		<-b.done
		//: joined.
		return nil
	}
	//: inside a delivery: the join is abandoned; ctx's error says why.
	return ctx.Err()
}

// acquire takes deliverMu, or reports false when ctx ends first; a free
// mutex is taken whatever state ctx is in. Between two attempts it waits on
// freed, which every release signals while a bounded wait is counted.
func (b *Batcher[T]) acquire(ctx context.Context) bool {
	//: free: taken at once.
	if b.deliverMu.TryLock() {
		return true
	}
	//: counted BEFORE the next attempt, so a release after it signals.
	b.bounded.Add(1)
	defer b.bounded.Add(-1)
	for {
		//: freed since the last look: taken.
		if b.deliverMu.TryLock() {
			return true
		}
		select {
		case _, open := <-b.freed:
			//: never closed; a closed one would only spin the wait.
			if !open {
				return false
			}
			//: released: look again.
		case <-ctx.Done():
			//: ctx ended behind another delivery.
			return false
		}
	}
}

// unlockDeliver releases deliverMu and, while a bounded wait is counted,
// tells it so.
func (b *Batcher[T]) unlockDeliver() {
	b.deliverMu.Unlock()
	//: nobody waits with a deadline: the mutex's own queue wakes the rest.
	if b.bounded.Load() == 0 {
		return
	}
	select {
	case b.freed <- struct{}{}:
		//: a bounded waiter looks again.
	default:
		//: a signal is already pending.
	}
}

// take swaps out the pending batch. The caller holds mu.
func (b *Batcher[T]) take() []T {
	batch := b.buf
	b.buf = nil
	b.weight = 0
	//: a nil buf starts a fresh backing array.
	return batch
}

// takeReady takes the ready batch, if any, and wakes the producers waiting
// for it to go. The caller holds mu.
func (b *Batcher[T]) takeReady() []T {
	batch := b.ready
	//: a ready batch leaves: its room is free.
	if batch != nil {
		b.ready = nil
		b.room.open()
	}
	//: nil when none was waiting.
	return batch
}

// flushOnce swaps out the pending batch under the lock and delivers it outside
// the lock. An empty batch is a no-op; a deliver error is wrapped as
// DeliverFailed with the cause reachable via errors.Is.
func (b *Batcher[T]) flushOnce(ctx context.Context) error {
	//: take ownership of the pending batch under the lock.
	b.mu.Lock()
	batch := b.take()
	b.mu.Unlock()
	//: nothing buffered — report success.
	if len(batch) == 0 {
		//: empty-batch fast path.
		return nil
	}
	//: serialize the Sink so two flush paths never invoke it concurrently (V6).
	b.deliverMu.Lock()
	defer b.unlockDeliver()
	//: deliver outside the lock so a slow Sink never blocks producers.
	return b.deliverBatch(ctx, batch)
}

// deliverBatch runs the Sink on batch and wraps a non-nil result as the typed
// DeliverFailed sentinel (the cause stays reachable via errors.Is). The caller
// holds deliverMu (V6), so a non-reentrant Sink stays safe. An empty batch is
// not delivered.
func (b *Batcher[T]) deliverBatch(ctx context.Context, batch []T) error {
	//: nothing to hand over: a ready slot or a pending batch that was empty.
	if len(batch) == 0 {
		//: no delivery, no error.
		return nil
	}
	//: run the Sink; a slow delivery never holds b.mu, only deliverMu.
	if derr := b.deliver(ctx, batch); derr != nil {
		//: wrap the cause as the typed DeliverFailed sentinel (errors.Is reaches it).
		return errs.Wrap(derr, BatcherDeliverFailed.Params())
	}
	//: happy path — batch delivered.
	return nil
}

// gate is a one-shot broadcast: opened once, it wakes every goroutine
// waiting on ch, and every later one at once.
type gate struct {
	// once makes the close of ch happen once, whoever opens the gate.
	once sync.Once
	// ch is closed when the gate opens.
	ch chan struct{}
}

// newGate returns a closed gate's opposite: one nobody has opened.
func newGate() *gate {
	//: a fresh channel, closed by open alone.
	return &gate{ch: make(chan struct{})}
}

// open opens the gate; a second call is a no-op.
func (g *gate) open() {
	g.once.Do(func() { close(g.ch) })
}

// either returns the one failure of two deliveries as it is, and both joined
// when both failed: a single failure keeps its own type for errors.As and a
// caller comparing it.
func either(first, second error) error {
	//: the first delivery succeeded: the second's verdict alone.
	if first == nil {
		return second
	}
	//: the second succeeded: the first's verdict alone.
	if second == nil {
		return first
	}
	//: both failed: both reported.
	return errors.Join(first, second)
}

// loop is the batcher's goroutine: it delivers on every FlushEvery tick and,
// under FlushBackground, every batch a cap made ready, until Close signals
// stop. Every delivery runs under the lifetime context and reports its
// failure to OnError.
func (b *Batcher[T]) loop(every time.Duration) {
	//: signal Close that the goroutine has fully exited (guarded for safety).
	defer b.doneOnce.Do(func() { close(b.done) })
	//: no interval: a nil channel, which never ticks.
	var ticks <-chan time.Time
	//: periodic flusher bounds how long a partial batch waits, on the
	//: configured clock so a test advances it instead of sleeping.
	if every > 0 {
		t := b.cfg.Clock.NewTicker(every)
		defer t.Stop()
		//: bound once: the ticker's channel is never closed (clock.Ticker).
		ticks = t.C()
	}
	//: deliver on every tick and every kick; exit promptly on stop.
	for {
		select {
		//: interval elapsed — flush whatever has accumulated, ready batch first.
		case <-ticks:
			b.background(true)
		//: a cap made a batch ready.
		case _, open := <-b.kick:
			//: never closed; a closed one would only spin the loop.
			if !open {
				return
			}
			b.background(false)
		//: Close requested — let Close perform the final flush.
		case <-b.stop:
			//: terminal exit; deferred close(done) unblocks Close.
			return
		}
	}
}

// background is one delivery of the batcher's goroutine: the ready batch,
// and on a tick the pending one too, under the lifetime context. It takes the
// Sink BEFORE the batches, and gives up when Close cancels the lifetime
// context while it waits — what it did not take stays pending for Close.
func (b *Batcher[T]) background(tick bool) {
	b.busy.Store(true)
	defer b.busy.Store(false)
	//: Close gave up on this goroutine, before or while it waited: leave the
	//: batches to Close.
	if b.life.Err() != nil || !b.acquire(b.life) {
		return
	}
	defer b.unlockDeliver()
	b.mu.Lock()
	ready := b.takeReady()
	var batch []T
	//: a kick delivers the ready batch alone.
	if tick {
		batch = b.take()
	}
	b.mu.Unlock()
	//: route a background failure to the observer.
	if ferr := either(b.deliverBatch(b.life, ready), b.deliverBatch(b.life, batch)); ferr != nil {
		//: surface the background failure via the configured hook.
		b.cfg.OnError(ferr)
	}
}

// weigh reports item's contribution to the batch weight: WeightOf when set,
// else 1 for count-only batching.
func (b *Batcher[T]) weigh(item T) int64 {
	//: a nil WeightOf means count-only — every item weighs one slot.
	if b.cfg.WeightOf == nil {
		//: count-only weight.
		return 1
	}
	//: caller-supplied weight (e.g. byte size).
	return b.cfg.WeightOf(item)
}

// full reports whether the pending batch has reached an eager-flush cap: the
// item-count cap (MaxItems>0) OR the weight cap (WeightOf set and MaxWeight>0).
// The caller holds b.mu.
func (b *Batcher[T]) full() bool {
	//: the item-count cap fires when MaxItems is positive and reached.
	countCap := b.cfg.MaxItems > 0 && len(b.buf) >= b.cfg.MaxItems
	//: the weight cap fires only when a WeightOf and positive MaxWeight exist.
	weightCap := b.cfg.WeightOf != nil && b.cfg.MaxWeight > 0 && b.weight >= b.cfg.MaxWeight
	//: either cap crossing forces an eager flush.
	return countCap || weightCap
}
