//go:build !race

// The fixtures of the frame codec's performance contracts (design/sdk.yaml,
// budgets; ADR 0152 D15, issue #921): each sets one call up and returns it,
// and the gen_perf_test.go kit gen writes beside this file counts its
// allocations against its budget. Race off, since the race detector
// allocates on its own.
package wire_test

import (
	"bytes"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// perfEntries and perfEntryBytes shape the append a fixture encodes: D15's
// batch of 64 entries of 256 bytes, the segmented log's bench row too.
const (
	perfEntries    = 64
	perfEntryBytes = 256
)

// perfAppend is a leader's append of perfEntries chained command entries,
// every field of the frame set.
func perfAppend() *coreconsensus.MessageValue {
	data := bytes.Repeat([]byte{'x'}, perfEntryBytes)
	m := &coreconsensus.MessageValue{
		Version: 1, Group: 1, Kind: coreconsensus.MsgApp, From: 1, To: 2, Term: 3,
		LogTerm: 3, Index: 100, Commit: 99, Context: 7, Contract: 1,
	}
	var chain uint32
	for i := range perfEntries {
		e := coreconsensus.EntryValue{
			Position: coreconsensus.PositionValue{Term: 3, Index: 101 + uint64(i)},
			Kind:     coreconsensus.EntryCommand, BodySum: wire.BodySum(data), Data: data,
		}
		e.ChainSum = wire.ChainSum(chain, &e)
		chain = e.ChainSum
		m.Entries = append(m.Entries, e)
	}
	return m
}

// perfAppendMessage is AppendMessage's fixture: the append encoded into a
// buffer that already holds a frame's capacity, as the transport's send
// buffer does once warm.
func perfAppendMessage(tb testing.TB) func() {
	m := perfAppend()
	buf := wire.AppendMessage(nil, m)
	return func() {
		buf = wire.AppendMessage(buf[:0], m)
		if len(buf) == 0 {
			tb.Fatal("AppendMessage wrote nothing")
		}
	}
}

// perfParseMessage is ParseMessage's fixture: the append's frame decoded
// into a MessageValue whose Entries already hold the batch's capacity, as
// the driver's decode target does once warm. Entry data aliases the frame.
func perfParseMessage(tb testing.TB) func() {
	frame := wire.AppendMessage(nil, perfAppend())
	var m coreconsensus.MessageValue
	m.Entries = make([]coreconsensus.EntryValue, 0, perfEntries)
	return func() {
		if err := wire.ParseMessage(frame, &m); err != nil {
			tb.Fatalf("ParseMessage() = %v", err)
		}
	}
}

// perfChainSum is ChainSum's fixture: the chain value of one entry of the
// append, the sum a leader computes per entry it proposes and a follower
// per entry it verifies.
func perfChainSum(tb testing.TB) func() {
	e := perfAppend().Entries[0]
	var sink uint32
	return func() {
		sink = wire.ChainSum(sink, &e)
		if sink == 0 && e.ChainSum == 0 {
			tb.Fatal("ChainSum of a zero chain")
		}
	}
}
