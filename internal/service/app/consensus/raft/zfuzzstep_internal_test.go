package raft

import (
	"encoding/binary"
	"fmt"
	"slices"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// FuzzStep feeds the step function arbitrary message sequences under the
// simulator's invariants (#924). Step is a pure function of (state, now,
// message), and the simulator owns the oracle: every input warms a 3-node
// cluster up for 3 s of load, recording every message a node sends, then
// steps its ops, each a recorded message delivered again to its node or
// forged from one, and runs the cluster between them.
//
// A message delivered again is in D15's fault model (duplicated, delayed,
// reordered), so every check holds: one leader per term, the committed
// history and the applied entries agreeing across nodes, vote uniqueness,
// term monotonicity, no disk gap, apply in order, and once the ops are
// done every node converging on one commit. A forged message is outside it
// (a peer that sends what Raft never sends breaks agreement legitimately),
// so after the first one the checks that compare nodes are off and each
// node's own are kept: a vote per term, a term that never goes back, a log
// written without a gap, entries applied in order, no panic. A run that
// never settles fails on the event budget, a Ready that never empties on
// maxReadyTurns.
//
// The file is named to sort after every seed suite (yieldonstall's
// comment). The nightly lane runs it: scripts/ci/consensus-fuzz.sh.
func FuzzStep(f *testing.F) {
	for _, c := range fuzzStepCorpus() {
		f.Add(c)
	}
	f.Fuzz(func(t *testing.T, ops []byte) {
		if err := fuzzStep(ops); err != nil {
			t.Fatal(err)
		}
	})
}

// fuzzOpBytes is the size of one op: the flags and the delay after it, the
// recorded message it names, the field a forgery changes and the value.
const fuzzOpBytes = 8

// fuzzMaxOps bounds the ops one input steps.
const fuzzMaxOps = 256

// fuzzEventBudget is the events one input may dispatch: the warm-up takes
// some 2 500, each op's run up to 127 ms a few dozen, the convergence run
// some 1 500.
const fuzzEventBudget = 200_000

// fuzzKinds are the kinds a forged message may take.
var fuzzKinds = []core.MessageKind{
	core.MsgPreVote, core.MsgPreVoteResp, core.MsgVote, core.MsgVoteResp,
	core.MsgHeartbeat, core.MsgHeartbeatResp, core.MsgTimeoutNow,
	core.MsgReadIndex, core.MsgReadIndexResp, core.MsgForwardAdmin,
	core.MsgForwardAdminResp, core.MsgLeaderRecord, core.MsgApp,
	core.MsgAppResp, core.MsgForward, core.MsgForwardResp,
}

// fuzzStep runs one input and returns the first violation.
func fuzzStep(ops []byte) error {
	s := newSim(simOptions{seed: 1})
	s.budget = fuzzEventBudget
	var trace []core.MessageValue
	s.onSend = func(m core.MessageValue) {
		if len(trace) < 4096 {
			m.Entries = slices.Clone(m.Entries)
			trace = append(trace, m)
		}
	}
	s.play(nil, 3*time.Second, true)
	if s.err != nil {
		return fmt.Errorf("warm-up: %w", s.err)
	}
	for k := 0; len(ops) >= fuzzOpBytes && k < fuzzMaxOps && s.err == nil; k++ {
		op := ops[:fuzzOpBytes]
		ops = ops[fuzzOpBytes:]
		m := trace[int(binary.BigEndian.Uint16(op[1:3]))%len(trace)]
		m.Entries = slices.Clone(m.Entries)
		if op[0]&1 != 0 {
			s.forged = true
			forge(&m, op[3], binary.BigEndian.Uint32(op[4:8]))
		}
		if n := s.nodes[m.To]; n != nil && n.alive {
			n.r.Step(s.clock(n), m)
			s.process(n)
		}
		s.play(nil, time.Duration(op[0]>>1)*time.Millisecond, true)
	}
	if s.err != nil || s.forged {
		return s.err
	}
	// Liveness: with nothing more replayed, every node converges.
	s.play(nil, 5*time.Second, false)
	if s.err != nil {
		return s.err
	}
	for _, id := range s.ids {
		if c, want := s.nodes[id].r.Status().Commit, s.maxCommit(); c != want {
			return fmt.Errorf("node %d stuck at commit %d < %d after the replays", id, c, want)
		}
	}
	return nil
}

// forge changes one field of m, which keeps its sender: the transport
// authenticates From (R7-05).
func forge(m *core.MessageValue, field byte, v uint32) {
	switch field % 8 {
	case 0:
		m.Kind = fuzzKinds[int(v)%len(fuzzKinds)]
	case 1:
		m.Term = shift(m.Term, int64(int8(v)))
	case 2:
		m.Index = shift(m.Index, int64(int16(v)))
	case 3:
		m.LogTerm = shift(m.LogTerm, int64(int8(v)))
	case 4:
		m.Commit = shift(m.Commit, int64(int16(v)))
	case 5:
		forgeEntries(m, v)
	case 6:
		m.Reject = !m.Reject
		m.RejectHint = shift(m.RejectHint, int64(int16(v>>16)))
	default:
		// Another node than the sender receives it.
		to := core.NodeID(1 + v%3)
		if to == m.From {
			to = core.NodeID(1 + (v+1)%3)
		}
		m.To = to
	}
}

// forgeEntries drops, cuts, re-terms or extends m's entries; a payload is
// shared with the recorded message and never written.
func forgeEntries(m *core.MessageValue, v uint32) {
	switch v % 4 {
	case 0:
		m.Entries = nil
	case 1:
		if len(m.Entries) > 0 {
			m.Entries = m.Entries[:len(m.Entries)-1]
		}
	case 2:
		if len(m.Entries) > 0 {
			e := &m.Entries[0]
			e.Position.Term = shift(e.Position.Term, int64(int8(v>>8)))
		}
	default:
		if k := len(m.Entries); k > 0 {
			e := m.Entries[k-1]
			e.Position.Index++
			m.Entries = append(m.Entries, e)
		}
	}
}

// shift adds d to x, at 0 at least.
func shift(x uint64, d int64) uint64 {
	if d < 0 && uint64(-d) > x {
		return 0
	}
	return uint64(int64(x) + d)
}

// fuzzStepCorpus is the seed corpus: no op, replays of the recorded
// trace at several delays, and one forgery of each field.
func fuzzStepCorpus() [][]byte {
	op := func(flags byte, idx uint16, field byte, v uint32) []byte {
		b := []byte{flags, 0, 0, field, 0, 0, 0, 0}
		binary.BigEndian.PutUint16(b[1:3], idx)
		binary.BigEndian.PutUint32(b[4:8], v)
		return b
	}
	out := [][]byte{nil}
	var replays []byte
	for i := range uint16(16) {
		replays = append(replays, op(byte(i%8)<<1, i*37, 0, 0)...)
	}
	out = append(out, replays)
	for field := range byte(8) {
		var b []byte
		for i := range uint16(4) {
			b = append(b, op(1|20<<1, i*101+uint16(field), field, uint32(i)*0x01010101+0xff)...)
		}
		out = append(out, b)
	}
	return out
}
