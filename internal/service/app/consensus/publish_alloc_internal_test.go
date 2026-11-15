//go:build !race

// publish_alloc_internal_test.go — the allocation gates of the leader's
// loop turn beside #912's encoding (sendshared_alloc_internal_test.go): what
// every turn publishes, and the frames of the messages without entries. Race
// off, because the race detector allocates on every memory access: the alloc
// lane (tools/alloc-lane-targets.txt,
// //internal/service/app/consensus:consensus_alloc_test) is their only gate.
// The file is self-contained: that target compiles it with
// allocwindow_test.go and the gates it lists.

package consensus

import (
	"bytes"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// publishAllocTurns is how many turns a window counts.
const publishAllocTurns int = 200

// publishFrameSink keeps each frame reachable, as the transport's queue
// does, so escape analysis cannot place it on the stack.
var publishFrameSink []byte

// publishNode is the part of a node publish reads: the step function of
// node 1 of members 1, 2, 3, its instruments with no meter, its clock and
// its log's identity.
func publishNode() *Node {
	var conf coreconsensus.MembershipValue
	for _, id := range []coreconsensus.NodeID{1, 2, 3} {
		conf.Members = append(conf.Members, coreconsensus.MemberValue{
			ID: id, Voter: true, SPKI: [32]byte{byte(id)}, Slot: "site", Address: "127.0.0.1:1",
		})
	}
	r := raft.NewRaft(&raft.Config{
		ID: 1, Group: 1, Seed: 1,
		Timing: coreconsensus.TimingContractSpec{
			Generation: 1, HeartbeatInterval: 100 * time.Millisecond,
			ElectionTimeoutMin: time.Second, ElectionTimeoutMax: 2 * time.Second, MaxClockDrift: 0.01,
		},
		MaxInflight: 8, MaxInflightBytes: 1 << 20, MaxBatchBytes: 1 << 20, MinVoters: 1, PromoteLag: 16,
		CanVote: true, MaxChainRefusals: 3, RecentEntriesBytes: 1 << 30, MaxApplyLagBytes: 1 << 30,
		MaxEntryBytes: 1 << 20, MaxForwardBytes: 1 << 20,
	}, &raft.StartState{
		HardState: coreconsensus.HardState{Term: 1}, Snapshot: coreconsensus.SnapshotMetaValue{Membership: conf},
		Now: time.Unix(1_700_000_000, 0),
	})
	n := &Node{r: r, obs: &obs{}, clk: clock.System}
	n.ident.Store(&coreconsensus.LogIdentityValue{})
	return n
}

// TestATurnThatChangesNothingPublishesWithoutAllocating is the alloc lane's
// gate of the loop's publication (#912): publish runs at every turn, and a
// turn that changed neither the status nor the configuration allocates
// nothing there. raft's Summary clones no membership, SameConf compares the
// configuration in force with the one published in place, and the status is
// moved to the heap only once it changed. Mutation-checked: publishing
// through Status, which clones both memberships, failed it with 400
// allocations over the 200 turns, and building the status on the heap
// before comparing it with 200.
func TestATurnThatChangesNothingPublishesWithoutAllocating(t *testing.T) {
	n := publishNode()
	n.publish()
	if n.status.Load() == nil || n.conf.Load() == nil {
		t.Fatal("setup: the first turn published no status or no configuration")
	}
	got := mallocsOver(publishAllocTurns, n.publish)
	reportWindow(t, "unchanged publication", got)
	if got.own != 0 {
		t.Errorf("%d turns that changed nothing performed %d allocations in publish, want 0", publishAllocTurns, got.own)
	}
}

// TestAFrameWithoutEntriesIsEncodedInOneAllocation holds the frames the
// driver encodes whole, the messages without entries — a heartbeat and its
// answer, an append that carries only the commit, the acknowledgements and
// votes that wait for a write: one allocation of their exact length each,
// and AppendMessage's bytes, which a node of the previous release decodes.
// Mutation-checked: the frame grown from nil, as before, performed 900
// allocations over the 200 frames of a window, up to 128 bytes for 112.
func TestAFrameWithoutEntriesIsEncodedInOneAllocation(t *testing.T) {
	type tc struct {
		name string
		m    coreconsensus.MessageValue
	}
	cases := []tc{
		{name: "heartbeat", m: coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgHeartbeat, From: 1, To: 2, Term: 3, Commit: 9, Context: 7}},
		{name: "append of the commit alone", m: coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgApp, From: 1, To: 2, Term: 3, LogTerm: 3, Index: 9, Commit: 9}},
		{name: "acknowledgement", m: coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgAppResp, From: 2, To: 1, Term: 3, Index: 9}},
		{name: "refusal with its hint", m: coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgAppResp, From: 2, To: 1, Term: 3, Index: 9, Reject: true, RejectHint: 4}},
		{name: "vote", m: coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgVoteResp, From: 2, To: 1, Term: 4}},
	}
	runCase := func(t *testing.T, c tc) {
		frame := entrylessFrame(&c.m)
		if want := wire.AppendMessage(nil, &c.m); !bytes.Equal(frame, want) {
			t.Fatalf("frame %x, AppendMessage's %x", frame, want)
		}
		if len(frame) != wire.MessageHeaderLen || cap(frame) != wire.MessageHeaderLen {
			t.Fatalf("frame of %d bytes in %d, want %d in %d", len(frame), cap(frame), wire.MessageHeaderLen, wire.MessageHeaderLen)
		}
		got := mallocsOver(publishAllocTurns, func() { publishFrameSink = entrylessFrame(&c.m) })
		reportWindow(t, "frame without entries", got)
		if want := uint64(publishAllocTurns); got.own != want {
			t.Errorf("%d frames performed %d allocations, want %d (one each)", publishAllocTurns, got.own, want)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
