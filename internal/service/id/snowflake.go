// Package id — snowflake (Twitter-style) stateful generator.
package id

import (
	"os"
	"strconv"
	"sync"

	coreid "github.com/kitsunium/sdk/internal/core/id"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

const (
	// snowflakeEpoch is the custom epoch (2024-01-01T00:00:00Z in ms) the 41-bit
	// timestamp counts from, extending the usable range past the Unix epoch.
	snowflakeEpoch int64 = 1_704_067_200_000
	// nodeBits is the width of the node field (10 bits → 1024 nodes).
	nodeBits uint = 10
	// seqBits is the width of the per-millisecond sequence field (12 bits).
	seqBits uint = 12
	// maxNode is the inclusive maximum node id.
	maxNode int64 = (1 << nodeBits) - 1
	// maxSeq is the inclusive maximum per-millisecond sequence.
	maxSeq int64 = (1 << seqBits) - 1
	// timeShift positions the timestamp field above node + sequence.
	timeShift uint = nodeBits + seqBits
	// nodeShift positions the node field above the sequence.
	nodeShift uint = seqBits
	// decimalBase renders the 63-bit id as a base-10 string.
	decimalBase int = 10
	// fnvOffset is the FNV-1a 64-bit offset basis.
	fnvOffset uint64 = 14695981039346656037
	// fnvPrime is the FNV-1a 64-bit prime.
	fnvPrime uint64 = 1099511628211
)

// Snowflake is the default-node snowflake generator, registered for the
// "snowflake" scheme. Its node id is derived from the hostname+pid so distinct
// processes rarely collide; use NewSnowflake for an explicit node assignment.
var Snowflake = coreid.Register(newSnowflake(defaultNode(), clock.System))

// snowflakeGen is a stateful Twitter-style snowflake generator: a 41-bit ms
// timestamp, a 10-bit node, and a 12-bit per-ms sequence, packed into a
// positive int64 rendered in decimal.
type snowflakeGen struct {
	mu     sync.Mutex
	clk    clock.Clock
	node   int64
	lastMS int64
	seq    int64
}

// NewSnowflake returns a snowflake Generator bound to node (reduced into the
// 10-bit node space). It is NOT added to the global registry — bind it to your
// own variable.
func NewSnowflake(node int64) coreid.Generator {
	//: clamp the node into the 10-bit space and use the system clock.
	return newSnowflake(node&maxNode, clock.System)
}

// newSnowflake is the shared constructor (clock injectable for tests).
func newSnowflake(node int64, clk clock.Clock) *snowflakeGen {
	//: a fresh generator starts before any issued timestamp.
	return &snowflakeGen{clk: clk, node: node & maxNode}
}

// Scheme implements core/id.Generator.
func (*snowflakeGen) Scheme() coreid.Scheme {
	//: the registered scheme key.
	return "snowflake"
}

// New issues the next monotonic snowflake id. Within a millisecond the sequence
// increments; on overflow it spins to the next millisecond. A backwards clock
// jump returns ClockBackwards rather than minting a non-monotonic id.
func (g *snowflakeGen) New() (newID string, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: milliseconds since the custom epoch.
	now := g.clk.Now().UnixMilli() - snowflakeEpoch
	//: a regressed clock would break monotonicity — refuse honestly.
	if now < g.lastMS {
		//: surface the typed clock-backwards sentinel.
		return "", ClockBackwards
	}
	//: same millisecond → advance the sequence; spin to next ms on overflow.
	if now == g.lastMS {
		//: increment within the 12-bit sequence space.
		g.seq = (g.seq + 1) & maxSeq
		//: a wrapped sequence means this ms is exhausted — wait for the next.
		if g.seq == 0 {
			//: the wait runs under g.mu, so it must be bounded — see tillNext.
			next, waitErr := g.tillNext(now)
			//: a regression during the wait cannot be waited out.
			if waitErr != nil {
				//: restore the sequence to its pre-wrap value. Leaving it at 0
				//: would let a retry inside this same millisecond hand out
				//: seq=1, which was already issued — a duplicate id. At maxSeq
				//: the retry wraps again and re-enters the bounded wait.
				g.seq = maxSeq
				//: surface the clock fault instead of spinning forever.
				return "", waitErr
			}
			//: the clock ticked over; issue in the new millisecond.
			now = next
		}
	} else {
		//: a new millisecond resets the sequence.
		g.seq = 0
	}
	//: record the issuing millisecond.
	g.lastMS = now
	//: pack timestamp | node | sequence into a positive int64.
	packed := (now << timeShift) | (g.node << nodeShift) | g.seq
	//: render in decimal.
	return strconv.FormatInt(packed, decimalBase), nil
}

// tillNext spins until the clock advances past prev (same-ms sequence
// overflow), returning the new epoch-relative millisecond.
//
// The wait is bounded by construction: the caller holds g.mu for the whole
// spin, so an unbounded loop would stall every concurrent New. A clock that
// regresses BELOW prev can never satisfy `now > prev` by waiting — only by the
// operator fixing the clock — so that case returns ClockBackwards rather than
// spinning. Staying AT prev is the normal sub-millisecond remainder and keeps
// spinning.
func (g *snowflakeGen) tillNext(prev int64) (next int64, err error) {
	//: busy-wait the sub-millisecond remainder until the clock ticks over.
	for {
		//: re-read the epoch-relative millisecond.
		now := g.clk.Now().UnixMilli() - snowflakeEpoch
		//: return as soon as we are strictly past the exhausted millisecond.
		if now > prev {
			//: the next millisecond is available.
			return now, nil
		}
		//: a regression below the exhausted millisecond is unrecoverable here.
		if now < prev {
			//: fail honestly instead of holding g.mu forever.
			return 0, ClockBackwards
		}
	}
}

// defaultNode derives a stable 10-bit node id from the hostname and pid via an
// inline FNV-1a (avoids the hash.Hash error channel).
func defaultNode() int64 {
	//: hostname is best-effort; an error yields an empty seed (still valid).
	host, hostErr := os.Hostname()
	//: an unavailable hostname degrades to the pid-only seed.
	if hostErr != nil {
		//: empty host still produces a valid (pid-derived) node.
		host = ""
	}
	//: fold hostname + pid into a 64-bit FNV-1a hash.
	seed := host + ":" + strconv.Itoa(os.Getpid())
	//: start from the FNV-1a offset basis.
	sum := fnvOffset
	//: mix each byte (XOR then multiply) — the FNV-1a step.
	for i := range len(seed) {
		//: XOR the byte into the low bits.
		sum ^= uint64(seed[i])
		//: multiply by the FNV prime.
		sum *= fnvPrime
	}
	//: reduce the hash into the 10-bit node space.
	return int64(sum) & maxNode
}
