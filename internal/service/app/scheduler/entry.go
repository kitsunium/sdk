package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	coresched "github.com/kitsunium/sdk/internal/core/app/scheduler"
)

// entry is one registered pairing plus the state the run loop keeps for it.
//
// next and armed are touched ONLY by the loop goroutine, which is why they
// need no lock; busy is the one field a job goroutine writes back, so it is an
// atomic. Keeping that split explicit is what lets the loop decide overlap
// without taking a mutex on its hot path.
type entry struct {
	// name, schedule, job, overlap and deadline are the registration, frozen
	// once Add returns; overlap is resolved, AllowOverlap read as
	// OverlapAllow.
	name     string
	schedule coresched.Schedule
	job      coresched.Job
	overlap  coresched.OverlapPolicy
	deadline time.Duration
	// next is the entry's next due instant; loop goroutine only.
	next time.Time
	// armed reports whether next holds a real due instant; loop goroutine
	// only. A Schedule that reports exhaustion — or that misbehaves by not
	// advancing — disarms its own entry and the scheduler keeps running.
	armed bool
	// busy reports that a run of THIS entry has not returned yet. Set by the
	// loop before starting a run, cleared by the job goroutine before it
	// publishes the result, so an observer that has seen the result also sees
	// a free slot.
	busy atomic.Bool
	// mu guards current.
	mu sync.Mutex
	// current is the newest run of an OverlapReplace entry — running, or
	// waiting for the one it replaced to return —, nil when there is none.
	current *replaceable
}

// replaceable is one run of an OverlapReplace entry: cancel ends its context,
// done is released once it has returned and published its result, and
// replaced says a newer fire took its place.
type replaceable struct {
	cancel   context.CancelFunc
	done     sync.WaitGroup
	replaced atomic.Bool
}

// newEntry builds the engine state for a validated registration.
func newEntry(value coresched.EntryValue) *entry {
	//: the registration is copied out of the caller's value; the engine never
	//: reads EntryValue again, so a caller mutating theirs changes nothing.
	overlap := value.Overlap
	//: the older spelling of OverlapAllow; validateEntry refused it beside
	//: any other policy.
	if value.AllowOverlap {
		overlap = coresched.OverlapAllow
	}
	//: frozen copy of the registration.
	return &entry{
		name:     value.Name,
		schedule: value.Schedule,
		job:      value.Job,
		overlap:  overlap,
		deadline: value.StartingDeadline,
	}
}

// expired reports whether a fire for scheduled, reached at now, is later than
// the entry's StartingDeadline allows. A non-positive deadline is none.
func (e *entry) expired(scheduled, now time.Time) bool {
	//: no deadline, or late within it.
	return e.deadline > 0 && now.Sub(scheduled) > e.deadline
}

// replaceWith makes next the entry's current run and returns the one it
// replaces, nil when none is running or waiting.
func (e *entry) replaceWith(next *replaceable) *replaceable {
	e.mu.Lock()
	defer e.mu.Unlock()
	prev := e.current
	e.current = next
	//: the run next waits for, if any.
	return prev
}

// release forgets mine as the entry's current run, unless a newer one has
// taken its place already.
func (e *entry) release(mine *replaceable) {
	e.mu.Lock()
	defer e.mu.Unlock()
	//: a newer run is current: leave it.
	if e.current == mine {
		e.current = nil
	}
}

// arm computes the entry's first due instant from now.
func (e *entry) arm(now time.Time) {
	next, ok := e.schedule(now)
	//: a Schedule that returns a non-advancing instant would make due() spin
	//: forever. Treat it as exhausted: one downstream bug disarms one entry
	//: instead of hanging the whole scheduler.
	e.armed = ok && next.After(now)
	e.next = next
}

// due reports whether the entry fires at now, which due instant the run stands
// for, and how many earlier due instants were dropped to get there.
//
// The scheduler SKIPS missed deadlines rather than replaying them, so when
// several due instants have passed the run is attributed to the LATEST one and
// the earlier ones are counted, not run. Replaying them would turn a machine
// that slept for a day into a burst of stale work at the worst possible
// moment; dropping them silently would make a scheduler that is hours behind
// look exactly like one that is on time.
func (e *entry) due(now time.Time) (scheduled time.Time, missed int, fire bool) {
	//: not armed, or not due yet.
	if !e.armed || e.next.After(now) {
		//: nothing to do for this entry on this pass.
		return time.Time{}, 0, false
	}
	scheduled = e.next
	//: walk forward until the next due instant is genuinely in the future,
	//: counting everything coalesced on the way.
	for {
		next, ok := e.schedule(scheduled)
		//: exhausted, or misbehaving — fire this one and disarm.
		if !ok || !next.After(scheduled) {
			e.armed = false
			//: the last instant the schedule produced still deserves its run.
			return scheduled, missed, true
		}
		//: the first instant in the future becomes the new deadline.
		if next.After(now) {
			e.next = next
			//: fire for the latest past instant.
			return scheduled, missed, true
		}
		//: another instant that has already passed — dropped, and counted.
		scheduled = next
		missed++
	}
}
