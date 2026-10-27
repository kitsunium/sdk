package server

import (
	"context"
	"errors"
	"math/bits"
	stdnet "net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// defaultOutboundDepth is LoopValue.Outbound's default: how many jobs one
	// lane's queue to its worker holds.
	defaultOutboundDepth int = 256
	// postRetry is how long a worker waits before it posts a completion again
	// to a loop whose post queue was full.
	postRetry time.Duration = time.Millisecond
)

// jobRing is a single-producer single-consumer ring of jobs, preallocated:
// head is written by the consumer only, tail by the producer only, and each
// reads the other's with an atomic load, so neither takes a lock.
type jobRing struct {
	// jobs is the storage, a power of two long.
	jobs []corenet.LoopJobValue
	// mask is len(jobs) less one.
	mask uint64
	// head is the next job to pop.
	head atomic.Uint64
	// tail is the next free place.
	tail atomic.Uint64
}

// newJobRing allocates a ring of at least n jobs.
func newJobRing(n int) *jobRing {
	size := 1 << bits.Len(uint(max(n, 1)-1))
	//: allocated once, never grown.
	return &jobRing{jobs: make([]corenet.LoopJobValue, size), mask: uint64(size - 1)}
}

// push appends job and reports false when the ring is full. Producer only.
func (r *jobRing) push(job corenet.LoopJobValue) bool {
	t := r.tail.Load()
	//: full: the consumer has not freed a place.
	if t-r.head.Load() == uint64(len(r.jobs)) {
		//: refused, nothing written.
		return false
	}
	r.jobs[t&r.mask] = job
	r.tail.Store(t + 1)
	//: queued.
	return true
}

// pop removes the oldest job and reports false when the ring is empty.
// Consumer only.
func (r *jobRing) pop() (corenet.LoopJobValue, bool) {
	h := r.head.Load()
	//: empty.
	if h == r.tail.Load() {
		//: nothing.
		return corenet.LoopJobValue{}, false
	}
	job := r.jobs[h&r.mask]
	r.head.Store(h + 1)
	//: the oldest.
	return job, true
}

// outboundLane is one lane's port to its worker (R3): the jobs the lane's
// thread sends, the worker's completions on their way back, and the worker
// goroutine itself.
type outboundLane struct {
	// lane is the lane's index.
	lane int
	// key is the lane's index in outboundLanes, the integer its completion
	// op is posted with.
	key uintptr
	// jobs carries jobs from the lane's thread to the worker.
	jobs *jobRing
	// done carries completions from the worker back to the lane's thread.
	done *jobRing
	// io is the lane's LoopIO, set by the first Send: what the worker posts
	// completions through.
	io atomic.Pointer[corenet.LoopIO]
	// wake rouses the worker; one token is enough.
	wake chan struct{}
	// posted is true from a posted completion op to its run.
	posted atomic.Bool
	// stopped ends the worker once it has finished its job.
	stopped atomic.Bool
	// refused counts the Sends a full queue refused.
	refused *atomic.Uint64
	// off is the offloader the completions return to.
	off corenet.LoopOffloader
}

// Send implements corenet.LoopOutbound: it queues job for the lane's worker
// and wakes it, or refuses with LoopOutboundFull and counts the refusal.
func (l *outboundLane) Send(io corenet.LoopIO, job corenet.LoopJobValue) error {
	//: the lane's LoopIO, kept once for the worker's completions.
	if l.io.Load() == nil {
		l.io.Store(&io)
	}
	//: a full queue refuses, never blocks the lane.
	if !l.jobs.push(job) {
		l.refused.Add(1)
		//: refused.
		return corenet.LoopOutboundFull
	}
	select {
	case l.wake <- struct{}{}:
	default:
	}
	//: queued.
	return nil
}

// run is the worker: it pops each job, runs it, and hands it back to the
// lane, until stopped.
//
// Goroutine lifecycle: one per lane, started by startOutbound and owned by
// the group's outbound; it exits once stop is set, after the job it runs, and
// its exit is what stop waits for.
func (l *outboundLane) run(w corenet.LoopWorker, done *sync.WaitGroup) {
	defer done.Done()
	//: until stopped.
	for !l.stopped.Load() {
		job, ok := l.jobs.pop()
		//: nothing queued: wait for a Send or the stop.
		if !ok {
			<-l.wake
			continue
		}
		w.Work(l.lane, job)
		l.complete(job)
	}
}

// complete hands job back to the lane's thread: into the done ring, then one
// post of the completion op when the ring was idle. A full ring, or a full
// post queue, is waited out — the worker may block, the lane may not; a
// stopped loop ends the wait.
func (l *outboundLane) complete(job corenet.LoopJobValue) {
	//: the ring frees as the lane drains it.
	for !l.done.push(job) {
		//: the lane is gone: the completion goes nowhere.
		if l.stopped.Load() {
			//: dropped.
			return
		}
		time.Sleep(postRetry)
	}
	//: an op already posted drains this one too.
	if !l.posted.CompareAndSwap(false, true) {
		//: on its way.
		return
	}
	io := l.io.Load()
	//: posted until the loop takes it, or stops.
	for {
		err := (*io).Post(runCompletions, l.key)
		//: queued, or the loop has stopped.
		if err == nil || !errors.Is(err, corenet.LoopPostQueueFull) {
			//: done.
			return
		}
		time.Sleep(postRetry)
	}
}

// drain runs on the lane's thread: every completion back to the offloader.
func (l *outboundLane) drain(io corenet.LoopIO) {
	//: cleared before the drain, so a completion pushed meanwhile posts again.
	l.posted.Store(false)
	//: every completion queued.
	for {
		job, ok := l.done.pop()
		//: none left.
		if !ok {
			//: drained.
			return
		}
		l.off.OnDone(io, job)
	}
}

// outboundLanes holds every lane of every server's outbound, by key: a
// PostFunc is a plain function and its argument an integer, so the
// completion op finds its lane here. It only grows; a stopped lane's entry
// stays, and its posts are refused by its stopped loop.
var outboundLanes struct {
	// mu serialises registration.
	mu sync.Mutex
	// lanes is replaced, never written in place, under mu.
	lanes atomic.Pointer[[]*outboundLane]
}

// registerLane gives l its key.
func registerLane(l *outboundLane) {
	outboundLanes.mu.Lock()
	defer outboundLanes.mu.Unlock()
	var cur []*outboundLane
	//: the table as it stands.
	if p := outboundLanes.lanes.Load(); p != nil {
		cur = *p
	}
	next := append(slices.Clip(cur), l)
	l.key = uintptr(len(cur))
	outboundLanes.lanes.Store(&next)
}

// runCompletions is the op a worker posts: it drains the lane named by key.
func runCompletions(io corenet.LoopIO, key uintptr) {
	lanes := *outboundLanes.lanes.Load()
	//: on the lane's thread.
	lanes[key].drain(io)
}

// outbound is one loop group's port to its workers.
type outbound struct {
	// lanes are the group's lanes, by index.
	lanes []*outboundLane
	// refused counts every lane's refused Sends.
	refused atomic.Uint64
	// workers waits on every worker.
	workers sync.WaitGroup
	// stopOnce makes stop idempotent.
	stopOnce sync.Once
	// busy is the offloader's Pending.
	busy func(lane int, ref corenet.LoopConnRef) bool
}

// startOutbound finds the chain's offloader — the first handler walking by
// Next that is a corenet.LoopOffloader with a worker — and, when there is
// one, starts a worker per lane and gives it the lanes' outbounds. A chain
// with none starts nothing and returns nil.
//
// Goroutine lifecycle: one worker per lane (outboundLane.run), owned by the
// returned outbound, counted in its workers group; signal ends each once its
// running job returns, and wait waits for them.
func startOutbound(chain corenet.LoopHandler, v corenet.LoopValue) *outbound {
	off, w := findOffloader(chain)
	//: nothing to offload.
	if w == nil {
		//: no worker.
		return nil
	}
	o := &outbound{}
	depth := orDefaultInt(v.Outbound, defaultOutboundDepth)
	n := loopLanes(v)
	outs := make([]corenet.LoopOutbound, n)
	//: one lane, one worker, one queue each way.
	for i := range n {
		l := &outboundLane{
			lane: i, jobs: newJobRing(depth), done: newJobRing(depth),
			wake: make(chan struct{}, 1), refused: &o.refused, off: off,
		}
		registerLane(l)
		o.lanes = append(o.lanes, l)
		outs[i] = l
		o.workers.Add(1)
		go l.run(w, &o.workers)
	}
	off.OnOutbound(corenet.LoopOutboundValue{Lanes: outs, OutputCap: orDefaultInt(v.OutputCap, portableOutputCap)})
	o.busy = off.Pending
	//: running.
	return o
}

// startOffloading starts the group's workers, when its chain offloads, then
// its engine: the workers run, and the chain holds its outbounds, before any
// callback can send a job, and a draining loop keeps every connection still
// waiting on one (R3). The workers stop after the engine, and State counts
// the refused jobs; an engine that does not start stops them at once.
func startOffloading(ctx context.Context, group *StreamGroup, lns []stdnet.Listener, plan loopPlan) (loopEngine, error) {
	out := startOutbound(group.loopChain, group.loop)
	//: nothing offloads: the engine alone.
	if out == nil {
		//: as before.
		return startLoopEngine(group, lns, plan)
	}
	group.loopBusy = out.busy
	eng, err := startLoopEngine(group, lns, plan)
	//: the backend refused: the workers go with it.
	if err != nil {
		out.signal()
		out.wait(ctx)
		//: refused.
		return nil, err
	}
	//: the engine, then its workers.
	return &offloadedEngine{loopEngine: eng, out: out}, nil
}

// findOffloader walks the chain by Next for its offloader.
func findOffloader(h corenet.LoopHandler) (corenet.LoopOffloader, corenet.LoopWorker) {
	//: outermost first, to the handler.
	for h != nil {
		//: an offloader with a worker.
		if off, ok := h.(corenet.LoopOffloader); ok {
			//: a nil worker offloads nothing.
			if w := off.Worker(); w != nil {
				//: found.
				return off, w
			}
		}
		layered, ok := h.(corenet.LoopLayered)
		//: the innermost handler.
		if !ok {
			//: none.
			return nil, nil
		}
		h = layered.Next()
	}
	//: none.
	return nil, nil
}

// signal tells every worker to stop once its running job returns. Queued
// jobs are dropped: their loop has stopped and could not write an answer.
func (o *outbound) signal() {
	o.stopOnce.Do(func() {
		//: each worker, woken to see it.
		for _, l := range o.lanes {
			l.stopped.Store(true)
			select {
			case l.wake <- struct{}{}:
			default:
			}
		}
	})
}

// wait waits for every worker to exit, within ctx.
//
// Goroutine lifecycle: one waiter, which ends when the last worker has; a
// ctx that ends first leaves it to end with them, holding nothing else.
func (o *outbound) wait(ctx context.Context) {
	waited := make(chan struct{})
	go func() {
		o.workers.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-ctx.Done():
	}
}

// offloadedEngine is a loop engine whose group offloads to workers: the
// workers stop once the engine has, and State counts the refused Sends.
type offloadedEngine struct {
	loopEngine
	// out is the group's outbound.
	out *outbound
}

// Shutdown drains the engine — answers still pending reach their
// connections, the workers running — then stops the workers.
func (e *offloadedEngine) Shutdown(ctx context.Context) error {
	err := e.loopEngine.Shutdown(ctx)
	e.out.signal()
	e.out.wait(ctx)
	//: the engine's outcome.
	return err
}

// Close closes the engine, then stops the workers without waiting on a job
// still running.
func (e *offloadedEngine) Close() error {
	err := e.loopEngine.Close()
	e.out.signal()
	//: the engine's outcome.
	return err
}

// State adds the refused Sends to the engine's report.
func (e *offloadedEngine) State() corenet.LoopStateValue {
	st := e.loopEngine.State()
	st.OutboundRefused = e.out.refused.Load()
	//: the engine's, and the outbound's.
	return st
}
