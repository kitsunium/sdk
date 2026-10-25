package server

import (
	"strconv"
	"sync"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/concur/worker"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// loopStallGrace is how long a loop group may stay stalled — every ring of
	// it drained by its watchdog — before the server's liveness check fails
	// (ADR 0188, R1). Serve returns at once; the grace is for a process that
	// keeps running without Serve, so an orchestrator restarts it.
	loopStallGrace time.Duration = 30 * time.Second
	// stallPoll is how often the loop groups' rings are read for a stall.
	stallPoll time.Duration = time.Second
	// runbook is what a fault about a drained ring tells the operator to set.
	runbook string = "set " + corenet.EnvDisable + "=" + string(corenet.CapabilityIOURing) + " before restarting"
)

// The words FaultValue.Source takes for what the server reports itself.
const (
	// faultListener is a degraded listener.
	faultListener string = "listener"
	// faultLoop is a loop group on a lower backend than it asked for.
	faultLoop string = "loop"
	// faultRing is one ring drained while others still accept.
	faultRing string = "ring"
	// faultStall is a loop group every ring of which is drained.
	faultStall string = "stall"
)

// stallWatch is the state of the stall watcher: the first group seen stalled
// and when, and the channel Serve selects on.
type stallWatch struct {
	// mu guards group and at.
	mu sync.Mutex
	// group is the first loop group seen stalled; empty while none is.
	group string
	// at is when it was first seen.
	at time.Time
	// once closes stalled once.
	once sync.Once
	// stalled is closed when a group is first seen stalled.
	stalled chan struct{}
	// daemon ticks the watcher while the server serves; nil without loop groups.
	daemon *worker.LoopDaemon
}

// groupStalled reports whether every ring of a loop group is drained: a
// native backend whose watchdog gave up on each of its rings. The portable
// backend has no ring and never stalls.
func groupStalled(st corenet.LoopStateValue) bool {
	//: no ring, no watchdog.
	if len(st.RingStates) == 0 {
		//: not stalled.
		return false
	}
	//: one ring still accepting is enough.
	for _, r := range st.RingStates {
		//: this one accepts.
		if !r.Draining {
			//: not stalled.
			return false
		}
	}
	//: every ring drained.
	return true
}

// startStallWatch ticks the watcher on the engine's clock while the server
// has loop groups. Shutdown and Close stop it.
func (s *Server) startStallWatch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: nothing to watch without a loop group, or already watching.
	if len(s.loopEngines) == 0 || s.stall.daemon != nil {
		return
	}
	s.stall.daemon = worker.Every(stallPoll, s.checkStalls, worker.WithClock(s.clk))
}

// stopStallWatch stops the watcher, if it runs, and joins it.
func (s *Server) stopStallWatch() {
	s.mu.Lock()
	daemon := s.stall.daemon
	s.mu.Unlock()
	//: never started.
	if daemon == nil {
		return
	}
	daemon.Stop()
}

// checkStalls reads every loop group while the server serves, and records the
// first one stalled: Serve returns LoopGroupStalled, and liveness fails once
// loopStallGrace has passed. A drain stops every ring on purpose, so nothing
// is read outside PhaseServing.
func (s *Server) checkStalls() {
	//: a drain is not a stall.
	if corenet.Phase(s.phase.Load()) != corenet.PhaseServing {
		return
	}
	//: the loop groups, in start order.
	for _, st := range s.loopStates() {
		//: this group accepts on some ring.
		if !groupStalled(st) {
			continue
		}
		s.markStalled(st.Group)
		return
	}
}

// markStalled records group as stalled, once: the first stall is the one
// Serve returns and the grace counts from.
func (s *Server) markStalled(group string) {
	s.stall.mu.Lock()
	//: only the first.
	if s.stall.group == "" {
		s.stall.group = group
		s.stall.at = s.clk.Now()
	}
	s.stall.mu.Unlock()
	s.stall.once.Do(func() { close(s.stall.stalled) })
}

// stalledGroup is the first loop group seen stalled and when; empty while none.
func (s *Server) stalledGroup() (string, time.Time) {
	s.stall.mu.Lock()
	defer s.stall.mu.Unlock()
	//: a copy.
	return s.stall.group, s.stall.at
}

// stallError is LoopGroupStalled naming group.
func stallError(group string) error {
	//: by name.
	return errs.Wrap(corenet.LoopGroupStalled, errs.WrapParams{}, errs.String("group", group))
}

// faults lists what st says the server runs without: the environment's unknown
// tokens, the degraded listeners and loop groups, and, while serving, each
// drained ring and each stalled group (ADR 0188).
func (s *Server) faults(st corenet.StateValue) []corenet.FaultValue {
	out := make([]corenet.FaultValue, 0, len(s.operator.faults))
	out = append(out, s.operator.faults...)
	//: every degraded listener.
	for _, l := range st.Listeners {
		//: this one fell back.
		if l.Degraded {
			out = append(out, corenet.FaultValue{Source: faultListener, Group: l.Group, Reason: l.DegradedReason})
		}
	}
	serving := st.Phase == corenet.PhaseServing
	//: every loop group.
	for _, lp := range st.Loops {
		//: on a lower backend than it asked for.
		if lp.Degraded {
			out = append(out, corenet.FaultValue{Source: faultLoop, Group: lp.Group, Reason: lp.DegradedReason})
		}
		//: a drain stops every ring on purpose.
		if serving {
			out = append(out, ringFaults(lp)...)
		}
	}
	//: none: nil, as a report with nothing to say has always been.
	if len(out) == 0 {
		//: no fault.
		return nil
	}
	//: the faults, environment first.
	return out
}

// ringFaults is one fault per drained ring of lp, or one stall fault when every
// ring is drained (R1).
func ringFaults(lp corenet.LoopStateValue) []corenet.FaultValue {
	//: every ring drained: the group accepts nothing.
	if groupStalled(lp) {
		//: one fault for the group.
		return []corenet.FaultValue{{
			Source: faultStall, Group: lp.Group,
			Reason: "every ring drained by its watchdog: the group accepts nothing; " + runbook,
		}}
	}
	var out []corenet.FaultValue
	//: each drained ring, while others still accept.
	for _, r := range lp.RingStates {
		//: this one drained.
		if r.Draining {
			out = append(out, corenet.FaultValue{
				Source: faultRing, Group: lp.Group,
				Reason: "loop " + strconv.Itoa(r.Loop) + " ring " + strconv.Itoa(r.Ring) + " drained by its watchdog; " + runbook,
			})
		}
	}
	//: possibly none.
	return out
}
