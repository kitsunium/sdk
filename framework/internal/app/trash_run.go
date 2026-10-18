package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// Emptying the trash (ADR 0178 C4): what is past due goes at start, the rest
// at its own instant on the app's clock. Each store that keeps a trash runs
// an agenda — each record's purge instant, kept from the store's own
// deletions — and a loop that sleeps on the earliest; nothing sweeps the
// trash while nothing is due. A record another process trashed joins the
// agenda at the next start; one written or restored meanwhile is not in the
// trash any more, and its instant passes doing nothing. Each entry names the
// tombstone it scheduled by the instant it was trashed: a purge empties only
// a tombstone trashed no later than that — never one a restoration and a
// new deletion made since — and leaves the agenda only when the entry is
// still the one it read.

// armSlack is how far the app's clock may move while a timer is armed
// before the timer is armed again: a timer takes a duration, which the clock
// counts from its own now, so a clock moved between the loop's reading and
// the arming — a test's manual clock — would put the timer past next.
const armSlack time.Duration = time.Millisecond

// trashRun is one store's trash in a running app: its agenda and its loop.
type trashRun struct {
	s     *storeCore
	a     *App
	state *loopState

	mu     sync.Mutex
	agenda map[string]trashEntry
	// wake is signalled when the agenda moves.
	wake   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
}

// trashEntry is a record on the trash's agenda: when it is due, and the
// instant its tombstone was trashed at the latest, which names it.
type trashEntry struct {
	due, trashed time.Time
}

// trashRunner is a store's trash, whatever its entity type.
type trashRunner interface {
	keepsTrash() bool
	startTrash(ctx context.Context, a *App) error
	stopTrash(ctx context.Context) error
	eraseTrashed(ctx context.Context, a *App, ids []string, reason string) ([]string, error)
}

// keepsTrash reports whether the store keeps a trash: it has revisions.
func (s *storeCore) keepsTrash() bool { return s.revisions > 0 && s.trashFor != nil }

// addTrashComponent registers the component that empties the stores' trash: at
// start, what is past due, then each record at its instant.
func (a *App) addTrashComponent() error {
	var stores []privacyStore
	for _, st := range a.productStores() {
		if st.keepsTrash() {
			stores = append(stores, st)
		}
	}
	if len(stores) == 0 {
		return nil
	}
	stop := func(ctx context.Context) error {
		var failed []error
		for _, st := range slices.Backward(stores) {
			failed = append(failed, st.stopTrash(ctx))
		}
		return errors.Join(failed...)
	}
	start := func(ctx context.Context) error {
		for _, st := range stores {
			if err := st.startTrash(ctx, a); err != nil {
				return errors.Join(err, stop(ctx))
			}
		}
		return nil
	}
	return a.component("trash", "", start, stop)
}

// startTrash purges what is past due in the store's trash — everything,
// when <store>-trash is 0 — and, while the store keeps a trash, starts the
// loop that purges the rest at its instant.
//
// Goroutine lifecycle: one goroutine runs the trash's loop until stopTrash
// cancels its context and waits for it.
func (s *storeCore) startTrash(ctx context.Context, a *App) error {
	tr, err := s.trashKeeper()
	if err != nil {
		return err
	}
	r := &trashRun{s: s, a: a, agenda: map[string]trashEntry{}, wake: make(chan struct{}, 1), done: make(chan struct{})}
	stones, err := tr.Tombstones(s.outside(ctx))
	if err != nil {
		return s.trashFailure(err)
	}
	for _, st := range stones {
		r.agenda[st.Key] = trashEntry{due: s.purgeAt(ctx, a, st), trashed: st.At}
	}
	if err := r.purgeDue(withActor(ctx, actorRetention), a.clock.Now()); err != nil {
		// What could not be purged stays in the trash and on the agenda: the
		// loop tries again after its backoff, or the next start does when no
		// loop runs (CodeTrash). The app starts.
		logger.Warn(ctx, a.log, "a store's trash could not be emptied at start; the framework tries again",
			logger.String("node", s.id), logger.String("error", describeText(err)))
	}
	if s.trashLength() == 0 {
		return nil
	}
	r.state = a.frameworkLoop(s.id+" trash", s.id, model.LoopRetention, "purge after "+humanDuration(s.trashLength()), loopOrigin{model.ProvenanceApp, frameworkName})
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(a.baseCtx))
	r.cancel = cancel
	s.bin.Store(r)
	go r.loop(withNode(loopCtx, s.id, a))
	return nil
}

// stopTrash stops the store's trash loop.
func (s *storeCore) stopTrash(ctx context.Context) error {
	r := s.bin.Swap(nil)
	if r == nil {
		return nil
	}
	r.cancel()
	select {
	case <-r.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.state.setState(r.a, model.LoopStopped)
	return nil
}

// trashPlanned puts a record the product trashed on the agenda.
func (s *storeCore) trashPlanned(ctx context.Context, a *App, st plug.TombstoneValue) {
	r := s.bin.Load()
	if r == nil {
		return
	}
	e := trashEntry{due: s.purgeAt(ctx, a, st), trashed: st.At}
	r.mu.Lock()
	r.agenda[st.Key] = e
	r.mu.Unlock()
	r.signal()
}

// forget takes key off the agenda, unless a deletion since put another
// tombstone of it there.
func (r *trashRun) forget(key string, e trashEntry) {
	r.mu.Lock()
	if r.agenda[key] == e {
		delete(r.agenda, key)
	}
	r.mu.Unlock()
}

// signal wakes the loop: a burst of deletions is one wake.
func (r *trashRun) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// earliest is the agenda's first instant, zero when it is empty.
func (r *trashRun) earliest() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	var first time.Time
	for _, e := range r.agenda {
		if first.IsZero() || e.due.Before(first) {
			first = e.due
		}
	}
	return first
}

// dueKeys are the keys due at now, in key order, with their entries.
func (r *trashRun) dueKeys(now time.Time) ([]string, map[string]trashEntry) {
	entries := map[string]trashEntry{}
	r.mu.Lock()
	for k, e := range r.agenda {
		if !e.due.After(now) {
			entries[k] = e
		}
	}
	r.mu.Unlock()
	keys := slices.Sorted(maps.Keys(entries))
	return keys, entries
}

// purgeDue purges every record due at now: a record no longer in the trash
// leaves the agenda, one whose purge failed stays on it.
func (r *trashRun) purgeDue(ctx context.Context, now time.Time) error {
	keys, entries := r.dueKeys(now)
	if len(keys) == 0 {
		return nil
	}
	a, s := r.a, r.s
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, op: model.OpRun, name: "Trash"})
	var failed []error
	for _, key := range keys {
		e := entries[key]
		if _, err := s.purgeTrashed(ctx, a, key, e.trashed); err != nil {
			failed = append(failed, err)
			continue
		}
		r.forget(key, e)
	}
	err := errors.Join(failed...)
	sp.attr("records", fmt.Sprint(len(keys)))
	sp.end(err)
	return err
}

// loop is the trash's goroutine: it sleeps until the next record is due,
// or a deletion moves the agenda, purges what is due, and sleeps again. A
// failed purge waits a backoff, one second doubling up to a minute.
func (r *trashRun) loop(ctx context.Context) {
	done := r.done
	defer close(done)
	ctx = withActor(r.a.asLoop(ctx, r.state.Name), actorRetention)
	var b retentionBackoff
	for {
		reason, ok := r.wait(ctx, r.nextWake(b.floor))
		if !ok {
			return
		}
		now := r.a.clock.Now()
		if first := r.earliest(); first.IsZero() || first.After(now) || now.Before(b.floor) {
			continue
		}
		r.runOnce(ctx, reason, now, &b)
		if ctx.Err() != nil {
			return
		}
	}
}

// nextWake is the agenda's first instant, no earlier than floor, and says so
// on the loop: idle until then, or until a deletion when nothing is due.
func (r *trashRun) nextWake(floor time.Time) time.Time {
	next := r.earliest()
	if !next.IsZero() && next.Before(floor) {
		next = floor
	}
	r.state.idle(r.a, next)
	return next
}

// runOnce purges what is due at now. A run that fails waits a backoff
// before the next; one that succeeds clears it.
func (r *trashRun) runOnce(ctx context.Context, reason string, now time.Time, b *retentionBackoff) {
	r.state.begin(r.a, reason)
	err := r.purgeDue(ctx, now)
	ended := r.a.clock.Now()
	if err != nil && ctx.Err() == nil {
		b.failures++
		b.floor = ended.Add(backoff(b.failures))
		logger.Warn(ctx, r.a.log, "a store's trash could not be emptied; the framework tries again", logger.String("node", r.s.id),
			logger.String("after", backoff(b.failures).String()), logger.String("error", describeText(err)))
	} else {
		b.failures, b.floor = 0, time.Time{}
	}
	r.state.ran(r.a, now, ended, err)
}

// arm arms a timer for next on the app's clock, and reports instead that
// next is due when the clock reached it meanwhile.
func (r *trashRun) arm(next time.Time) (clock.Timer, bool) {
	for {
		now := r.a.clock.Now()
		if !now.Before(next) {
			return nil, true
		}
		timer := r.a.clock.NewTimer(next.Sub(now))
		if r.a.clock.Now().Sub(now) <= armSlack {
			return timer, false
		}
		timer.Stop()
	}
}

// wait blocks until next on the app's clock, or a deletion that moved the
// agenda; with nothing on the agenda, it waits for a deletion alone. It
// says why the loop woke, and false when it stops. A wake already pending is
// taken before a timer is armed.
func (r *trashRun) wait(ctx context.Context, next time.Time) (string, bool) {
	select {
	case <-r.wake:
		return model.WakeChange, true
	default:
	}
	var fire <-chan time.Time
	if !next.IsZero() {
		timer, due := r.arm(next)
		if due {
			return model.WakeDeadline, true
		}
		defer timer.Stop()
		fire = timer.C()
	}
	select {
	case <-ctx.Done():
		return "", false
	case <-fire:
		return model.WakeDeadline, true
	case <-r.wake:
		return model.WakeChange, true
	}
}
