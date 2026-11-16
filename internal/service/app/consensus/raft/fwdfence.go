package raft

import (
	"cmp"
	"slices"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

const (
	// fwdAdmitted: the forward may be handled.
	fwdAdmitted fwdVerdict = iota
	// fwdRestarted: its term is at most the one this node started at.
	fwdRestarted
	// fwdEvicted: its term is at most the highest of its node's marks the
	// fence forgot.
	fwdEvicted
	// maxFwdRuns is how many forwarder runs the fence keeps a mark of before
	// it forgets those of terms below this node's (fwdFence.evict). A mark
	// of the current term is kept past it: forgotten, its node's fence
	// would rise to the current term, and its node's next run would be
	// refused until the next election, which a healthy leader never calls
	// (#1482, the safety review of its fix C, D1).
	maxFwdRuns int = 1 << 12
	// maxFwdRunsHard bounds the marks of the current term: past it, the run
	// handled least recently of the node that holds the most goes, and
	// that node's new runs wait for the next term. The marks are per lane,
	// about 32 768 restarts when each run forwards on both lanes; restarts
	// spread over several nodes fence each of them in turn, the most loaded
	// node changing as the counts level out (#1540).
	maxFwdRunsHard int = 1 << 16
)

// fwdRun names one run of a forwarder on one lane: the node, the epoch its
// run drew for the high 32 bits of its submission ids (R7-23), and whether
// its forwards are administrative, which travel on the control lane while
// commands travel on the data lane, each lane a connection of its own.
type fwdRun struct {
	from  core.NodeID
	epoch uint32
	admin bool
}

// fwdMark is what the fence knows of a run: the highest counter, the low 32
// bits of a submission id, of a forward of the run this node handled, the
// highest term such a forward carried, and when it was last handled, the
// fence's count of the forwards it recorded then.
type fwdMark struct {
	high uint32
	term uint64
	used uint64
}

// fwdFence is the safety of the deduplication (#1482): a forward this node
// handled is never handled again, whatever the delay of its copy, its
// answer kept or not. A forwarder draws its submission ids in the order it
// sends them, a counter under its run's epoch, and a lane delivers them in
// that order; every copy of a frame goes to the one node it was addressed
// to. So a forward whose counter is not above the highest this node handled
// of its run is a copy of one it handled — or one its lane delivered late,
// behind a later one, which only a lane's change of connection does (a
// write that failed, a link replaced at its age or its silence): either
// is neither proposed nor answered, and its forwarder answers its caller
// OUTCOME_UNKNOWN if it still waits.
//
// A run the fence keeps no mark of is admitted only past two terms. The
// first is start, the term of this node's durable hard state when it
// started, and it rests on what a forwarder addresses: Node.forward sends a
// forward to the leader its status names, stamped with that status's term,
// and a node names a leader only once it heard an append or a heartbeat of
// that leader's term, its leader forgotten at every change of term; so a
// forward to this node always carries a term this node led, and a node
// leads only a term durable before it leads. Every forward a previous run
// of this node handled, answered or not, therefore carried a term at most
// start — whether the answer left before a later term was durable or not
// (TestAForwardToANodeCarriesATermItLed). The second is, per forwarder
// node, the highest term of its marks the fence forgot.
type fwdFence struct {
	marks map[fwdRun]fwdMark
	start uint64
	nodes map[core.NodeID]uint64
	used  uint64
}

// fwdVerdict is what the fence says of a forward of a run it keeps no mark
// of.
type fwdVerdict uint8

// runOf is the run of the forward m and its counter.
func runOf(m *core.MessageValue) (fwdRun, uint32) {
	return fwdRun{from: m.From, epoch: uint32(m.Context >> 32), admin: m.Kind == core.MsgForwardAdmin}, uint32(m.Context)
}

// admits reports whether the forward m may be handled: its counter above
// the highest handled of its run, or, for a run with no mark, its term past
// the fence's terms.
func (f *fwdFence) admits(m *core.MessageValue) bool {
	run, n := runOf(m)
	if k, ok := f.marks[run]; ok {
		return n > k.high
	}
	return m.Term > f.start && m.Term > f.nodes[m.From]
}

// unmarked is the verdict on the forward m, of a run with no mark.
func (f *fwdFence) unmarked(m *core.MessageValue) fwdVerdict {
	switch {
	case m.Term <= f.start:
		return fwdRestarted
	case m.Term <= f.nodes[m.From]:
		return fwdEvicted
	default:
		return fwdAdmitted
	}
}

// note records that the forward m was handled. A run new to the fence
// first makes room past soft marks: evict, at the term current.
func (f *fwdFence) note(m *core.MessageValue, soft, hard int, current uint64) {
	run, n := runOf(m)
	k, ok := f.marks[run]
	if !ok {
		if f.marks == nil {
			f.marks = map[fwdRun]fwdMark{}
		}
		if len(f.marks) >= soft {
			f.evict(soft, hard, current)
		}
	}
	f.used++
	f.marks[run] = fwdMark{high: max(k.high, n), term: max(k.term, m.Term), used: f.used}
}

// evict brings the marks under soft, forgetting those of a term below
// current, the oldest term first and, within a term, the one handled least
// recently; the marks of the current term stay, so that no node's fence
// rises to the current term and refuses a run of it, until hard marks are
// kept: then the mark handled least recently of the node that holds the
// most goes, the lowest node on a tie. Each mark forgotten raises its
// node's fence to the mark's term, so that run's copies stay refused. The
// choices are the same whatever the map's order: no two marks share a
// count of use.
func (f *fwdFence) evict(soft, hard int, current uint64) {
	var old []fwdRun
	for run, k := range f.marks {
		if k.term < current {
			old = append(old, run)
		}
	}
	slices.SortFunc(old, func(a, b fwdRun) int {
		ka, kb := f.marks[a], f.marks[b]
		if c := cmp.Compare(ka.term, kb.term); c != 0 {
			return c
		}
		return cmp.Compare(ka.used, kb.used)
	})
	for _, run := range old[:min(len(old), len(f.marks)-soft+1)] {
		f.forget(run)
	}
	if len(f.marks) >= hard {
		f.forget(f.crowded())
	}
}

// crowded is the mark handled least recently of the node that holds the
// most marks, the lowest node on a tie.
func (f *fwdFence) crowded() fwdRun {
	return f.leastRecent(f.crowdedNode())
}

// crowdedNode is the node that holds the most marks, the lowest on a tie.
func (f *fwdFence) crowdedNode() core.NodeID {
	count := map[core.NodeID]int{}
	for run := range f.marks {
		count[run.from]++
	}
	var node core.NodeID
	for id, c := range count {
		if c > count[node] || (c == count[node] && id < node) {
			node = id
		}
	}
	return node
}

// leastRecent is node's mark handled least recently: no two marks share a
// count of use.
func (f *fwdFence) leastRecent(node core.NodeID) fwdRun {
	var out fwdRun
	var at uint64
	for run, k := range f.marks {
		if run.from == node && (at == 0 || k.used < at) {
			out, at = run, k.used
		}
	}
	return out
}

// forget drops run's mark and raises its node's fence to the mark's term:
// every forward of the run carried a term at most that one.
func (f *fwdFence) forget(run fwdRun) {
	k := f.marks[run]
	delete(f.marks, run)
	if f.nodes == nil {
		f.nodes = map[core.NodeID]uint64{}
	}
	f.nodes[run.from] = max(f.nodes[run.from], k.term)
}
