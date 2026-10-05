package kit

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// The retention loop ------------------------------------------------------------

// retentionRun is one store's retention in a running app: its agenda — each
// record's next due instant, kept from the store's writes — and its loop.
type retentionRun[T any] struct {
	s     *StoreService[T]
	a     *App
	state *loopState
	mode  string

	mu     sync.Mutex
	agenda map[string]time.Time
	// timer is when the loop's timer fires; zero while it waits for a write.
	timer time.Time
	// wake is signalled by a write that moves the agenda.
	wake    chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
	unwatch func()
}

// retentionRunner is a store's retention, whatever its entity type.
type retentionRunner interface {
	startRetention(ctx context.Context, a *App, mode string) error
	stopRetention(ctx context.Context) error
}

// startRetention builds the agenda from the store's records and starts the
// loop, which runs only when a record is due.
//
// Goroutine lifecycle: one goroutine runs the retention loop until
// stopRetention cancels its context and waits for it.
func (s *StoreService[T]) startRetention(ctx context.Context, a *App, mode string) error {
	p := s.privacy
	if !p.hasRetention() || mode == model.RetentionOff {
		return nil
	}
	if s.engine() == nil {
		return notRunning(&s.nodeBase)
	}
	r := newRetentionRun(s, a, mode)
	// The hooks first, then the scan: a write between the two is seen by
	// both, never by neither.
	r.unwatch = s.watch(r.written, r.forget)
	if err := r.scan(ctx); err != nil {
		r.unwatch()
		return err
	}
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(a.baseCtx))
	r.cancel = cancel
	p.mu.Lock()
	p.run = r
	p.mu.Unlock()
	go r.loop(withNode(loopCtx, s.id, a))
	return nil
}

// newRetentionRun is a store's retention, its loop registered with the
// daemon's.
func newRetentionRun[T any](s *StoreService[T], a *App, mode string) *retentionRun[T] {
	r := &retentionRun[T]{
		s: s, a: a, mode: mode, agenda: map[string]time.Time{},
		wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
	r.state = a.kitLoop(s.id+" retention", s.id, model.LoopRetention, r.schedule(), loopOrigin{model.ProvenanceKit, "kit"})
	return r
}

// scan puts every record of the store on the agenda.
func (r *retentionRun[T]) scan(ctx context.Context) error {
	all, err := r.s.all(ctx)
	if err != nil {
		return err
	}
	for _, v := range all {
		if key := r.s.keyOf(v); key != "" {
			r.plan(key, v)
		}
	}
	return nil
}

// stopRetention stops the loop and takes its hooks back.
func (s *StoreService[T]) stopRetention(ctx context.Context) error {
	p := s.privacy
	if p == nil {
		return nil
	}
	p.mu.Lock()
	r := p.run
	p.run = nil
	p.mu.Unlock()
	if r == nil {
		return nil
	}
	r.unwatch()
	r.cancel()
	select {
	case <-r.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.state.setState(r.a, model.LoopStopped)
	return nil
}

// retention is the running retention, or nil.
func (s *StoreService[T]) retention() *retentionRun[T] {
	if s.privacy == nil {
		return nil
	}
	s.privacy.mu.Lock()
	defer s.privacy.mu.Unlock()
	return s.privacy.run
}

// schedule is the loop's schedule, in words.
func (r *retentionRun[T]) schedule() string {
	var parts []string
	for _, x := range []struct {
		what string
		rule *retentionRule[T]
	}{{"erase", r.s.privacy.erase}, {"delete", r.s.privacy.delete}} {
		if x.rule == nil {
			continue
		}
		if x.rule.at != nil {
			parts = append(parts, x.what+" at its instant")
		} else {
			parts = append(parts, x.what+" after "+humanDuration(x.rule.wait()))
		}
	}
	text := strings.Join(parts, " · ")
	if r.mode == model.RetentionDryRun {
		text += " · dry run"
	}
	return text
}

// written recomputes when key is due, after a write of it — the store's
// hook, which carries no context: the record is read again as it stands.
func (r *retentionRun[T]) written(key string) {
	v, err := r.s.read(context.Background(), key)
	if err != nil {
		r.forget(key)
		return
	}
	r.plan(key, v)
}

// plan puts v, the record under key, on the agenda at its next due instant,
// or takes it off; a record due earlier than the loop's timer wakes the
// loop. The wake is sent under the lock the loop reads the agenda with: a
// loop that reads the record finds its wake already pending, and takes it
// before it arms a timer (wait).
func (r *retentionRun[T]) plan(key string, v T) {
	at, due := r.s.nextDue(r.a, key, v)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !due {
		delete(r.agenda, key)
		return
	}
	r.agenda[key] = at
	if r.timer.IsZero() || at.Before(r.timer) {
		r.signal()
	}
}

// forget takes a deleted record off the agenda.
func (r *retentionRun[T]) forget(key string) {
	r.mu.Lock()
	delete(r.agenda, key)
	r.mu.Unlock()
}

// signal wakes the loop: a burst of writes is one wake.
func (r *retentionRun[T]) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// earliest is the agenda's first instant, zero when it is empty.
func (r *retentionRun[T]) earliest() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	var first time.Time
	for _, at := range r.agenda {
		if first.IsZero() || at.Before(first) {
			first = at
		}
	}
	return first
}

// dueKeys are the keys due at now, in key order.
func (r *retentionRun[T]) dueKeys(now time.Time) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var keys []string
	for k, at := range r.agenda {
		if !at.After(now) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

// reschedule puts the record under key on the agenda again, from what it
// holds now: after a hold is placed or lifted, or a run left it.
func (s *StoreService[T]) reschedule(key string) {
	if r := s.retention(); r != nil {
		r.written(key)
	}
}

// loop is the retention's goroutine: it sleeps until the next record is
// due, or a write brings one forward, handles what is due, and sleeps again.
func (r *retentionRun[T]) loop(ctx context.Context) {
	done := r.done
	defer close(done)
	ctx = r.a.asLoop(ctx, r.state.Name)
	var b retentionBackoff
	for {
		next := r.nextWake(b.floor)
		reason, run, ok := r.wait(ctx, next, b.floor)
		if !ok {
			return
		}
		if !run {
			continue
		}
		r.runOnce(ctx, reason, &b)
		if ctx.Err() != nil {
			return
		}
	}
}

// retentionBackoff is how long a failing retention waits: its failures in a
// row, and the earliest its next run may start.
type retentionBackoff struct {
	failures int
	floor    time.Time
}

// nextWake is the agenda's first instant, no earlier than floor, and says so
// on the loop: idle until then, or until a write when nothing is due.
func (r *retentionRun[T]) nextWake(floor time.Time) time.Time {
	next := r.earliest()
	if !next.IsZero() && next.Before(floor) {
		next = floor
	}
	r.state.idle(r.a, next)
	return next
}

// runOnce handles what is due now. A run that fails waits a backoff, one
// second doubling up to a minute; a run that succeeds, the least gap a
// deadline waits after a run.
func (r *retentionRun[T]) runOnce(ctx context.Context, reason string, b *retentionBackoff) {
	a := r.a
	started := a.clock.Now()
	r.state.begin(a, reason)
	err := r.sweep(ctx, started)
	ended := a.clock.Now()
	if err != nil && ctx.Err() == nil {
		b.failures++
		b.floor = ended.Add(backoff(b.failures))
		logger.Warn(ctx, a.log, "a store's retention failed; kit tries again", logger.String("node", r.s.id),
			logger.String("after", backoff(b.failures).String()), logger.String("error", describeText(err)))
	} else {
		b.failures, b.floor = 0, ended.Add(minDeadlineGap)
	}
	r.state.ran(a, started, ended, err)
}

// wait blocks until next, or a write that moved the agenda. It
// says why the loop runs, whether it runs — a write that makes nothing due
// now only moves the timer — and false when the loop stops. With nothing
// due it waits for a write alone: an idle store's retention never runs.
//
// A write that came while the loop ran — its own erasures
// included — is taken before a timer is armed: an armed timer is one the
// loop sleeps on, never one it drops at once for a wake already there. A
// test on a manual clock counts the armed waits to know the loop sleeps
// before it moves the clock; moved between the loop's reading of the clock
// and its timer, the clock would put the timer past the instant it moved
// to.
func (r *retentionRun[T]) wait(ctx context.Context, next, floor time.Time) (reason string, run, ok bool) {
	select {
	case <-r.wake:
		return r.woken(floor)
	default:
	}
	a := r.a
	var fire <-chan time.Time
	if !next.IsZero() {
		timer := a.clock.NewTimer(max(next.Sub(a.clock.Now()), 0))
		defer timer.Stop()
		fire = timer.C()
	}
	r.setTimer(next)
	defer r.setTimer(time.Time{})
	select {
	case <-ctx.Done():
		return "", false, false
	case <-fire:
		return model.WakeDeadline, true, true
	case <-r.wake:
		return r.woken(floor)
	}
}

// woken says whether a write that moved the agenda makes the loop run now —
// a record due, and floor, the backoff, past — or only sleep again, until
// the agenda's new first instant.
func (r *retentionRun[T]) woken(floor time.Time) (reason string, run, ok bool) {
	now := r.a.clock.Now()
	if first := r.earliest(); !first.IsZero() && !first.After(now) && !now.Before(floor) {
		return model.WakeChange, true, true
	}
	return "", false, true
}

// setTimer records when the loop's timer fires, for written to compare.
func (r *retentionRun[T]) setTimer(at time.Time) {
	r.mu.Lock()
	r.timer = at
	r.mu.Unlock()
}
