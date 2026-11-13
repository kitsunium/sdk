//go:build !race

// The fixtures of the step function's performance contracts
// (design/sdk.yaml, budgets; ADR 0152 D15, issue #921): each sets one call
// up and returns it, and the gen_perf_test.go kit gen writes beside this
// file counts its allocations against its budget. Race off, since the race
// detector allocates on its own.
//
// What a fixture's call does beyond Step is the driver's part, done in
// place: the outbox the call queues into is emptied without being given
// back (Ready hands a new one each turn, and that allocation is Ready's,
// not Step's), and the log is let go of what the call appended once it is
// counted, as a compaction would.
package raft

import (
	"bytes"
	"testing"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// perfAcks is how many acknowledgements StepAppResp's leader can take, one
// index further each: more than the warm-up and the window of the
// generated test together.
const perfAcks = 70_000

// perfBatch and perfEntryBytes shape the append StepApp64's follower takes:
// D15's 64 entries of 256 bytes.
const (
	perfBatch      = 64
	perfEntryBytes = 256
)

// drainOutbox empties r's outbox in place.
func drainOutbox(r *Raft) {
	r.rd.Messages = r.rd.Messages[:0]
	r.rd.AfterSync = r.rd.AfterSync[:0]
	r.rd.Entries = r.rd.Entries[:0]
}

// perfStepAppResp is the fixture of Step for a leader handling one
// AppendEntries response in a pipeline: a leader of term 2 over perfAcks
// entries, every entry committed through peer 3, and peer 2 replicating
// with its window full, acknowledging the oldest append in flight at each
// call — its match moves, a slot frees, and the leader sends the next
// append. Its batches are cut to one entry (MaxBatchBytes 1) so the log
// holds one per call: what a response costs does not depend on how many
// entries the batch it sends carries.
func perfStepAppResp(tb testing.TB) func() {
	terms := make([]uint64, perfAcks)
	for i := range terms {
		terms[i] = 1
	}
	l := leaderOf(2, 0, chainEntries(0, 1, terms...), 0, false)
	l.cfg.MaxBatchBytes = 1
	last := l.log.lastIndex()
	ack := msg(core.MsgAppResp, 3, 1, 2)
	ack.Index = last
	l.Step(l.now, ack)
	ack = msg(core.MsgAppResp, 2, 1, 2)
	ack.Index, ack.Reject, ack.RejectHint = last-1, true, 1
	l.Step(l.now, ack)
	ack = msg(core.MsgAppResp, 2, 1, 2)
	ack.Index = 1
	l.Step(l.now, ack)
	for range l.cfg.MaxInflight {
		l.sendAppend(2, false)
	}
	rd := l.Ready()
	l.StorageDone(rd.Seq, last, 2)
	l.Ready()
	if pr := l.prs[2]; pr.state != stateReplicate || len(pr.inflight) != l.cfg.MaxInflight || l.log.committed != last {
		tb.Fatalf("setup: peer 2 %+v, commit %d of %d", *pr, l.log.committed, last)
	}
	next := uint64(2)
	return func() {
		ack.Index = min(next, last)
		next++
		l.Step(l.now, ack)
		drainOutbox(l)
	}
}

// perfStepApp64 is the fixture of Step for a follower handling an
// AppendEntries of perfBatch entries of perfEntryBytes: each call appends
// the next batch after the last, chained, and the follower's log then lets
// go of it as a compaction through it would, keeping its capacity as a log
// that grows does, so the next call appends into an empty log again.
func perfStepApp64(tb testing.TB) func() {
	f := newTest(2, testState{term: 2}, 0, false)
	data := bytes.Repeat([]byte{'x'}, perfEntryBytes)
	m := msg(core.MsgApp, 1, 2, 2)
	m.Entries = make([]core.EntryValue, perfBatch)
	sum := wire.BodySum(data)
	return func() {
		m.Index, m.LogTerm = f.log.lastIndex(), f.log.lastTerm()
		chain := f.log.chain(m.Index)
		for i := range m.Entries {
			e := &m.Entries[i]
			*e = core.EntryValue{
				Position: core.PositionValue{Term: 2, Index: m.Index + uint64(i) + 1},
				Kind:     core.EntryCommand, BodySum: sum, Data: data,
			}
			e.ChainSum = wire.ChainSum(chain, e)
			chain = e.ChainSum
		}
		f.Step(t0, m)
		if f.log.lastIndex() != m.Index+perfBatch {
			tb.Fatalf("append after %d left the log at %d", m.Index, f.log.lastIndex())
		}
		drainOutbox(f)
		kept := f.log.ents[:0]
		f.log.stable = f.log.lastIndex()
		f.log.compactTo(f.log.lastIndex(), 2, chain)
		f.log.ents = kept
	}
}
