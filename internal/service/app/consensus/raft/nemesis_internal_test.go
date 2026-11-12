package raft

import (
	"slices"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// The nemeses of D15 as values a scenario composes (#924): a schedule says
// "partition {1} from {2,3} for 3 s, stall node 2's disk for 10 s, then skew
// node 3 by 2 %" in one literal, and sim.play runs it. A schedule draws
// nothing from the simulator's generator: what it changes is applied
// between two runs, so a scenario reproduces from its seed exactly as the
// calls it replaces did.

// nemesisKind is the fault a nemesis injects.
type nemesisKind uint8

const (
	// nemPartition cuts its nodes from the rest of the cluster, both ways;
	// its end heals every cut. Two partitions never hold at once.
	nemPartition nemesisKind = iota + 1
	// nemSkew runs the clocks of its nodes at the rate param against true
	// time (1.02 is 2 % fast); its end sets them back to 1.
	nemSkew
	// nemStallDisk stalls the disks of its nodes for its duration: a sync
	// in flight completes, and an install is served, no earlier than the
	// stall's end, and every write issued meanwhile queues behind it (#554).
	nemStallDisk
	// nemCrash crashes its nodes; its end restarts every one still down.
	nemCrash
	// nemDrop drops each message, and each snapshot sent, with the
	// probability param; its end drops none.
	nemDrop
	// nemDup delivers each message a second time with the probability
	// param; its end duplicates none.
	nemDup
	// nemCrashes crashes a random node at param crashes per second across
	// the cluster, each back 0.1 to 1 s later; its end stops them and
	// restarts every node still down.
	nemCrashes
	// nemQuiet holds the load: no command is proposed while it holds.
	nemQuiet
)

// nemesis is one fault of a schedule: kind on nodes, from the schedule's
// start plus from, for dur (0: to the schedule's end), param its
// probability, rate or frequency.
type nemesis struct {
	kind  nemesisKind
	nodes []core.NodeID
	from  time.Duration
	dur   time.Duration
	param float64
}

// schedule is the faults of a scenario's window, in the order they are
// applied when several start, or end, at the same instant: every end
// before every start.
type schedule []nemesis

// end is the instant, from the schedule's start, the nemesis ends at in a
// window of span.
func (nm *nemesis) end(span time.Duration) time.Duration {
	if nm.dur <= 0 || nm.from+nm.dur > span {
		return span
	}
	return nm.from + nm.dur
}

// play runs the simulation for span under sched, proposing a command every
// 20 ms while load holds and no nemQuiet does. The run is cut at every
// instant a nemesis starts or ends, its faults applied there, and every
// nemesis still holding ends at span, so the cluster leaves the window
// healed, on true time, with no random fault and every node up. It stops
// at the first violation, which s.err holds.
func (s *sim) play(sched schedule, span time.Duration, load bool) {
	t0 := s.now
	bounds := []time.Duration{0, span}
	for i := range sched {
		bounds = append(bounds, min(sched[i].from, span), sched[i].end(span))
	}
	slices.Sort(bounds)
	bounds = slices.Compact(bounds)
	quiet := 0
	for k, b := range bounds {
		if k > 0 {
			s.run(t0.Add(b), load && quiet == 0)
		}
		if s.err != nil {
			return
		}
		for i := range sched {
			if nm := &sched[i]; nm.from < span && nm.end(span) == b {
				quiet += s.nemesisEnd(nm)
			}
		}
		for i := range sched {
			if nm := &sched[i]; nm.from < span && nm.from == b {
				quiet += s.nemesisStart(nm, t0.Add(nm.end(span)))
			}
		}
	}
}

// nemesisStart applies nm, which holds until the true instant until, and
// returns the change it makes to the count of nemQuiet holding.
func (s *sim) nemesisStart(nm *nemesis, until time.Time) int {
	switch nm.kind {
	case nemPartition:
		if len(s.cut) > 0 {
			s.fail("schedule: a partition of %v starts while another holds", nm.nodes)
		}
		s.partition(nm.nodes)
	case nemSkew:
		for _, id := range nm.nodes {
			s.setRate(id, nm.param)
		}
	case nemStallDisk:
		for _, id := range nm.nodes {
			s.stallDisk(id, until)
		}
	case nemCrash:
		for _, id := range nm.nodes {
			if s.nodes[id].alive {
				s.crash(id)
			}
		}
	case nemDrop:
		s.opt.drop = nm.param
	case nemDup:
		s.opt.dup = nm.param
	case nemCrashes:
		s.opt.crashRate = nm.param
	case nemQuiet:
		return 1
	default:
		s.fail("schedule: nemesis kind %d unknown", nm.kind)
	}
	return 0
}

// nemesisEnd lifts nm, and returns the change it makes to the count of
// nemQuiet holding.
func (s *sim) nemesisEnd(nm *nemesis) int {
	switch nm.kind {
	case nemPartition:
		s.heal()
	case nemSkew:
		for _, id := range nm.nodes {
			s.setRate(id, 1)
		}
	case nemStallDisk:
		// The stall ends by itself: its instant is on each node.
	case nemCrash:
		for _, id := range nm.nodes {
			if n := s.nodes[id]; !n.alive {
				s.start(n)
			}
		}
	case nemDrop:
		s.opt.drop = 0
	case nemDup:
		s.opt.dup = 0
	case nemCrashes:
		s.opt.crashRate = 0
		for _, id := range s.ids {
			if n := s.nodes[id]; !n.alive {
				s.start(n)
			}
		}
	case nemQuiet:
		return -1
	}
	return 0
}

// stallDisk stalls node id's disk until the true instant until: the syncs
// in flight complete no earlier, and every write issued meanwhile queues
// behind it, as a device whose fdatasync blocks for seconds (#554).
func (s *sim) stallDisk(id core.NodeID, until time.Time) {
	n := s.nodes[id]
	if until.After(n.stallUntil) {
		n.stallUntil = until
	}
	if n.lastDone.Before(n.stallUntil) {
		n.lastDone = n.stallUntil
	}
}
