package wire_test

import (
	"bytes"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// splitMessage is an append of three entries with every fixed field set, so
// that a field AppendHeader dropped or moved changes the bytes.
func splitMessage() coreconsensus.MessageValue {
	ents := make([]coreconsensus.EntryValue, 3)
	for i := range ents {
		ents[i] = coreconsensus.EntryValue{
			Position: coreconsensus.PositionValue{Term: 7, Index: uint64(40 + i)},
			Kind:     coreconsensus.EntryCommand,
			Version:  uint32(i),
			Data:     bytes.Repeat([]byte{byte('a' + i)}, 10*i),
		}
		ents[i].BodySum = wire.BodySum(ents[i].Data)
	}
	return coreconsensus.MessageValue{
		Kind: coreconsensus.MsgApp, Group: 3, Flags: 1, From: 1, To: 2, Term: 7, LogTerm: 6, Index: 39,
		Commit: 38, RejectHint: 5, Context: 9, TransferIssuer: 4, TransferTerm: 2, Contract: 11, Code: 13,
		Entries: ents,
	}
}

// TestAHeaderThenItsEntriesAreTheMessagesBytes holds the split of #912 to the
// wire format: the header of each peer, followed by the body every peer
// shares, is byte for byte the frame AppendMessage writes, which a node of
// the previous release decodes. The body names no peer, so the same bytes
// serve a second follower under its own header.
func TestAHeaderThenItsEntriesAreTheMessagesBytes(t *testing.T) {
	m := splitMessage()
	body := wire.AppendEntries(nil, m.Entries)
	n := 0
	for i := range m.Entries {
		n += wire.EntryLen(&m.Entries[i])
	}
	if len(body) != n {
		t.Fatalf("the body is %d bytes, want the sum of EntryLen, %d", len(body), n)
	}
	for _, to := range []coreconsensus.NodeID{2, 5} {
		m.To = to
		head := wire.AppendHeader(nil, &m, len(body))
		if len(head) != wire.MessageHeaderLen {
			t.Fatalf("the header is %d bytes, want MessageHeaderLen, %d", len(head), wire.MessageHeaderLen)
		}
		want := wire.AppendMessage(nil, &m)
		if got := append(head, body...); !bytes.Equal(got, want) {
			t.Fatalf("to %d: header then body differ from AppendMessage's frame:\n got %x\nwant %x", to, got, want)
		}
		var back coreconsensus.MessageValue
		if err := wire.ParseMessage(want, &back); err != nil || back.To != to || len(back.Entries) != len(m.Entries) {
			t.Fatalf("to %d: the frame parses as to %d with %d entries, %v", to, back.To, len(back.Entries), err)
		}
	}
}

// TestAHeaderAppendsAfterWhatDstHolds holds AppendHeader to the append
// contract: it writes after dst's bytes and sets the length of its own
// frame, not of dst's start.
func TestAHeaderAppendsAfterWhatDstHolds(t *testing.T) {
	m := splitMessage()
	body := wire.AppendEntries(nil, m.Entries)
	prefix := []byte{0xde, 0xad}
	head := wire.AppendHeader(append([]byte(nil), prefix...), &m, len(body))
	if !bytes.Equal(head[:2], prefix) {
		t.Fatalf("the prefix was overwritten: %x", head[:2])
	}
	if want := wire.AppendMessage(nil, &m); !bytes.Equal(append(head[2:], body...), want) {
		t.Fatal("a header appended after a prefix differs from AppendMessage's")
	}
}
