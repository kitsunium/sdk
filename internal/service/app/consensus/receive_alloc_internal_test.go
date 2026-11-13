//go:build !race

// receive_alloc_internal_test.go — the allocation gate of the receive path
// (#1110): a follower's frame is copied once, by the driver, and Step keeps
// the entries' Data aliasing that copy. Race off, because the race detector
// allocates on every memory access: the alloc lane (tools/alloc-lane-targets.txt,
// //internal/service/app/consensus:consensus_alloc_test) is its only gate. The
// file is self-contained: that target compiles it with allocwindow_test.go
// and filelog_alloc_internal_test.go alone.

package consensus

import (
	"bytes"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// receiveAllocRuns is how many appends a window counts.
const receiveAllocRuns int = 100

// receiveAllocCount is the number of entries of each append frame.
const receiveAllocCount int = 64

// receiveAllocSink keeps each parsed message reachable, so escape analysis
// cannot place the frame's copy on the stack.
var receiveAllocSink coreconsensus.MessageValue

// appendFrames are n encoded MsgApp frames from leader 1 to follower 2 at
// term 1, each of receiveAllocCount chained command entries of payload bytes,
// one after the other from index 1 on.
func appendFrames(n, payload int) [][]byte {
	frames := make([][]byte, 0, n)
	var chain uint32
	var index uint64
	for range n {
		m := coreconsensus.MessageValue{
			Version: 1, Group: 1, Kind: coreconsensus.MsgApp, From: 1, To: 2, Term: 1, Index: index,
		}
		if index > 0 {
			m.LogTerm = 1
		}
		for range receiveAllocCount {
			index++
			d := bytes.Repeat([]byte{byte(index)}, payload)
			e := coreconsensus.EntryValue{
				Position: coreconsensus.PositionValue{Term: 1, Index: index},
				Kind:     coreconsensus.EntryCommand, BodySum: wire.BodySum(d), Data: d,
			}
			e.ChainSum = wire.ChainSum(chain, &e)
			chain = e.ChainSum
			m.Entries = append(m.Entries, e)
		}
		frames = append(frames, wire.AppendMessage(nil, &m))
	}
	return frames
}

// receiveFollower is node 2 of members 1, 2, 3, empty, at term 1: what the
// frames of appendFrames append to.
func receiveFollower(now time.Time) *raft.Raft {
	var conf coreconsensus.MembershipValue
	for _, id := range []coreconsensus.NodeID{1, 2, 3} {
		conf.Members = append(conf.Members, coreconsensus.MemberValue{ID: id, Voter: true, SPKI: [32]byte{byte(id)}})
	}
	return raft.NewRaft(&raft.Config{
		ID: 2, Group: 1, Seed: 2,
		Timing: coreconsensus.TimingContractSpec{
			Generation: 1, HeartbeatInterval: 100 * time.Millisecond,
			ElectionTimeoutMin: time.Second, ElectionTimeoutMax: 2 * time.Second, MaxClockDrift: 0.01,
		},
		MaxInflight: 8, MaxInflightBytes: 1 << 20, MaxBatchBytes: 1 << 20, MinVoters: 1, PromoteLag: 16,
		CanVote: true, MaxChainRefusals: 3, RecentEntriesBytes: 1 << 30, MaxApplyLagBytes: 1 << 30,
		MaxEntryBytes: 1 << 20, MaxForwardBytes: 1 << 20,
	}, &raft.StartState{
		HardState: coreconsensus.HardState{Term: 1}, Snapshot: coreconsensus.SnapshotMetaValue{Membership: conf}, Now: now,
	})
}

// receiveAllocs counts what the receive path of the driver allocates over
// receiveAllocRuns appends of payload bytes an entry: the frame copied out of
// the transport's buffer and parsed (deliver), then stepped by a follower and
// its write acknowledged (the loop and the storage actor).
func receiveAllocs(t *testing.T, payload int) windowCount {
	t.Helper()
	frames := appendFrames(receiveAllocRuns+1, payload)
	now := time.Unix(1_700_000_000, 0)
	r := receiveFollower(now)
	next := 0
	var last uint64
	got := mallocsOver(receiveAllocRuns, func() {
		var m coreconsensus.MessageValue
		if err := wire.ParseMessage(bytes.Clone(frames[next]), &m); err != nil {
			t.Fatalf("frame %d: %v", next, err)
		}
		next++
		receiveAllocSink = m
		r.Step(now, m)
		rd := r.Ready()
		if k := len(rd.Entries); k > 0 {
			last = rd.Entries[k-1].Position.Index
			r.StorageDone(rd.Seq, last, rd.Entries[k-1].Position.Term)
		}
	})
	if last != uint64(next*receiveAllocCount) {
		t.Fatalf("the follower wrote through %d after %d appends of %d, want every entry", last, next, receiveAllocCount)
	}
	return got
}

// A follower's Step of a MsgApp of 64 entries copied every payload a second
// time (appendNew's ownCopy) out of the frame the driver had already copied
// out of the transport's buffer: 65 allocations an append for its payloads
// (#1110). Step keeps the entries' Data as the caller's gift now: the frame's
// copy and the entry slice ParseMessage makes are the only allocations the
// payloads cost, and a payload of 256 bytes costs Step what an empty one does.
// ParseMessage sizes the entry slice once from the count the frame states,
// where it grew it by doubling: seven allocations for 64 entries. Mutation-
// checked: restoring ownCopy in appendNew was measured at 64 more allocations
// an append (6 400 over the 100 appends), and the slice grown entry by entry
// at 800 allocations over the 100 parses.
func TestAFollowerStepKeepsTheFramesPayloads(t *testing.T) {
	frame := appendFrames(1, 256)[0]
	parse := mallocsOver(receiveAllocRuns, func() {
		var m coreconsensus.MessageValue
		if err := wire.ParseMessage(bytes.Clone(frame), &m); err != nil {
			t.Fatal(err)
		}
		receiveAllocSink = m
	})
	reportWindow(t, "a MsgApp of 64 x 256 B copied and parsed", parse)
	if max := uint64(2 * receiveAllocRuns); parse.own > max {
		t.Fatalf("%d frames copied and parsed allocated %d times, over %d (the copy and the entry slice)", receiveAllocRuns, parse.own, max)
	}
	empty := receiveAllocs(t, 0)
	reportWindow(t, "appends of 64 empty entries received", empty)
	loaded := receiveAllocs(t, 256)
	reportWindow(t, "appends of 64 x 256 B received", loaded)
	if loaded.own > empty.own {
		t.Fatalf("%d appends of 64 x 256 B allocated %d times, %d more than of empty payloads: Step copied what the frame holds",
			receiveAllocRuns, loaded.own, loaded.own-empty.own)
	}
}
