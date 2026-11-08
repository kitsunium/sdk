package buffer

import (
	"math/bits"

	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
)

// initialCap is the capacity Get asks for: the smallest class, chosen to hold
// a single log line without a re-alloc.
const initialCap int = 1024

// classes is how many size classes the pool keeps: 1, 4, 16, 64 and 256 KiB.
const classes int = 5

// smallestShift is log2 of the smallest class's size (1 KiB); each class is
// four times the one before it.
const smallestShift int = 10

// pools holds one recycler.CappedPool per class. Each pool's factory makes a
// buffer of exactly its class's size, and keeps a returned buffer only while
// its capacity is at most twice that size: a buffer grown past its class is
// dropped, so a class never pins more than twice what it hands out. Storing
// *[]byte (rather than []byte) avoids boxing the slice header into
// sync.Pool's any payload on every Put, which is what keeps the hot path
// allocation-free.
var pools = newPools()

// newPools builds the classes' pools, smallest first.
func newPools() [classes]*recycler.CappedPool[*[]byte] {
	var out [classes]*recycler.CappedPool[*[]byte]
	//: one pool per class, its factory and threshold sized from the class.
	for k := range out {
		size := classSize(k)
		out[k] = recycler.NewCappedPool[*[]byte](
			func() *[]byte { return new(make([]byte, 0, size)) },
			func(b *[]byte) { *b = (*b)[:0] },
			func(b *[]byte) int { return cap(*b) },
			2*size,
		)
	}
	//: the five pools.
	return out
}

// classSize is class k's size: 1 KiB times four to the k.
func classSize(k int) int {
	//: each class is two bits above the one before it.
	return 1 << (smallestShift + 2*k)
}

// classFor is the smallest class whose size is at least n, or classes when
// none is: n above the largest class.
func classFor(n int) int {
	//: the smallest class serves every request up to its size.
	if n <= initialCap {
		return 0
	}
	//: ceil(log4(n / 1 KiB)), from the bit length of n-1.
	return (bits.Len(uint(n-1)) - smallestShift + 1) / 2
}

// classOf is the largest class whose size is at most capacity — the class a
// returned buffer belongs to —, or -1 when capacity is below the smallest.
func classOf(capacity int) int {
	//: a buffer smaller than every class serves none of them.
	if capacity < initialCap {
		return -1
	}
	//: floor(log4(capacity / 1 KiB)), capped at the largest class, whose
	//: threshold then drops what is too large.
	return min((bits.Len(uint(capacity))-smallestShift-1)/2, classes-1)
}

// Get borrows a reusable byte buffer with zero length and capacity >= initialCap.
func Get() *[]byte {
	//: the smallest class.
	return pools[0].Get()
}

// GetSize borrows a reusable byte buffer with zero length and capacity at
// least n, from the smallest class that fits: 1, 4, 16, 64 or 256 KiB. A
// buffer is never smaller than its class. Above 256 KiB the buffer is
// allocated to measure, and Put drops it unless its capacity is at most 512
// KiB. An n of zero or less is the smallest class.
func GetSize(n int) *[]byte {
	k := classFor(n)
	//: no class is large enough: allocated to measure, as the caller would have.
	if k >= classes {
		return new(make([]byte, 0, n))
	}
	//: the class's pool, or its factory on a miss.
	return pools[k].Get()
}

// Put returns a buffer to the pool after use: routed by its capacity to the
// largest class it covers, its length truncated, and dropped when its
// capacity is past twice that class's size or below the smallest class. A
// nil argument is a safe no-op so call sites can always defer Put — the
// generic CappedPool cannot nil-check a pointer-like T, so the guard lives
// here.
//
// Put ends the caller's ownership of b. Once it returns, neither *b nor the
// bytes it points at may be read or written — not even len(*b) — and b must
// not be Put again: the pool may already have handed b to another goroutine,
// which writes both as its own. Copy out whatever is still needed before Put.
func Put(b *[]byte) {
	//: nil-safe no-op so callers can defer Put unconditionally.
	if b == nil {
		//: nothing to recycle; let any allocation fall to the GC.
		return
	}
	//: the routing stays out of line, so Put itself stays inlinable.
	putClass(b)
}

// putClass routes b by its capacity to the largest class it covers; the
// class's recycler drops it past twice the class's size.
func putClass(b *[]byte) {
	//: the smallest class first: the logger's line, the common return.
	if c := cap(*b); c >= initialCap && c <= 2*initialCap {
		pools[0].Put(b)
		return
	}
	k := classOf(cap(*b))
	//: smaller than every class: left to the GC.
	if k < 0 {
		return
	}
	//: the class it fits.
	pools[k].Put(b)
}
