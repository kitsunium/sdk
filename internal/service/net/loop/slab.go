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
	mapper func(n int) ([]byte, error)
	// mapped is every mapping, for release.
	mapped [][]byte
}

// slabClass is one size class.
type slabClass struct {
	// size is the chunk size; perArena how many chunks one arena holds.
	size     int
	perArena int
	// arenas are the class's mappings, in index order.
	arenas [][]byte
	// free is a stack of free chunk indexes.
	free []int32
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
	s.mapped = append(s.mapped, mem)
	base := int32(len(c.arenas) * c.perArena)
	c.arenas = append(c.arenas, mem)
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
