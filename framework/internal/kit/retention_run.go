package kit

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kitsunium/sdk/framework/model"
)

// retentionDay is the unit a retention is usually written in.
const retentionDay time.Duration = 24 * time.Hour

// What the retention did with one record.
const (
	// retainNothing left the record as it was.
	retainNothing retained = iota
	// retainErased erased the record's personal fields.
	retainErased
	// retainDeleted deleted the record.
	retainDeleted
)

// What the retention did with one record.
type retained int

// A retention's runs (ADR 0006 §5): what is due erased or deleted, a held
// record left, the store folded after.

// sweep erases and deletes what is due at now, then folds the store: one
// span on the store's node per run. Under dry-run it journals what it would
// do and changes nothing.
func (r *retentionRun[T]) sweep(ctx context.Context, now time.Time) error {
	keys := r.dueKeys(now)
	if len(keys) == 0 {
		return nil
	}
	a, s := r.a, r.s
	ctx = withActor(ctx, actorRetention)
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, op: model.OpRun, name: "Retention"})
	failed, wrote := r.retainAll(ctx, keys, now)
	if wrote {
		failed = append(failed, s.fold(ctx))
	}
	err := errors.Join(failed...)
	sp.attr("records", fmt.Sprint(len(keys)))
	sp.end(err)
	return err
}

// retainAll handles the records due at now, and says whether the store
// changed: then its files are folded, even when a journal entry failed.
func (r *retentionRun[T]) retainAll(ctx context.Context, keys []string, now time.Time) (failed []error, wrote bool) {
	dry := r.mode == model.RetentionDryRun
	for _, key := range keys {
		out, err := r.s.retainOne(ctx, r.a, key, now, r.mode)
		failed = append(failed, err)
		wrote = wrote || (out != retainNothing && !dry)
		if dry && err == nil {
			// Said once: the next write of the record asks again.
			r.forget(key)
		}
	}
	return failed, wrote
}

// retainOne handles one record the agenda says is due: read again, judged
// again at now — a hold, a write since —, then deleted, erased, or left.
// Under dry-run it says what it would do, and journals it. The outcome says
// what changed, even when the journal then failed.
func (s *StoreService[T]) retainOne(ctx context.Context, a *App, key string, now time.Time, mode string) (retained, error) {
	// The writer turn before the hold lock: a write's outermost lock
	// (transact_turn.go).
	ctx, release, err := s.turn(ctx)
	defer release()
	if err != nil {
		return retainNothing, err
	}
	unlock := a.lockHolds(s.id)
	defer unlock()
	v, err := s.read(ctx, key)
	if err != nil {
		if isNotFound(err) {
			return retainNothing, nil
		}
		return retainNothing, err
	}
	deleteDue, eraseDue, err := s.dueNow(ctx, a, key, v, now)
	switch {
	case err != nil || (!deleteDue && !eraseDue):
		s.reschedule(key)
		return retainNothing, err
	case mode == model.RetentionDryRun:
		return s.wouldRetain(ctx, a, key, v, deleteDue)
	case deleteDue:
		return s.retainDelete(ctx, a, key, v)
	}
	return s.retainErase(ctx, a, key)
}

// wouldRetain journals what the retention would do with a due record, and
// changes nothing: a dry run.
func (s *StoreService[T]) wouldRetain(ctx context.Context, a *App, key string, v T, deleteDue bool) (retained, error) {
	op, would := model.JournalErase, retainErased
	if deleteDue {
		op, would = model.JournalDelete, retainDeleted
	}
	return would, a.journal(ctx, &journalLine{op: op, dryRun: true, store: s.id, key: key, subject: s.subjectOf(v)})
}

// retainDelete deletes a due record, in a span of its own on the store's
// node, inside the run's.
func (s *StoreService[T]) retainDelete(ctx context.Context, a *App, key string, v T) (retained, error) {
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, op: model.OpWrite, name: "Delete"})
	removed, err := s.deleteRecord(ctx, a, key, v, actorRetention)
	sp.end(err)
	if removed {
		return retainDeleted, err
	}
	return retainNothing, err
}

// retainErase erases a due record, in a span of its own on the store's node,
// inside the run's.
func (s *StoreService[T]) retainErase(ctx context.Context, a *App, key string) (retained, error) {
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, op: model.OpWrite, name: "Erase"})
	wrote, err := s.eraseRecord(ctx, a, key, actorRetention)
	sp.end(err)
	if wrote {
		return retainErased, err
	}
	return retainNothing, err
}

// nextDue is when v, the record under key, is next due for erasure or
// deletion, and whether it is: a hold keeps it off the agenda, HeldUntil
// postpones it to the instant it gives, and a record with nothing left to
// erase is due only for its deletion. When kit cannot tell whether a hold
// keeps it, it is scheduled: the run asks again, and fails.
func (s *StoreService[T]) nextDue(a *App, key string, v T) (time.Time, bool) {
	if _, held, err := a.holdOf(context.Background(), s.id, key); held && err == nil {
		return time.Time{}, false
	}
	next := s.firstDue(a, v)
	if next.IsZero() {
		return time.Time{}, false
	}
	if until, held := s.heldUntil(v); held && until.After(next) {
		next = until
	}
	return next, true
}

// firstDue is the first instant a rule makes v due at, zero when none does.
// A retention function that panics makes the record due now: the run that
// finds the panic again fails, says so, and backs off.
func (s *StoreService[T]) firstDue(a *App, v T) time.Time {
	p := s.privacy
	var next time.Time
	add := func(at time.Time, ok bool, err error) {
		if err != nil {
			at, ok = a.clock.Now(), true
		}
		if ok && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	if p.delete != nil {
		add(p.delete.due(v))
	}
	if p.erase != nil && s.erasable(context.Background(), v) {
		add(p.erase.due(v))
	}
	return next
}

// dueNow says whether v is due, at now, for its deletion or its erasure. A
// held record is due for neither; when kit cannot tell, it is the error.
func (s *StoreService[T]) dueNow(ctx context.Context, a *App, key string, v T, now time.Time) (deleteDue, eraseDue bool, err error) {
	if held, err := s.heldAt(ctx, a, key, v, now); held || err != nil {
		return false, false, err
	}
	p := s.privacy
	if p.delete != nil {
		deleteDue, err = dueBy(p.delete, v, now)
	}
	if !deleteDue && p.erase != nil && s.erasable(ctx, v) {
		var eerr error
		eraseDue, eerr = dueBy(p.erase, v, now)
		err = cmp.Or(err, eerr)
	}
	return deleteDue, eraseDue, err
}

// dueBy says whether rule makes v due at now.
func dueBy[T any](rule *retentionRule[T], v T, now time.Time) (bool, error) {
	at, ok, err := rule.due(v)
	return ok && !at.After(now), err
}

// heldAt says whether a hold, or the store's HeldUntil, keeps v at now.
func (s *StoreService[T]) heldAt(ctx context.Context, a *App, key string, v T, now time.Time) (bool, error) {
	if _, held, err := a.holdOf(ctx, s.id, key); held || err != nil {
		return held, err
	}
	until, held := s.heldUntil(v)
	return held && until.After(now), nil
}

// heldUntil is the instant the store's HeldUntil holds v until, when it
// declares one and v gives one.
func (s *StoreService[T]) heldUntil(v T) (until time.Time, held bool) {
	p := s.privacy
	if p == nil || p.heldUntil == nil {
		return time.Time{}, false
	}
	defer func() {
		if recover() != nil {
			// A function that panics holds the record: kept, never lost.
			until, held = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), true
		}
	}()
	return p.heldUntil(v)
}

// retentionText says a rule in words, for the register and an export:
// "90 days after Closed", "at Report.EraseAt".
func retentionText[T any](a *App, r *retentionRule[T]) (string, bool) {
	if r == nil {
		return "", false
	}
	fn := "an instant the record carries"
	if r.fnAt != nil && r.fnAt.fn() != "" {
		fn = shortFunc(r.fnAt.fn())
	}
	if r.at != nil {
		return "at " + fn, true
	}
	d := r.wait()
	if a == nil || !a.running() {
		d = cmp.Or(r.delay, d)
	}
	text := humanDuration(d) + " after " + fn
	if r.setting != nil {
		text += " (setting " + r.setting.name + ")"
	}
	return text, true
}

// shortFunc is a function's name without its package path:
// "example.com/shop.Report.Closed" is "Report.Closed".
func shortFunc(qualified string) string {
	name := qualified[strings.LastIndex(qualified, "/")+1:]
	if _, rest, ok := strings.Cut(name, "."); ok {
		name = rest
	}
	return strings.TrimSuffix(name, "-fm")
}

// humanDuration writes a delay in days when it is whole days: "90 days",
// else as Go writes it.
func humanDuration(d time.Duration) string {
	switch {
	case d >= retentionDay && d%retentionDay == 0 && d/retentionDay == 1:
		return "1 retentionDay"
	case d >= retentionDay && d%retentionDay == 0:
		return fmt.Sprintf("%d days", d/retentionDay)
	}
	return d.String()
}

// retentionInfo describes a rule for the model.
func retentionInfo[T any](a *App, r *retentionRule[T]) *model.Retention {
	if r == nil {
		return nil
	}
	info := &model.Retention{}
	if r.at != nil {
		info.At = a.source(r.fnAt)
		return info
	}
	info.Since = a.source(r.fnAt)
	info.After = r.wait().String()
	if r.setting != nil {
		info.Setting = r.setting.name
	}
	return info
}

// retainOnce runs the store's retention once at the app's now, without its
// loop: what the command line and a test ask for.
func (s *StoreService[T]) retainOnce(ctx context.Context, a *App, mode string) (erased, deleted int, err error) {
	if !s.retains() {
		return 0, 0, nil
	}
	all, err := s.all(ctx)
	if err != nil {
		return 0, 0, err
	}
	now := a.clock.Now()
	var failed []error
	for _, v := range all {
		out, err := s.retainOne(ctx, a, s.keyOf(v), now, mode)
		failed = append(failed, err)
		switch out {
		case retainErased:
			erased++
		case retainDeleted:
			deleted++
		case retainNothing:
			// The record keeps its data: nothing to count.
		}
	}
	if erased+deleted > 0 && mode != model.RetentionDryRun {
		failed = append(failed, s.fold(ctx))
	}
	return erased, deleted, errors.Join(failed...)
}

// retains reports whether the store erases or deletes on its own.
func (s *StoreService[T]) retains() bool { return s.privacy.hasRetention() }

// addRetention registers the component that runs the stores' retention
// loops, as KIT_RETENTION says: on, dry-run, or not at all.
func (a *App) addRetention() error {
	var stores []privacyStore
	for _, st := range a.productStores() {
		if st.retains() {
			stores = append(stores, st)
		}
	}
	mode := a.retentionMode()
	if len(stores) == 0 || mode == model.RetentionOff {
		return nil
	}
	stop := func(ctx context.Context) error {
		var failed []error
		for _, st := range slices.Backward(stores) {
			failed = append(failed, st.stopRetention(ctx))
		}
		return errors.Join(failed...)
	}
	start := func(ctx context.Context) error {
		for _, st := range stores {
			if err := st.startRetention(ctx, a, mode); err != nil {
				return errors.Join(err, stop(ctx))
			}
		}
		return nil
	}
	return a.component("retention", "", start, stop)
}
