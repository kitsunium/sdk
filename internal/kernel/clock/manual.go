// Package clock — hosts ManualClock, the deterministic [Timed] whose instant
// only moves when a caller moves it.
package clock

import (
	"sync"
	"time"
)

// ManualClock is a [Timed] whose instant moves ONLY when the caller moves it,
// with [ManualClock.Advance] or [ManualClock.Set]. Every wait it hands out —
// After, NewTimer, NewTicker, Sleep — is due at an absolute instant on this
// clock, and fires exactly when the caller drives the clock past that instant.
// No wall-clock time passes, so a test asserts cadence and ordering instead of
// sleeping and hoping.
//
// It is a concrete struct, not an interface, for the same reason
// recycler.Pool and snapshot.Value are: there is one implementation, and a
// single-impl interface would be over-abstraction. Always used behind the
// pointer returned by [NewManualClock] — the embedded mutex and cond must not
// be copied.
//
// Safe for concurrent use: every field is guarded by mu, and each wait's
// channel is created once at registration and never reassigned.
//
// Two hazards, both inherent to a clock that only a caller can move:
//
//   - [ManualClock.Sleep] blocks until ANOTHER goroutine advances the clock.
//     A test that sleeps on its own goroutine with nobody to advance hangs
//     until the test binary's timeout. Sleep on the code under test, advance
//     from the test.
//   - Advance wakes a waiter but does not schedule it. When the assertion
//     depends on the woken goroutine having run, synchronise on something the
//     goroutine itself signals; [ManualClock.BlockUntil] closes the mirror
//     race (advancing before the goroutine has registered its wait).
type ManualClock struct {
	// mu guards every field below and is the lock cond is built on. It is an
	// RWMutex so the read-only accessors do not serialise a test's readers
	// against each other while a driving goroutine is between advances.
	mu sync.RWMutex
	// cond is broadcast whenever the wait set changes, so BlockUntil wakes.
	cond *sync.Cond
	// now is the clock's current instant.
	now time.Time
	// waits holds every armed wait, in registration order.
	waits []*manualWait
	// seq numbers registrations so waits due at the same instant fire in a
	// deterministic order (earliest registration first).
	seq uint64
}

// NewManualClock returns a ManualClock reading start. Any instant is legal —
// before the Unix epoch, after 2038, in any location — which is the point:
// unlike testing/synctest's bubble clock, the origin is the caller's choice.
func NewManualClock(start time.Time) *ManualClock {
	//: build first, then wire the cond to this instance's write lock.
	mc := &ManualClock{now: start}
	//: cond and mu must refer to the same lock or BlockUntil deadlocks.
	mc.cond = sync.NewCond(&mc.mu)
	//: hand back the pointer; the value must never be copied.
	return mc
}

// Now returns the clock's current instant.
func (m *ManualClock) Now() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	//: time.Time is a value — returning a copy needs no further guarding.
	return m.now
}

// Since returns the duration between t and the clock's current instant. It can
// be negative when t is in the clock's future, exactly like time.Since.
func (m *ManualClock) Since(t time.Time) time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	//: subtract against the controlled instant, never the wall clock.
	return m.now.Sub(t)
}

// After returns a channel delivering the fire instant once d has elapsed on
// this clock. A non-positive d delivers before After returns.
func (m *ManualClock) After(d time.Duration) <-chan time.Time {
	//: identical to the stdlib's definition: After is NewTimer(d).C.
	return m.NewTimer(d).C()
}

// NewTimer returns a one-shot [Timer] due d from the clock's current instant.
// A non-positive d is already elapsed and fires before NewTimer returns.
func (m *ManualClock) NewTimer(d time.Duration) Timer {
	//: a zero period marks the wait as one-shot.
	return &manualTimer{clk: m, w: m.register(d, 0)}
}

// NewTicker returns a repeating [Ticker] of period d, panicking on a
// non-positive d with the same message [System] uses.
func (m *ManualClock) NewTicker(d time.Duration) Ticker {
	//: refuse the arbitrary-cadence case before allocating anything.
	requirePositivePeriod("NewTicker", d)
	//: a positive period is both the first deadline and the cadence.
	return &manualTicker{clk: m, w: m.register(d, d)}
}

// Sleep blocks the calling goroutine until d has elapsed on this clock — which
// means until another goroutine advances it. A non-positive d returns at once.
func (m *ManualClock) Sleep(d time.Duration) {
	//: a sleep is a wait nobody can cancel; block on the timer's channel.
	<-m.After(d)
}

// Advance moves the clock forward by d, firing every wait that comes due, in
// deadline order. It panics on a negative d: rewinding is [ManualClock.Set]'s
// job and would otherwise silently mean "un-fire the timers I already fired".
//
// Each fired wait receives its own deadline, not the post-advance instant, so
// a receiver reads the moment it was scheduled for. A ticker delivers AT MOST
// ONE tick per Advance and re-arms past the new instant: the delivery channel
// holds one tick anyway, and the alternative — one send per elapsed period —
// makes Advance(time.Hour) on a nanosecond ticker an unbounded loop. To
// observe N ticks, call Advance(period) N times, draining between calls.
func (m *ManualClock) Advance(d time.Duration) {
	//: a negative advance has no honest meaning for already-fired waits.
	if d < 0 {
		//: point at the operation that does support moving backwards.
		panic("clock: ManualClock.Advance requires d >= 0, got " + d.String() + " (use Set to move time backwards)")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	//: walk forward to the target, firing everything due on the way.
	m.advanceTo(m.now.Add(d))
}

// Set moves the clock to t. Forward, it behaves exactly like Advance and fires
// every wait that comes due. Backward, it moves the instant and fires nothing:
// deadlines are absolute, so a rewound clock re-reaches them and the waits fire
// then. That models a wall-clock jump — an NTP step, a VM restore — which is
// something testing/synctest's monotonic bubble clock cannot express.
func (m *ManualClock) Set(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	//: a rewind changes the reading without retracting or firing anything.
	if t.Before(m.now) {
		//: pending deadlines stay where they are and fire on the way back.
		m.now = t
		//: nothing came due; skip the fire pass entirely.
		return
	}
	//: forward is the ordinary advance path.
	m.advanceTo(t)
}

// Pending returns the number of armed waits. A fired one-shot timer is no
// longer pending; a stopped timer or ticker is not pending; a live ticker is.
func (m *ManualClock) Pending() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	//: the slice holds exactly the armed waits — see link/retire.
	return len(m.waits)
}

// BlockUntil blocks until at least n waits are armed. It closes the race a
// manual clock otherwise has with the code it drives: the test would advance
// before the goroutine under test had registered its timer, and the wake would
// be lost. It is the manual counterpart of synctest.Wait — coarser, because it
// counts registrations instead of detecting durable blocking, but usable in a
// plain parallel test.
//
// It blocks forever if the count is never reached; bound the test with the
// binary's own timeout rather than an arbitrary one here.
func (m *ManualClock) BlockUntil(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	//: cond.Wait releases mu while parked and re-acquires it on wake.
	for len(m.waits) < n {
		//: re-check on every broadcast; spurious wakes are allowed.
		m.cond.Wait()
	}
}

// register arms a new wait due d from now with the given ticker period, firing
// it at once when it is already due.
func (m *ManualClock) register(d, period time.Duration) *manualWait {
	m.mu.Lock()
	defer m.mu.Unlock()
	//: capacity 1 so Advance's send never blocks, and never queues past one.
	w := &manualWait{
		ch:       make(chan time.Time, 1),
		deadline: m.now.Add(d),
		period:   period,
		seq:      m.seq,
	}
	//: registration order breaks deadline ties deterministically.
	m.seq++
	m.link(w)
	//: a non-positive d is already elapsed — deliver now instead of waiting
	//: for an Advance, so After(0) behaves the same on both clocks.
	if !w.deadline.After(m.now) {
		//: the fire instant is the clock's reading, which is past the deadline.
		fireWait(w, m.now)
		//: a one-shot that has fired is no longer armed.
		m.retire(w)
	}
	//: hand the wait to the Timer/Ticker facade that owns it.
	return w
}

// advanceTo moves now to target, firing due waits in (deadline, seq) order.
// Caller holds mu for writing.
func (m *ManualClock) advanceTo(target time.Time) {
	//: fire one wait per turn; each fire either retires or re-arms it, so the
	//: due set strictly shrinks and the loop terminates.
	for {
		w := m.nextDue(target)
		//: nothing else comes due before target.
		if w == nil {
			//: settle the clock at the requested instant.
			m.now = target
			//: the advance is complete.
			return
		}
		//: time stops at the firing instant, so a wait registered during this
		//: pass is measured from the deadline rather than from target.
		m.now = w.deadline
		fireWait(w, w.deadline)
		//: a ticker survives its tick; a timer does not.
		if w.period > 0 {
			//: skip straight past target so a tiny period stays O(1).
			rearmWait(w, target)
		} else {
			//: a one-shot leaves the armed set until Reset re-arms it.
			m.retire(w)
		}
	}
}

// nextDue returns the earliest wait due at or before target, breaking ties by
// registration order, or nil when none is due. Caller holds mu.
func (m *ManualClock) nextDue(target time.Time) *manualWait {
	//: best tracks the incumbent earliest-due wait across the scan.
	var best *manualWait
	//: linear scan — a test holds a handful of waits, not a heap's worth.
	for _, w := range m.waits {
		//: a deadline after target has not come due yet.
		if w.deadline.After(target) {
			//: skip it; a later Advance will pick it up.
			continue
		}
		//: keep the earliest deadline, and the earliest registration on a tie.
		if best == nil || w.deadline.Before(best.deadline) || (w.deadline.Equal(best.deadline) && w.seq < best.seq) {
			//: this wait fires before the incumbent.
			best = w
		}
	}
	//: nil means the due set is empty for this target.
	return best
}

// link arms w. Caller holds mu for writing, and MUST pass a disarmed wait:
// register builds a fresh one and resetWait retires first, so linking twice —
// which would duplicate the entry in the due scan — cannot happen. The
// invariant is stated rather than guarded, because a guard here would be a
// branch no test can reach and no reader can trust.
func (m *ManualClock) link(w *manualWait) {
	//: append and mark, then wake anyone counting registrations.
	w.linked = true
	m.waits = append(m.waits, w)
	//: BlockUntil is waiting on exactly this count.
	m.cond.Broadcast()
}

// retire disarms w, removing it from the armed set. Caller holds mu for
// writing.
func (m *ManualClock) retire(w *manualWait) {
	//: an already-disarmed wait is not in the slice.
	if !w.linked {
		//: nothing to remove.
		return
	}
	//: mark first so the flag and the slice never disagree mid-removal.
	w.linked = false
	//: remove by identity; the surviving order stays registration order so a
	//: debugger view of waits keeps reading in the sequence it was built.
	for i, cur := range m.waits {
		//: pointer identity is the only key a wait has.
		if cur == w {
			//: splice it out and stop scanning.
			m.waits = append(m.waits[:i], m.waits[i+1:]...)
			//: the count changed; wake anyone counting registrations.
			m.cond.Broadcast()
			//: done.
			return
		}
	}
}

// stopWait disarms w and reports whether it was armed, draining any pending
// delivery when drainPending is set.
func (m *ManualClock) stopWait(w *manualWait, drainPending bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	//: the report is "was it still armed", read before the mutation.
	was := w.linked
	m.retire(w)
	//: timers promise no stale value survives Stop (Go 1.23+); tickers
	//: deliberately keep a delivered tick so a receiver sees no zero value.
	if drainPending {
		//: clear the one-slot buffer.
		drainWait(w)
	}
	//: hand back the pre-mutation state.
	return was
}

// resetWait re-arms w for d from now and reports whether it was armed,
// draining any pending delivery when drainPending is set. A ticker's period is
// replaced by d; a timer's stays zero.
func (m *ManualClock) resetWait(w *manualWait, d time.Duration, drainPending bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	//: the report mirrors Timer.Reset: "was it still armed".
	was := w.linked
	m.retire(w)
	//: same stale-value rule as stopWait.
	if drainPending {
		//: clear the one-slot buffer before re-arming.
		drainWait(w)
	}
	//: only a ticker carries a cadence to replace.
	if w.period > 0 {
		//: the caller already validated d through requirePositivePeriod.
		w.period = d
	}
	//: the new deadline is relative to the clock's current instant.
	w.deadline = m.now.Add(d)
	m.link(w)
	//: a non-positive d is due immediately, exactly as at registration.
	if !w.deadline.After(m.now) {
		//: deliver and disarm in the same pass.
		fireWait(w, m.now)
		m.retire(w)
	}
	//: hand back the pre-mutation state.
	return was
}
