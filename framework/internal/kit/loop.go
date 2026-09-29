// Package kit — loops: work the daemon waits for, woken by time or by events.
package kit

import (
	"context"
	"errors"
	"maps"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/lifecycle"
	"github.com/kitsunium/sdk/pkg/v1/logger"
	"github.com/kitsunium/sdk/pkg/v1/resilience"
)

// Limits of the loops.
const (
	// loopBackoffFirst and loopBackoffMax bound the wait after a failure.
	loopBackoffFirst time.Duration = time.Second
	loopBackoffMax   time.Duration = time.Minute
	// minDeadlineGap is the least a deadline wake waits after a run.
	minDeadlineGap time.Duration = time.Second
	// healthyRun is how long a hand-written loop must run for its backoff to
	// start over.
	healthyRun time.Duration = time.Minute
)

// loopBackoff is the SDK's exponential backoff at kit's bounds.
var loopBackoff = resilience.Backoff{BaseDelay: loopBackoffFirst, MaxDelay: loopBackoffMax}

// The daemon's own loops.
//
// A declared loop (Service.Loop) is kit's: one goroutine per loop waits on
// the app's clock — so a test's manual clock drives it — for the earliest of
// its wakes, runs the product's function once, and waits again. Its wakes are data, which is why the diagram can draw
// them. A hand-written loop (Service.Go) is the product's: kit only starts
// it, cancels it, and restarts it after a failure.
// loopOrigin says who wrote the code a loop runs — the product, a library,
// kit — and which library, when one did.
type loopOrigin struct {
	provenance, library string
}

// Loop is a declared loop of the daemon: kit owns the wait, the product owns
// the work. Its wake sources — a period, a deadline the product computes, a
// topic — are data, so the diagram draws exactly what wakes it; kit runs it on
// its own goroutine, one run at a time, observes every run, and restarts it
// after a panic.
type Loop struct {
	nodeBase
	run  func(context.Context, WakeEvent) error
	opts loopOptions
	// deadlineAt is where the WakeAt function is, for the diagram.
	deadlineAt *pos

	// running is the loop's goroutine in the app that runs it, guarded by mu.
	mu      sync.Mutex
	running *loopRun
}

// WakeEvent says why a declared loop runs: the reason, the instant, and the
// topic that woke it when one did.
type WakeEvent struct {
	// Reason is model.WakeInterval, WakeDeadline, WakeTopic, WakeStart or
	// WakeManual.
	Reason string
	// At is when the wake happened, on the app's clock.
	At time.Time
	// Topic is the topic node that woke the loop, for a topic wake.
	Topic string
}

// LoopConfigurer configures a declared loop.
type LoopConfigurer interface {
	loopConfigure(o *loopOptions)
}

type loopOptions struct {
	every    time.Duration
	deadline func(context.Context) (time.Time, bool)
	topics   []topicWaker
	// everySet and nilTopic remember options that cannot be honoured, for
	// the declaration to report.
	everySet bool
	nilTopic bool
}

// topicWaker is a topic a loop wakes on, whatever its message type.
type topicWaker interface {
	base() *nodeBase
	// onPublish calls fn after every publish on the topic, until the
	// returned function removes it.
	onPublish(fn func()) (remove func())
}

type loopOption func(o *loopOptions)

// loopRun is a declared loop running in an app.
type loopRun struct {
	a     *App
	state *loopState
	// wake holds one token when a wake is pending; pending says why — the
	// first reason since the last run: a burst is one run.
	wake    chan struct{}
	pending *WakeEvent
	cancel  context.CancelFunc
	done    chan struct{}
	unhook  []func()
}

// nextWake is when a declared loop wakes by itself, and why; a zero at means
// only a topic or a nudge wakes it. floor is the end of the backoff after a
// failure: no run starts before it, but a nudge's.
type nextWake struct {
	at     time.Time
	reason string
	floor  time.Time
}

// Routine is a loop written by hand: a function that runs until its context
// ends. kit starts it with the app, cancels its context on shutdown and waits
// for it, restarts it after an error or a panic — backing off from one second
// to one minute — and reads its select statements to draw what it waits on.
type Routine struct {
	nodeBase
	run func(context.Context) error

	mu sync.Mutex
	// sup runs the function while the app runs: the SDK's supervisor.
	sup *lifecycle.Supervisor
}

// loopConfigure sets the option on what it configures.
func (f loopOption) loopConfigure(o *loopOptions) { f(o) }

// WakeEvery wakes the loop every d.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func WakeEvery(d time.Duration) LoopConfigurer {
	return loopOption(func(o *loopOptions) { o.every, o.everySet = d, true })
}

// WakeAt wakes the loop at the time next returns, asked again after every
// run; false means "no deadline for now". It is how a loop sleeps exactly
// until its next piece of work — the next reminder due — rather than polling.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func WakeAt(next func(context.Context) (time.Time, bool)) LoopConfigurer {
	return loopOption(func(o *loopOptions) { o.deadline = next })
}

// WakeOn wakes the loop whenever a message is published on topic. It is a
// nudge, not a subscription: the loop is not handed the message and wakes
// once for a burst — it re-reads the state it works on.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func WakeOn[T any](topic *TopicService[T]) LoopConfigurer {
	return loopOption(func(o *loopOptions) {
		if topic != nil {
			o.topics = append(o.topics, topic)
		} else {
			o.nilTopic = true
		}
	})
}

// Loop declares a loop run by kit: run is called once at start, then on
// every wake.
//
// Runs never overlap: wakes that come while the function runs are gathered
// into one run after it. A run that fails or panics is counted, and the next
// one waits a backoff — one second, doubling up to a minute — unless it is
// a [Loop.Nudge]. A deadline already past wakes the loop at once, but never
// sooner than a second after the previous run: a WakeAt that keeps answering
// the past costs a run a second, not a spinning core.
//
//go:noinline
func (s *Service) Loop(name string, run func(context.Context, WakeEvent) error, opts ...LoopConfigurer) *Loop {
	l := NewLoop(run)
	for _, o := range opts {
		o.loopConfigure(&l.opts)
	}
	l.kind, l.name, l.decl = model.KindLoop, name, callerPos()
	if p, _ := funcInfo(run); p.file != "" {
		l.body = &p
	}
	if p, _ := funcInfo(l.opts.deadline); p.file != "" {
		l.deadlineAt = &p
	}
	s.add(l, true)
	if run == nil {
		s.problem(l.decl, l.id, "loop.nil", "name", name)
	}
	if l.opts.everySet && l.opts.every <= 0 {
		s.problem(l.decl, l.id, "loop.every", "name", name)
	}
	if l.opts.nilTopic {
		s.problem(l.decl, l.id, "loop.topic", "name", name)
	}
	return l
}

// Nudge wakes the loop now, as if its deadline had come. The run's reason
// is model.WakeManual; a nudge also cuts short the backoff after a failure.
// It does nothing while the loop is not running.
func (l *Loop) Nudge() { l.signal(WakeEvent{Reason: model.WakeManual}) }

// describe fills the graph node out with what the Loop declares, and returns
// its edges.
func (l *Loop) describe(a *App, out *model.Node) []model.Edge {
	info := &model.LoopInfo{Style: model.LoopDeclared, Restart: "after a failure or a panic, backing off from 1s to 1m"}
	info.Wakes = append(info.Wakes, model.WakeSource{Kind: model.WakeStart})
	if l.opts.every > 0 {
		info.Wakes = append(info.Wakes, model.WakeSource{Kind: model.WakeInterval, Every: l.opts.every.String()})
	}
	if l.opts.deadline != nil {
		info.Wakes = append(info.Wakes, model.WakeSource{Kind: model.WakeDeadline, Func: a.source(l.deadlineAt)})
	}
	var edges []model.Edge
	for _, t := range l.opts.topics {
		id := t.base().id
		info.Wakes = append(info.Wakes, model.WakeSource{Kind: model.WakeTopic, Topic: id})
		edges = append(edges, model.Edge{From: id, To: l.id, Kind: model.EdgeWakes, Declared: true})
	}
	info.Wakes = append(info.Wakes, model.WakeSource{Kind: model.WakeManual})
	out.Loop = info
	return edges
}

// schedule is the human text of what wakes a declared loop.
func (l *Loop) schedule() string {
	parts := []string{"at start"}
	if l.opts.every > 0 {
		parts = append(parts, "every "+l.opts.every.String())
	}
	if l.opts.deadline != nil {
		parts = append(parts, "at its deadline")
	}
	for _, t := range l.opts.topics {
		parts = append(parts, "on "+t.base().id)
	}
	return strings.Join(parts, " · ")
}

// backoff is the wait after n consecutive failures: 1s, doubling, up to 1m.
func backoff(n int) time.Duration { return loopBackoff.Delay(n) }

// signal wakes the loop, when it runs. Guarded by l.mu.
func (l *Loop) signal(w WakeEvent) {
	l.mu.Lock()
	r := l.running
	if r == nil {
		l.mu.Unlock()
		return
	}
	if r.pending == nil {
		w.At = r.a.clock.Now()
		r.pending = &w
	}
	l.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// take returns the pending wake and clears it.
func (l *Loop) take(r *loopRun) WakeEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := WakeEvent{Reason: model.WakeManual, At: r.a.clock.Now()}
	if r.pending != nil {
		w, r.pending = *r.pending, nil
	}
	return w
}

// start runs the loop until stop.
//
// Goroutine lifecycle: one goroutine runs the loop's cycle until stop cancels
// its context; stop waits for it.
func (l *Loop) start(_ context.Context, a *App) error {
	for _, t := range l.opts.topics {
		if t.base().svc.app.Load() != a {
			return failure(CodeLoopNotMounted, "LOOP_TOPIC_NOT_MOUNTED", "a loop wakes on a topic whose service is not mounted in this app", nil,
				errs.String("loop", l.id), errs.String("topic", t.base().id))
		}
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(a.baseCtx))
	r := &loopRun{
		a:      a,
		state:  a.kitLoop(l.id, l.id, model.LoopWake, l.schedule(), loopOrigin{model.ProvenanceKit, "kit"}),
		wake:   make(chan struct{}, 1),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	for _, t := range l.opts.topics {
		topic := t.base().id
		r.unhook = append(r.unhook, t.onPublish(func() { l.signal(WakeEvent{Reason: model.WakeTopic, Topic: topic}) }))
	}
	l.mu.Lock()
	l.running = r
	l.mu.Unlock()
	go l.cycle(ctx, r)
	return nil
}

// stop ends the loop and waits for its run until ctx ends.
func (l *Loop) stop(ctx context.Context, a *App) error {
	l.mu.Lock()
	r := l.running
	l.running = nil
	l.mu.Unlock()
	if r == nil {
		return nil
	}
	for _, remove := range r.unhook {
		remove()
	}
	r.cancel()
	select {
	case <-r.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.state.setState(a, model.LoopStopped)
	return nil
}

// cycle is the loop's goroutine: run, wait, run.
func (l *Loop) cycle(ctx context.Context, r *loopRun) {
	done := r.done
	defer close(done)
	a := r.a
	ctx = a.asLoop(ctx, l.id)
	w := WakeEvent{Reason: model.WakeStart, At: a.clock.Now()}
	failures := 0
	for {
		started := a.clock.Now()
		due, hasDue, err := l.once(ctx, r, w)
		ended := a.clock.Now()
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			err = nil // the app is stopping: the run honoured its context
		}
		if err != nil {
			failures++
		} else {
			failures = 0
		}
		next := l.next(ended, due, hasDue, failures)
		r.state.idle(a, next.at)
		r.state.ran(a, started, ended, err)
		if ctx.Err() != nil {
			return
		}
		var ok bool
		if w, ok = l.wait(ctx, r, next); !ok {
			return
		}
	}
}

// once is one run: the dev tools' fault, the function, then — still inside
// the run's span, so what it reads is drawn from the loop — the deadline.
func (l *Loop) once(ctx context.Context, r *loopRun, w WakeEvent) (due time.Time, hasDue bool, err error) {
	a := r.a
	r.state.begin(a, w.Reason)
	ctx, sp := a.begin(ctx, &spanStart{node: l.id, op: model.OpRun, name: l.name})
	sp.attr("wake", w.Reason)
	if w.Topic != "" {
		sp.attr("topic", w.Topic)
	}
	defer func() {
		if p := recover(); p != nil {
			logger.Error(ctx, a.log, "a loop panicked", logger.String("node", l.id), logger.Any("panic", p), logger.String("stack", string(debug.Stack())))
			err = failure(CodeLoopPanic, "LOOP_PANICKED", "the loop's function panicked", nil, errs.String("loop", l.id))
			hasDue = false
		}
		sp.end(err)
	}()
	err = l.run(ctx, w)
	if err != nil && ctx.Err() == nil {
		logger.Warn(ctx, a.log, "a loop run failed", logger.String("node", l.id), logger.String("error", err.Error()))
	}
	if l.opts.deadline != nil {
		due, hasDue = l.opts.deadline(ctx)
	}
	return due, hasDue, err
}

// next computes the loop's next wake after a run that ended at ended.
func (l *Loop) next(ended, due time.Time, hasDue bool, failures int) nextWake {
	var n nextWake
	if l.opts.every > 0 {
		n.at, n.reason = ended.Add(l.opts.every), model.WakeInterval
	}
	if hasDue {
		n.deadline(laterOf(due, ended.Add(minDeadlineGap)))
	}
	if failures > 0 {
		n.backOff(ended.Add(backoff(failures)))
	}
	return n
}

// deadline makes the wake the deadline at, when it comes first.
func (n *nextWake) deadline(at time.Time) {
	if n.at.IsZero() || at.Before(n.at) {
		n.at, n.reason = at, model.WakeDeadline
	}
}

// backOff holds the wake until floor, the backoff's end.
func (n *nextWake) backOff(floor time.Time) {
	n.floor = floor
	if !n.at.IsZero() && n.at.Before(floor) {
		n.at = floor
	}
}

// wait blocks until the loop's next wake, on the app's clock, and says why.
// It reports false when the loop is stopping.
//
// A publish or a nudge that came while the loop ran is taken before a timer
// is armed: an armed timer is one the loop sleeps on, never one it drops at
// once for a wake already there — during a backoff, to arm the backoff's end
// instead. A test on a manual clock counts the armed waits to know the loop
// sleeps before it moves the clock; moved between the loop's reading of the
// clock and its timer, the clock would put the timer past the instant it
// moved to. Nothing changes on a real clock: the loop runs for that wake, or
// holds it until the backoff's end, as it did when it armed a timer first.
func (l *Loop) wait(ctx context.Context, r *loopRun, n nextWake) (WakeEvent, bool) {
	var held *WakeEvent // a wake that came during the backoff
	for {
		if w, ok, done := l.takePending(r, n.floor, &held); done {
			return w, ok
		}
		if w, ok, done := l.sleep(ctx, r, n, &held); done {
			return w, ok
		}
	}
}

// takePending judges a wake already there, without waiting: done when the
// loop runs for it — or ends, its wake channel closed —; a wake that must
// wait for the backoff's end is held.
func (l *Loop) takePending(r *loopRun, floor time.Time, held **WakeEvent) (wake WakeEvent, ok, done bool) {
	select {
	case _, open := <-r.wake:
		if !open {
			return WakeEvent{}, false, true
		}
		return l.judged(r, floor, held)
	default:
		return WakeEvent{}, false, false
	}
}

// sleep waits for the next wake: the instant n names — the backoff's end
// when a wake is held —, a wake that comes, or the end of ctx.
func (l *Loop) sleep(ctx context.Context, r *loopRun, n nextWake, held **WakeEvent) (wake WakeEvent, ok, done bool) {
	a := r.a
	at := n.at
	if *held != nil {
		at = n.floor
	}
	var fire <-chan time.Time
	if !at.IsZero() {
		timer := a.clock.NewTimer(at.Sub(a.clock.Now()))
		defer timer.Stop()
		fire = timer.C()
	}
	select {
	case <-ctx.Done():
		return WakeEvent{}, false, true
	case <-fire:
		if *held != nil {
			w := **held
			w.At = a.clock.Now()
			return w, true, true
		}
		return WakeEvent{Reason: n.reason, At: a.clock.Now()}, true, true
	case _, open := <-r.wake:
		if !open {
			return WakeEvent{}, false, true
		}
		return l.judged(r, n.floor, held)
	}
}

// judged runs the loop for the wake that came, or holds it until floor.
func (l *Loop) judged(r *loopRun, floor time.Time, held **WakeEvent) (wake WakeEvent, ok, done bool) {
	came, now := l.woken(r, floor)
	if now {
		return came, true, true
	}
	*held = &came
	return WakeEvent{}, false, false
}

// woken takes the wake that came and says whether the loop runs for it now
// — a nudge, or any wake once floor, the backoff's end, has come — or holds
// it until floor. wait asks it for a wake already there and for one that
// comes while it sleeps: both are judged alike.
func (l *Loop) woken(r *loopRun, floor time.Time) (wake WakeEvent, now bool) {
	wake = l.take(r)
	return wake, wake.Reason == model.WakeManual || !r.a.clock.Now().Before(floor)
}

// onPublish calls fn after every publish on the topic, until removed.
func (t *TopicService[T]) onPublish(fn func()) (remove func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.wakers == nil {
		t.wakers = map[int]func(){}
	}
	id := t.nextID
	t.nextID++
	t.wakers[id] = fn
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		delete(t.wakers, id)
	}
}

// wake calls the functions registered with onPublish.
func (t *TopicService[T]) wake() {
	t.mu.Lock()
	fns := slices.Collect(maps.Values(t.wakers))
	t.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// Go declares a hand-written loop. run must return when ctx ends; returning
// earlier, with or without an error, restarts it after a backoff.
//
// Everything run does is drawn from the loop's node: a store it reads, a
// topic it publishes on. kit has no span around it — a run lasts as long as
// the app — so each of those calls starts a trace of its own.
//
//go:noinline
func (s *Service) Go(name string, run func(context.Context) error) *Routine {
	r := &Routine{run: run}
	r.kind, r.name, r.decl = model.KindLoop, name, callerPos()
	if p, _ := funcInfo(run); p.file != "" {
		r.body = &p
	}
	s.add(r, true)
	if run == nil {
		s.problem(r.decl, r.id, "routine.nil", "name", name)
	}
	return r
}

// describe fills the graph node out with what the Routine declares, and
// returns its edges.
func (r *Routine) describe(_ *App, out *model.Node) []model.Edge {
	out.Loop = &model.LoopInfo{Style: model.LoopGoroutine, Restart: "after an error or a panic, backing off from 1s to 1m"}
	return nil
}

// start runs the routine until stop.
func (r *Routine) start(_ context.Context, a *App) error {
	state := a.kitLoop(r.id, r.id, model.LoopRoutine, "supervised · restarts after 1s to 1m", loopOrigin{provenance: model.ProvenanceProduct})
	// The SDK's supervisor runs the function and restarts it after every
	// early end, on the app's clock and kit's backoff; kit draws what it
	// reports.
	var lastErr error
	sup, err := lifecycle.NewSupervisor(r.id, func(ctx context.Context) error {
		// The goroutine is the loop's: its pprof label (dev) lets the
		// goroutine view and the profiles name it, whatever the product's
		// code does.
		return r.once(a.asLoop(ctx, r.id), a)
	}, lifecycle.SupervisorConfig{
		Clock:        a.clock,
		Backoff:      loopBackoff,
		HealthyAfter: healthyRun,
		Observe: func(e lifecycle.SupervisionEvent) {
			lastErr = r.observe(a, state, &e, lastErr)
		},
	})
	if err == nil {
		err = sup.Start(withNode(context.WithoutCancel(a.baseCtx), r.id, a))
	}
	if err != nil {
		return failure(CodeLoopStart, "LOOP_START", "a hand-written loop cannot start", err, errs.String("loop", r.id))
	}
	r.mu.Lock()
	r.sup = sup
	r.mu.Unlock()
	return nil
}

// observe draws what the supervisor did on the loop's state, and says it in
// kit's log. It runs on the supervisor's goroutine, one event at a time, and
// returns the last run's error, which a run's end replaces and a restart
// reports.
func (r *Routine) observe(a *App, state *loopState, e *lifecycle.SupervisionEvent, lastErr error) error {
	switch e.Phase {
	case lifecycle.SupervisionRunStarted:
		if e.Run > 1 {
			a.mu.Lock()
			state.Restarts++
			state.NextRun = nil
			a.mu.Unlock()
		}
		state.begin(a, "")
	case lifecycle.SupervisionRunEnded:
		lastErr = e.Err
		state.idle(a, time.Time{})
		state.ran(a, e.At.Add(-e.Duration), e.At, e.Err)
	case lifecycle.SupervisionRestarting:
		ctx := context.Background()
		if err := lastErr; err == nil {
			logger.Warn(ctx, a.log, "a hand-written loop returned before its context ended; restarting it", logger.String("node", r.id), logger.String("after", e.Delay.String()))
		} else {
			logger.Warn(ctx, a.log, "a hand-written loop failed; restarting it", logger.String("node", r.id), logger.String("after", e.Delay.String()), logger.String("error", err.Error()))
		}
		state.idle(a, e.At.Add(e.Delay))
		state.setState(a, model.LoopRestarting)
	case lifecycle.SupervisionStopped:
		state.setState(a, model.LoopStopped)
	default:
		// Every other phase changes nothing the Studio shows.
	}
	return lastErr
}

// stop ends the routine and waits for it until ctx ends.
func (r *Routine) stop(ctx context.Context, _ *App) error {
	r.mu.Lock()
	sup := r.sup
	r.sup = nil
	r.mu.Unlock()
	if sup == nil {
		return nil
	}
	return sup.Stop(ctx)
}

// once runs the loop's function once — after the dev tools' fault — and
// turns a panic into an error.
func (r *Routine) once(ctx context.Context, a *App) (err error) {
	defer func() {
		if p := recover(); p != nil {
			logger.Error(ctx, a.log, "a hand-written loop panicked", logger.String("node", r.id), logger.Any("panic", p), logger.String("stack", string(debug.Stack())))
			err = failure(CodeLoopPanic, "LOOP_PANICKED", "the loop's function panicked", nil, errs.String("loop", r.id))
		}
	}()
	return r.run(ctx)
}

// kitLoop registers a loop with its provenance and the code that loops —
// which win over what its kind implies: a mailer's outbox is a consumer the
// SDK's mail spool drives — and marks it waiting unless a.loop already did.
func (a *App) kitLoop(name, node, kind, schedule string, origin loopOrigin) *loopState {
	provenance, library := origin.provenance, origin.library
	l := a.loop(name, node, kind, schedule)
	a.mu.Lock()
	l.Provenance, l.Library = provenance, library
	if l.State == "" {
		l.State = model.LoopWaiting
	}
	a.mu.Unlock()
	return l
}

// idle records that the loop waits, and when it runs next — at, the zero
// time when that is not known — before ran streams the run that just ended.
//
// The state keeps a copy of its own: the graph and the event stream hand
// NextRun out by pointer, and their readers dereference it outside a.mu, so
// the instant must never change once kept. Taken by value, at can never be
// a caller's variable: the secrets' rotation loop once passed the address of
// its own, and wrote it again at the next rotation while a model it had
// handed out was read (TestALoopsNextRunIsItsOwnCopy).
func (l *loopState) idle(a *App, at time.Time) {
	var next *time.Time
	if !at.IsZero() {
		next = new(at.UTC())
	}
	a.mu.Lock()
	l.State = model.LoopWaiting
	l.NextRun = next
	a.mu.Unlock()
}

// NewLoop is a loop no service declares yet, whose runs are run:
// [Service.Loop] makes one and declares it, which is how a product gets one.
func NewLoop(run func(context.Context, WakeEvent) error) *Loop { return &Loop{run: run} }

// laterOf is the later of two instants.
func laterOf(x, y time.Time) time.Time {
	if x.Before(y) {
		return y
	}
	return x
}
