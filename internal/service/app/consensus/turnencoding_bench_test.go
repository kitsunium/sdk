package consensus

// The bytes of a leader turn, #912's second criterion (#1364): "bytes
// allocated per batch divided by the follower count". The file is
// gazelle-excluded and compiled into the `manual`, race-off target
// `consensus_bench_test` (rule 12), whose generator writes its ENC rows
// into BENCH.md; the A/B lane (`cenc`) reads both of its paths in the head.
// It is self-contained: its entries, its appends and its transports are its
// own.

import (
	"bytes"
	"fmt"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// A leader turn of D15's batch: 64 entries of 256 B, the same slice of the
// leader's log sent to every follower in replicate state.
const (
	teEntryCount = 64
	teEntryBytes = 256
)

// teFollowers are the follower counts measured.
var teFollowers = []int{2, 4}

// BenchmarkLeaderTurnEncoding measures what a leader turn of 64 entries of
// 256 B to N followers allocates, through the driver's sendMessages:
//
//   - shared: through a BatchSender, the path since #912 — one body for
//     every follower, the Ready's headers in one buffer;
//   - perpeer: through a transport without the sibling, which the driver
//     still sends AppendMessage's whole frame per follower, each encoded
//     from nil — the path before #912, byte for byte.
//
// Both rows are the head's, so the ratio of their B/op is the batch's bytes
// divided by what N encodings cost, against the bound 1/N.
func BenchmarkLeaderTurnEncoding(b *testing.B) {
	for _, shared := range []bool{true, false} {
		for _, followers := range teFollowers {
			b.Run(fmt.Sprintf("%s/followers=%d", tePath(shared), followers), func(b *testing.B) {
				benchTurnEncoding(b, followers, shared)
			})
		}
	}
}

// tePath names a path of BenchmarkLeaderTurnEncoding.
func tePath(shared bool) string {
	if shared {
		return "shared"
	}
	return "perpeer"
}

// benchTurnEncoding is one row of BenchmarkLeaderTurnEncoding, which the
// generator also runs alone: one op is one turn's sendMessages.
func benchTurnEncoding(b *testing.B, followers int, shared bool) {
	ents := make([]coreconsensus.EntryValue, teEntryCount)
	for i := range ents {
		data := bytes.Repeat([]byte{byte(i)}, teEntryBytes)
		ents[i] = coreconsensus.EntryValue{
			Position: coreconsensus.PositionValue{Term: 3, Index: 100 + uint64(i)},
			Kind:     coreconsensus.EntryCommand, Data: data, BodySum: wire.BodySum(data),
		}
	}
	ms := make([]coreconsensus.MessageValue, followers)
	for i := range ms {
		ms[i] = coreconsensus.MessageValue{
			Kind: coreconsensus.MsgApp, From: 1, To: coreconsensus.NodeID(i + 2), Term: 3, LogTerm: 3,
			Index: ents[0].Position.Index - 1, Commit: 99, Entries: ents,
		}
	}
	n := &Node{cfg: Config{Transport: teDiscard{}}, unfed: map[coreconsensus.NodeID]bool{}}
	if shared {
		tr := teDiscardShared{}
		n.cfg.Transport, n.batch = tr, tr
	}
	b.ReportAllocs()
	for b.Loop() {
		n.sendMessages(ms)
	}
}

// teDiscard is a transport without the BatchSender sibling that keeps
// nothing: what a turn allocates is the driver's alone. Only Send is
// called.
type teDiscard struct{ coreconsensus.Transport }

func (teDiscard) Send(coreconsensus.NodeID, []byte) {}

// teDiscardShared is teDiscard with the sibling.
type teDiscardShared struct{ teDiscard }

func (teDiscardShared) SendShared(coreconsensus.NodeID, []byte, []byte) {}
