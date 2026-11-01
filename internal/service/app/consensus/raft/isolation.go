package raft

import (
	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// The causes an IsolationValue carries.
const (
	// IsolationStranded is set on a voter #603's guard finds stranded: it no
	// longer answers, or lags, and no member of any configuration it may hold
	// would answer its PreVote with this leader's record once it is back, so
	// it would hear no one. The guard refuses every removal or move of a
	// member that answers and may vouch, until it is back or removed.
	IsolationStranded IsolationFlags = 1 << iota
	// IsolationPinDenied is set beside IsolationStranded when a pin DenyPin
	// withdrew is what leaves the voter so: counted back, it would be
	// reached. A DenyPin is never refused for what it leaves a stopped voter
	// with: a key's fence does not wait for a voter that is down (#773).
	IsolationPinDenied
	// IsolationLeaderMoved is set on a voter the guard reaches only through
	// this leader, which the voter knows at another address: the guard
	// counts the leader at any address, since it dials the voter while it
	// leads, but the voter asks the address it knows, and once the
	// leadership moves to a member it does not know, nothing answers it. The
	// leader's own move, and the change that relies on it, are admitted
	// (#765, the owner's option (b)).
	IsolationLeaderMoved
	// IsolationLeaderUnconfirmed is set on a voter the guard reaches only on
	// the bet a compacted log makes: listed in the snapshot's base after
	// this leader, it knows this leader, but whether as a voter, which it
	// sends its PreVote to, or as a learner, the log no longer says. Known
	// as a learner, once the leadership moves to a member it does not know,
	// nothing answers it. The change that relies on it is admitted (#765,
	// the owner's option (b)).
	IsolationLeaderUnconfirmed
)

// IsolationValue is a voter this leader may never reach again, and why
// (#773, #765): one line of Isolated, or of GuardRefusal.
type IsolationValue struct {
	// ID is the voter.
	ID core.NodeID
	// Flags are why: IsolationStranded, IsolationPinDenied,
	// IsolationLeaderMoved, IsolationLeaderUnconfirmed.
	Flags IsolationFlags
}

// IsolationFlags are why a voter may never be reached again, one bit each,
// read with Has.
type IsolationFlags uint8

// Has reports whether every cause of x is set.
func (f IsolationFlags) Has(x IsolationFlags) bool { return f&x == x }

// Isolated returns the voters of the configuration this leader governs by
// that #603's guard finds stranded, or reaches only on one of its bets on
// this leader, with what leaves each so, in configuration order; nil off
// the leader. It judges them as the guard would judge a change to that
// configuration, and decides nothing.
func (r *Raft) Isolated() []IsolationValue {
	if r.role != core.RoleLeader {
		return nil
	}
	cur := r.conf()
	var out []IsolationValue
	for _, v := range r.judged(cur) {
		if f := r.isolation(cur, v); f != 0 {
			out = append(out, IsolationValue{ID: v, Flags: f})
		}
	}
	return out
}

// GuardRefusal returns what #603's guard found when it refused the change
// the last Propose was handed: each voter that change would strand, with
// IsolationPinDenied when a DenyPin left it so already. nil when the guard
// did not refuse that change: refused on another ground, or admitted.
func (r *Raft) GuardRefusal() []IsolationValue {
	return r.guardRefusal
}

// isolation is what leaves voter v of next isolated, or at risk of it, as
// the guard counts its vouchers (reaches), 0 when nothing does: stranded,
// or reached only on one of the guard's bets on this leader.
func (r *Raft) isolation(next *core.MembershipValue, v core.NodeID) IsolationFlags {
	if r.reachedBy(next, v) {
		var f IsolationFlags
		if !r.reaches(next, v, reach{leaderWhereKnown: true}) {
			f |= IsolationLeaderMoved
		}
		if !r.reaches(next, v, reach{noBaseBet: true}) {
			f |= IsolationLeaderUnconfirmed
		}
		return f
	}
	f := IsolationStranded
	if r.reaches(next, v, reach{undenied: true}) {
		f |= IsolationPinDenied
	}
	return f
}
