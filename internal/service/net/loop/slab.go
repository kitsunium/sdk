package loop

import "math/bits"

const (
	// minClassShift is the smallest chunk, 2 KiB.
	minClassShift uint = 11
	// arenaBytes is the least memory one mapping adds to a class: chunks are
	// mapped a quarter of a mebibyte at a time, or one chunk when a chunk is
	// larger.
	arenaBytes int = 256 << 10
	// defaultSlabBudget is the global ceiling on the bytes a loop's
	// connections hold in spill and output together. Past it a Write is
	// refused with LoopOutputFull and an unconsumed tail closes its
	// connection with LoopSpillFull: load is shed by name, never by the
	// kernel's OOM killer.
	defaultSlabBudget int = 128 << 20
	// noChunk marks a chunkRef that holds nothing.
	noChunk int32 = -1
	// shrinkPerTick bounds the arenas one tick unmaps, every class together
	// (#809): 256 arenas of a 64 MiB burst go back within 32 ticks.
	shrinkPerTick int = 8
)

// chunkRef names one chunk of the slab by size class and index, and how
// much of it is used. It holds no pointer, so the connection table that
// carries it stays pointer-free (ADR 0186 §D7).
type chunkRef struct {
	// idx is the chunk's index in its class, noChunk when none.
	idx int32
	// cls is the size class.
	cls uint8
	// off is the first byte still meaningful; len the end of the data.
	off int32
	len int32
}

// empty reports whether the ref holds no chunk.
func (c chunkRef) empty() bool {
	//: noChunk is the only empty value.
	return c.idx == noChunk
}

// slab is a loop's shared store of payload memory, cut into power-of-two
// size classes and mapped outside the Go heap (ADR 0186 §D7): what the
// kernel reads or writes never moves, and the collector scans none of it.
// It belongs to one loop, so it takes no lock.
type slab struct {
	// classes holds each class's arenas and free chunks, smallest first.
	classes []slabClass
	// inUse is the bytes handed out; budget its ceiling.
	inUse  int
	budget int
	// mapper maps a new arena; the platform's mmap, a heap slice elsewhere.
	// unmap releases one, nil when the Go heap owns them.
	mapper func(n int) ([]byte, error)
	unmap  func([]byte) error
	// mappedBytes is the bytes the arenas map now.
	mappedBytes int
}

// slabClass is one size class.
type slabClass struct {
	// size is the chunk size; perArena how many chunks one arena holds.
	size     int
	perArena int
	// arenas are the class's mappings, in index order; nil is an arena
	// unmapped by shrink, its indexes held on holes for the next grow.
	arenas [][]byte
	holes  []int32
	// free is a stack of free chunk indexes.
	free []int32
	// freeIn and dropped are shrink's scratch (#809): each arena's chunks on
	// the free stack, counted on the tick so the alloc and free path keeps no
	// count of its own, and the arenas one pass unmaps.
	freeIn  []int32
	dropped []bool
}

// newSlab builds a slab whose largest class holds maxChunk bytes.
func newSlab(maxChunk, budget int, mapper func(n int) ([]byte, error)) *slab {
	top := classOf(maxChunk)
	s := &slab{budget: budget, mapper: mapper, classes: make([]slabClass, int(top)+1)}
	//: each class's geometry is fixed at construction.
	for i := range s.classes {
		size := 1 << (minClassShift + uint(i))
		s.classes[i] = slabClass{size: size, perArena: max(arenaBytes/size, 1)}
	}
	//: no memory is mapped until a chunk is asked for.
	return s
}

// classOf is the smallest class whose chunk holds n bytes.
func classOf(n int) uint8 {
	//: everything up to the smallest chunk shares it.
	if n <= 1<<minClassShift {
		//: the first class.
		return 0
	}
	//: the power of two at or above n, counted from the smallest.
	return uint8(uint(bits.Len(uint(n-1))) - minClassShift)
}

// alloc returns a chunk holding at least n bytes, or false when n passes the
// largest class, the budget, or the mapping fails.
func (s *slab) alloc(n int) (chunkRef, bool) {
	cls := classOf(n)
	//: past the largest class: the per-connection cap was passed.
	if int(cls) >= len(s.classes) {
		//: refused.
		return chunkRef{idx: noChunk}, false
	}
	c := &s.classes[cls]
	//: the global ceiling sheds load by name; under it, a class with no free
	//: chunk maps one more arena, and a failed mapping refuses too.
	if s.inUse+c.size > s.budget || (len(c.free) == 0 && !s.grow(c)) {
		//: refused.
		return chunkRef{idx: noChunk}, false
	}
	idx := c.free[len(c.free)-1]
	c.free = c.free[:len(c.free)-1]
	s.inUse += c.size
	//: an empty chunk of the class.
	return chunkRef{idx: idx, cls: cls}, true
}

// grow maps one arena for c and pushes its chunks on the free stack.
func (s *slab) grow(c *slabClass) bool {
	mem, err := s.mapper(c.size * c.perArena)
	//: nothing mapped, nothing added.
	if err != nil {
		//: the caller refuses the allocation.
		return false
	}
	s.mappedBytes += len(mem)
	a := int32(len(c.arenas))
	//: a hole shrink left takes the arena first: the indexes stay bounded.
	if n := len(c.holes); n > 0 {
		a = c.holes[n-1]
		c.holes = c.holes[:n-1]
		c.arenas[a] = mem
	} else {
		c.arenas = append(c.arenas, mem)
	}
	base := a * int32(c.perArena)
	//: pushed highest first, so the lowest index is handed out first.
	for i := int32(c.perArena) - 1; i >= 0; i-- {
		c.free = append(c.free, base+i)
	}
	//: the class can hand out chunks again.
	return true
}

// free returns the chunk to its class. A ref that holds nothing is ignored.
func (s *slab) free(r chunkRef) {
	//: nothing to return.
	if r.empty() {
		//: done.
		return
	}
	c := &s.classes[r.cls]
	c.free = append(c.free, r.idx)
	s.inUse -= c.size
}

// shrink unmaps, in each class holding two wholly free arenas or more, every
// one of them but one, at most shrinkPerTick arenas a call, highest index
// first: the loop calls it once a tick, so a burst's arenas go back within
// a second of idle and a class keeps one for the next connection (#809).
func (s *slab) shrink() {
	left := shrinkPerTick
	//: each class, smallest first, while the tick's work lasts.
	for i := range s.classes {
		c := &s.classes[i]
		//: two idle arenas need two arenas' chunks free: no count before.
		if left == 0 || len(c.free) < 2*c.perArena {
			continue
		}
		idle := c.idleArenas()
		//: one idle arena is the hysteresis: kept.
		if idle < 2 {
			continue
		}
		left -= s.shrinkClass(c, min(idle-1, left))
	}
}

// reclaimable reports whether shrink has an arena to give back: a class
// holding two wholly free arenas or more. A loop with nothing timed sleeps
// until an event only once it is false, so a burst's arenas go back however
// quiet the loop falls, and an idle loop wakes for nothing once they have
// (#809).
func (s *slab) reclaimable() bool {
	//: each class.
	for i := range s.classes {
		c := &s.classes[i]
		//: the count is walked only where two arenas' chunks are free.
		if len(c.free) >= 2*c.perArena && c.idleArenas() >= 2 {
			//: something to give back.
			return true
		}
	}
	//: one idle arena a class at most: kept.
	return false
}

// shrinkClass unmaps n of c's idle arenas and drops their chunks from the free
// stack in one pass, and says how many it unmapped.
func (s *slab) shrinkClass(c *slabClass, n int) int {
	done := c.pickIdle(n)
	keep := c.free[:0]
	//: the free stack loses the dropped arenas' chunks, in order.
	for _, idx := range c.free {
		//: kept.
		if !c.dropped[int(idx)/c.perArena] {
			keep = append(keep, idx)
		}
	}
	c.free = keep
	s.unmapDropped(c)
	//: how many went.
	return done
}

// pickIdle marks in dropped up to n idle arenas of c, highest first, and
// says how many.
func (c *slabClass) pickIdle(n int) int {
	c.dropped = scratch(c.dropped, len(c.arenas))
	done := 0
	//: the highest idle arenas first.
	for a := len(c.arenas) - 1; a >= 0 && done < n; a-- {
		//: every chunk of it free, and mapped.
		if int(c.freeIn[a]) == c.perArena && c.arenas[a] != nil {
			c.dropped[a] = true
			done++
		}
	}
	//: marked.
	return done
}

// unmapDropped unmaps every arena dropped marks, each a hole for the next grow.
func (s *slab) unmapDropped(c *slabClass) {
	//: each marked arena.
	for a, d := range c.dropped {
		//: kept.
		if !d {
			continue
		}
		s.mappedBytes -= len(c.arenas[a])
		//: the platform's munmap; the Go heap's needs none.
		if s.unmap != nil {
			swallowErr(s.unmap(c.arenas[a]))
		}
		c.arenas[a] = nil
		c.holes = append(c.holes, int32(a))
	}
}

// scratch is buf cleared at length n, grown only when its capacity is short.
func scratch[T any](buf []T, n int) []T {
	//: grown once.
	if cap(buf) < n {
		buf = make([]T, 0, n)
	}
	buf = buf[:n]
	clear(buf)
	//: zeroed.
	return buf
}

// idleArenas counts, into freeIn, each arena's chunks on the free stack, and
// returns how many mapped arenas have every chunk there: one walk of the free
// stack on the tick, so the alloc and free path keeps no count of its own.
func (c *slabClass) idleArenas() int {
	c.freeIn = scratch(c.freeIn, len(c.arenas))
	//: each free chunk counts for its arena.
	for _, idx := range c.free {
		c.freeIn[int(idx)/c.perArena]++
	}
	n := 0
	//: each mapped arena whose chunks are all free.
	for a, f := range c.freeIn {
		//: idle.
		if int(f) == c.perArena && c.arenas[a] != nil {
			n++
		}
	}
	//: the count.
	return n
}

// release unmaps every arena, as the loop stops.
func (s *slab) release() {
	//: every class.
	for i := range s.classes {
		c := &s.classes[i]
		//: every arena still mapped.
		for a, mem := range c.arenas {
			//: a hole holds nothing.
			if mem != nil && s.unmap != nil {
				swallowErr(s.unmap(mem))
			}
			c.arenas[a] = nil
		}
	}
	s.mappedBytes = 0
}

// bytes returns the whole chunk r names.
func (s *slab) bytes(r chunkRef) []byte {
	c := &s.classes[r.cls]
	a := int(r.idx) / c.perArena
	o := (int(r.idx) % c.perArena) * c.size
	//: capacity pinned to the chunk, so an append can never reach the next one.
	return c.arenas[a][o : o+c.size : o+c.size]
}

// data returns the meaningful bytes of r, off to len.
func (s *slab) data(r chunkRef) []byte {
	//: nothing held, nothing meaningful.
	if r.empty() {
		//: empty.
		return nil
	}
	//: the window the ref describes.
	return s.bytes(r)[r.off:r.len]
}

// size returns the capacity of r's chunk.
func (s *slab) size(r chunkRef) int {
	//: the class decides it.
	return s.classes[r.cls].size
}
