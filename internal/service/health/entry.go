// Package health — hosts the registered check: what the registry remembers
// about it between probes, and the three accesses that read and move that
// memory.
package health

import (
	"context"
	"sync"
	"time"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
)

// entry is one registered check plus everything the registry remembers about
// it between probes.
//
// The two bodies are mutually exclusive: run for startup and readiness,
// runSelf for liveness. That asymmetry is the domain's central rule made
// concrete — a liveness body has no context, so it cannot be handed a
// dependency API (see core/health.SelfCheck).
type entry struct {
	// name identifies the check in every result and error field.
	name string
	// probe says which of the three questions this check answers.
	probe corehealth.Probe
	// budget is this check's resolved timeout, already clamped.
	budget time.Duration
	// nonCritical downgrades a failure to degraded. Readiness only: the other
	// two registration types do not carry the field at all.
	//
	// It is written once, at registration, and read afterwards WITHOUT mu —
	// see [entry.verdict], which runs on the measuring goroutine outside every
	// critical section. That is why it is a field of its own rather than a bit
	// sharing a word with the mutable `passed` below.
	nonCritical bool
	// maxAge is the cache window for a SUCCESS, zero for no cache. Readiness
	// only.
	maxAge time.Duration
	// latching keeps a success forever rather than for maxAge. Startup only:
	// a startup check that passed has answered its question for good, and
	// re-running it at steady state would repeat a one-shot.
	latching bool
	// run is the startup / readiness body.
	run corehealth.Check
	// runSelf is the liveness body.
	runSelf corehealth.SelfCheck

	// mu guards the three fields below, and is NEVER held while a check body
	// runs — a check that blocks must block only itself.
	//
	// It is an RWMutex because [entry.fresh] is a pure read that runs on every
	// probe of every check, while the two writers run once per measurement.
	// fresh copies the cached result out and mutates only that copy; nothing
	// under the read lock touches the entry.
	mu sync.RWMutex
	// active is the run outstanding right now, or nil. At most one exists per
	// check at any instant; see [entry.claim].
	active *inflight
	// cached is the last success kept for replay, or nil. A failure is never
	// stored here. It is REPLACED wholesale by [entry.finish] and never
	// mutated in place, which is what lets concurrent readers share it.
	cached *corehealth.ResultValue
	// passed records that a latching check has succeeded once, so the startup
	// counter is decremented exactly once however many probes race.
	passed bool
}

// fresh returns a replayable answer for this check, if one is held.
//
// Only successes are ever held. A failure is re-measured on every probe,
// because the answer an operator needs promptly is the one that says the
// outage ended — and because a cached failure would keep a recovered replica
// out of rotation for a window nobody chose for that purpose.
func (e *entry) fresh(now time.Time) (result corehealth.ResultValue, ok bool) {
	//: a read lock, and it is a real one: every write below is to `replay`,
	//: which is this caller's own copy of the cached result.
	e.mu.RLock()
	defer e.mu.RUnlock()
	//: nothing measured yet, or the last measurement failed.
	if e.cached == nil {
		//: no replay; measure.
		return corehealth.ResultValue{}, false
	}
	replay := *e.cached
	//: a latching (startup) success answers for good — see entry.latching.
	if e.latching {
		//: replay it, dated, forever.
		replay.Cached = true
		//: a question already answered once is not asked again.
		return replay, true
	}
	//: outside the caller's window the answer stops being current.
	if e.maxAge <= 0 || replay.Age(now) > e.maxAge {
		//: too old to stand behind; measure again.
		return corehealth.ResultValue{}, false
	}
	replay.Cached = true
	//: a dated statement, inside the window the caller asked for.
	return replay, true
}

// claim returns the run this probe will wait on, and whether this caller is
// the one that must start it.
//
// A probe arriving while a run is outstanding JOINS it rather than starting a
// second one or reporting a timeout it has not waited for. Two overlapping
// probes therefore see one measurement, and a wedged check is measured by one
// goroutine no matter how long the outage lasts.
func (e *entry) claim(parent context.Context) (run *inflight, mine bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	//: somebody is already measuring; wait on their answer.
	if e.active != nil {
		//: join, and do not start a second body.
		return e.active, false
	}
	//: detached from the probe's own context: a client that hangs up must not
	//: cancel a measurement other probes are waiting on.
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	e.active = &inflight{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	//: this caller owns starting the body.
	return e.active, true
}

// finish records the outcome of a run and reports whether it just latched a
// startup check for the first time.
//
// It clears active AFTER the result is published, so a probe arriving in
// between joins a run that is already complete and gets its answer at once.
func (e *entry) finish(result corehealth.ResultValue) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active = nil
	//: two ways to keep nothing, and both mean the next probe measures. A
	//: failure is never cached (see entry.fresh), and a check with neither a
	//: window nor a latch asked for no cache at all.
	if result.Status != corehealth.StatusHealthy || (!e.latching && e.maxAge <= 0) {
		//: nothing to replay.
		return false
	}
	stored := result
	e.cached = &stored
	//: the counter must move exactly once however many probes raced here.
	latchedNow := e.latching && !e.passed
	e.passed = e.latching || e.passed
	//: tell the registry whether the startup set just shrank.
	return latchedNow
}
