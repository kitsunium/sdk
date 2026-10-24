package loop

import "time"

const (
	// tickEvery is the timer wheel's resolution: a deadline fires no earlier
	// than asked and at most one tick late.
	tickEvery time.Duration = 10 * time.Millisecond
	// wheelSlots is the wheel's circumference, a power of two; a deadline
	// further than wheelSlots ticks away waits in its bucket for its round.
	wheelSlots int64 = 1024
	// noLink ends a bucket list.
	noLink int32 = -1
)

// wheel is a hashed timer wheel over one ring's connection slots: one timer
// per slot, armed at an absolute tick, in index-linked buckets, so it holds
// no pointer and arming costs no allocation.
type wheel struct {
	// buckets holds the first slot of each bucket.
	buckets []int32
	// next and prev link a bucket's slots; at is each slot's armed tick, zero
	// when unarmed.
	next []int32
	prev []int32
	at   []int64
	// now is the last tick the wheel advanced to.
	now int64
	// due collects the slots one advance fires, reused across advances: the
	// buckets are unlinked before any fires, so a callback that arms or
	// disarms another slot never walks a list under the wheel's feet.
	due []int32
}

// newWheel builds a wheel for n slots, its clock at tick now.
func newWheel(n int, now int64) *wheel {
	w := &wheel{
		buckets: make([]int32, wheelSlots),
		next:    make([]int32, n),
		prev:    make([]int32, n),
		at:      make([]int64, n),
		now:     now,
	}
	//: every bucket starts empty.
	for i := range w.buckets {
		w.buckets[i] = noLink
	}
	//: ready.
	return w
}

// arm sets slot's timer to fire at tick, replacing any previous one. A tick
// at or before now fires on the next advance.
func (w *wheel) arm(slot int32, tick int64) {
	w.disarm(slot)
	//: never in the past, so the bucket walked next holds it.
	tick = max(tick, w.now+1)
	w.at[slot] = tick
	b := tick & (wheelSlots - 1)
	w.prev[slot] = noLink
	w.next[slot] = w.buckets[b]
	//: linked in front of the bucket's list.
	if h := w.buckets[b]; h != noLink {
		w.prev[h] = slot
	}
	w.buckets[b] = slot
}

// disarm clears slot's timer; an unarmed slot is left as it is.
func (w *wheel) disarm(slot int32) {
	at := w.armed(slot)
	//: nothing armed.
	if at == 0 {
		//: done.
		return
	}
	b := at & (wheelSlots - 1)
	//: unlinked from its neighbours, or from the bucket's head.
	if p := w.prev[slot]; p != noLink {
		w.next[p] = w.next[slot]
	} else {
		w.buckets[b] = w.next[slot]
	}
	//: the follower now follows the predecessor.
	if n := w.next[slot]; n != noLink {
		w.prev[n] = w.prev[slot]
	}
	w.at[slot] = 0
}

// grow extends the wheel to n slots, the new ones unarmed.
func (w *wheel) grow(n int) {
	//: only ever grows.
	for len(w.at) < n {
		w.next = append(w.next, noLink)
		w.prev = append(w.prev, noLink)
		w.at = append(w.at, 0)
	}
}

// armed reports slot's armed tick, zero when none.
func (w *wheel) armed(slot int32) int64 {
	//: the stored tick.
	return w.at[slot]
}

// advance moves the wheel to tick now and calls fire for every slot whose
// tick has come, disarming it first, so fire may arm it again. It walks each
// bucket passed once at most.
func (w *wheel) advance(now int64, fire func(slot int32)) {
	//: time never runs backwards for the wheel.
	if now <= w.now {
		//: nothing due.
		return
	}
	from := w.now + 1
	//: past a whole turn every bucket is due once.
	if now-from >= wheelSlots {
		from = now - wheelSlots + 1
	}
	w.now = now
	w.due = w.due[:0]
	//: one bucket per tick passed; due slots are unlinked as they are found.
	for t := from; t <= now; t++ {
		b := t & (wheelSlots - 1)
		s := w.buckets[b]
		//: a bucket mixes this round's slots with later rounds'.
		for s != noLink {
			nxt := w.next[s]
			//: due: unlinked now, fired once the walk is over.
			if w.at[s] <= now {
				w.disarm(s)
				w.due = append(w.due, s)
			}
			s = nxt
		}
	}
	//: fired in the order found; a callback may arm or disarm any slot.
	for _, s := range w.due {
		fire(s)
	}
}
