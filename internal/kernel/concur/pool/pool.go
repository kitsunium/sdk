package pool

import (
	"context"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Task is one unit of work a Pool runs: it receives the pool's context,
// cancelled by Stop, and its error goes to Config.OnError.
type Task func(ctx context.Context) error

// Pool is a fixed set of long-lived workers consuming a bounded queue of
// tasks. Submit hands a task over — refused with PoolFull when there is no
// room —, Drain stops accepting and runs what is queued, Stop abandons the
// queue and cancels what runs. A task's panic is recovered on its worker and
// handed to Config.OnPanic, and the worker serves the next task. Safe for
// concurrent use; the zero value is NOT usable, build it with NewPool.
type Pool struct {
	// tasks carries submitted tasks to the workers: unbuffered when the pool
	// has no queue, so a send lands only on an idle worker. It is never
	// closed: stopped tells the workers the sends are over.
	tasks chan Task
	// queued reports whether tasks is buffered: a full queue is waited on,
	// within the submitter's context, instead of refused at once.
	queued bool
	// ctx is every task's context; cancel ends it, on Stop.
	ctx    context.Context
	cancel context.CancelFunc
	// onPanic and onError observe what a task's worker cannot return.
	onPanic func(PanicValue)
	onError func(err error)
	// mu orders a send on tasks against the end of the sends: Submit sends
	// under the read lock with closed false, and closing sets closed under the
	// write lock before closing stopped, so once a worker sees stopped no task
	// will ever be sent again and what it finds queued is all there is.
	mu     sync.RWMutex
	closed bool
	// quit is closed when the pool stops accepting, waking a Submit waiting
	// for room so that closing never waits on a submitter's context; stopped
	// is closed after the last send, telling the workers to empty the queue
	// and leave. closeOnce makes the two closes once.
	quit      chan struct{}
	stopped   chan struct{}
	closeOnce sync.Once
	// abandon is set by Stop: a worker drops each task it takes from then on.
	abandon atomic.Bool
	// workers counts the running workers; done is closed, once, when the last
	// one is out.
	workers  sync.WaitGroup
	done     chan struct{}
	doneOnce sync.Once
}

// NewPool starts a Pool of cfg.Workers workers — at least one — over a queue
// of cfg.Queue tasks, its tasks running under a context derived from ctx: the
// values ctx carries, its cancellation, and Stop's. Its goroutines — the
// workers and one that waits for them — live until Drain or Stop, which join
// them within their own context.
func NewPool(ctx context.Context, cfg Config) *Pool {
	workers := max(cfg.Workers, 1)
	queue := max(cfg.Queue, 0)
	taskCtx, cancel := context.WithCancel(ctx)
	p := &Pool{
		tasks:   make(chan Task, queue),
		queued:  queue > 0,
		ctx:     taskCtx,
		cancel:  cancel,
		onPanic: cfg.OnPanic,
		onError: cfg.OnError,
		quit:    make(chan struct{}),
		stopped: make(chan struct{}),
		done:    make(chan struct{}),
	}
	//: no observer: a task's error is discarded, as batcher's OnError does.
	if p.onError == nil {
		p.onError = func(error) {}
	}
	p.workers.Add(workers)
	//: the workers live until the sends are over and the queue is empty.
	for range workers {
		go p.work()
	}
	//: the last worker out closes done, the join point Drain and Stop wait on.
	go func() {
		p.workers.Wait()
		p.doneOnce.Do(func() { close(p.done) })
	}()
	//: running, accepting.
	return p
}

// Submit hands task to the pool. Without a queue it is taken by an idle
// worker or refused with PoolFull at once — never parked. With one, a full
// queue is waited on until ctx ends, which returns PoolFull wrapping ctx's
// error. After Drain or Stop it returns PoolClosed. A task accepted runs
// exactly once, unless Stop abandons it first. A nil task panics.
func (p *Pool) Submit(ctx context.Context, task Task) error {
	//: a nil task would panic on a worker, far from the call that made the mistake.
	if task == nil {
		panic("internal/kernel/concur/pool: Submit of a nil task")
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	//: closing has begun: nothing more is taken.
	if p.closed {
		return PoolClosed
	}
	select {
	case p.tasks <- task:
		//: an idle worker, or a free slot of the queue.
		return nil
	default:
	}
	//: no queue: a refusal, never a park (ADR 0031).
	if !p.queued {
		return PoolFull
	}
	select {
	case p.tasks <- task:
		//: a slot freed.
		return nil
	case <-p.quit:
		//: closing began while this Submit waited.
		return PoolClosed
	case <-ctx.Done():
		//: the submitter's bound, before a slot freed.
		return errs.Wrap(ctx.Err(), PoolFull.Params())
	}
}

// Drain stops accepting tasks, lets the workers run every task already
// queued, and waits for them to exit within ctx: nil once they have, ctx's
// error when it ends first — the workers then go on until the queue is
// empty; Stop abandons it. Idempotent.
func (p *Pool) Drain(ctx context.Context) error {
	p.close()
	select {
	case <-p.done:
		//: every queued task ran and every worker exited.
		return nil
	case <-ctx.Done():
		//: the workers outlive ctx: the caller decides whether to Stop.
		return ctx.Err()
	}
}

// Stop stops accepting tasks, abandons those still queued — they never run
// —, cancels the context the running ones received, and waits for them
// within ctx: nil once every worker has exited, ctx's error when it ends
// first. A task that ignores its context cannot be stopped from outside.
// Idempotent.
func (p *Pool) Stop(ctx context.Context) error {
	p.abandon.Store(true)
	p.cancel()
	p.close()
	select {
	case <-p.done:
		//: every worker exited.
		return nil
	case <-ctx.Done():
		//: a task ignores its cancelled context.
		return ctx.Err()
	}
}

// close stops accepting: quit wakes a Submit waiting for room, the write
// lock waits out every Submit inside its send, and stopped then tells the
// workers that what is queued is all there will be. Idempotent.
func (p *Pool) close() {
	p.closeOnce.Do(func() {
		close(p.quit)
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		close(p.stopped)
	})
}

// work is one worker: it runs the tasks it receives until the sends are
// over, then empties the queue and leaves — dropping the tasks unrun once
// Stop has abandoned the queue.
func (p *Pool) work() {
	defer p.workers.Done()
	//: until the sends are over.
	for task, ok := p.next(); ok; task, ok = p.next() {
		p.serve(task)
	}
	//: what was queued before the end of the sends, all of it.
	for task, ok := p.leftover(); ok; task, ok = p.leftover() {
		p.serve(task)
	}
}

// next waits for a task, or reports false once the sends are over.
func (p *Pool) next() (Task, bool) {
	select {
	case task := <-p.tasks:
		//: a task to run.
		return task, true
	case <-p.stopped:
		//: no task will be sent any more.
		return nil, false
	}
}

// leftover takes a task left in the queue without waiting, or reports false
// when the queue is empty.
func (p *Pool) leftover() (Task, bool) {
	select {
	case task := <-p.tasks:
		//: one more queued task.
		return task, true
	default:
		//: empty, and nothing more will come.
		return nil, false
	}
}

// serve runs task, unless Stop has abandoned the queue.
func (p *Pool) serve(task Task) {
	//: Stop abandoned the queue: what is left is not run.
	if p.abandon.Load() {
		return
	}
	p.run(task)
}

// run runs one task on this worker, recovering its panic into a PanicValue
// for OnPanic so the worker serves the next task. With no OnPanic the panic
// is raised again: a fault nobody observes is not swallowed.
func (p *Pool) run(task Task) {
	defer func() {
		raised := recover()
		//: the task returned.
		if raised == nil {
			return
		}
		pv := PanicValue{Raised: raised, Stack: debug.Stack()}
		//: nobody watches: the fault takes the process down, as a bare goroutine's would.
		if p.onPanic == nil {
			panic(pv)
		}
		p.onPanic(pv)
	}()
	//: a task's failure has no caller to return to.
	if err := task(p.ctx); err != nil {
		p.onError(err)
	}
}

// PanicValue carries a panic a task raised to Config.OnPanic: Raised is the
// value the panic carried, verbatim, and Stack the stack of the worker that
// ran the task, captured at recovery. It is not an error and carries no code:
// a panic is a programming fault.
type PanicValue struct {
	// Raised is the value the original panic carried, verbatim.
	Raised any
	// Stack is the stack of the worker goroutine, captured at recovery.
	Stack []byte
}

// String renders the original panic followed by the stack of the worker
// that raised it, so a re-raise with no OnPanic prints the task's stack.
func (p PanicValue) String() string {
	var out strings.Builder
	out.WriteString("kernel/concur/pool: panic in task: ")
	//: the shapes panic payloads take, without fmt in a kernel package.
	switch raised := p.Raised.(type) {
	case string:
		out.WriteString(raised)
	case error:
		out.WriteString(raised.Error())
	case interface{ String() string }:
		out.WriteString(raised.String())
	default:
		out.WriteString("non-textual panic value (see PanicValue.Raised)")
	}
	out.WriteString("\noriginating goroutine stack:\n")
	out.Write(p.Stack)
	//: the assembled message.
	return out.String()
}
