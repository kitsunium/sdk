package consensus

import (
	"bytes"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// sharedFrame is one frame a sharedSender was handed.
type sharedFrame struct {
	to           coreconsensus.NodeID
	head, body   []byte
	viaSendFrame bool
}

// sharedSender is a transport that keeps what it is handed, through Send
// and through SendShared, in order. Only Send and SendShared are called.
type sharedSender struct {
	coreconsensus.Transport
	got []sharedFrame
}

func (s *sharedSender) Send(to coreconsensus.NodeID, frame []byte) {
	s.got = append(s.got, sharedFrame{to: to, head: frame, viaSendFrame: true})
}

func (s *sharedSender) SendShared(to coreconsensus.NodeID, header, body []byte) {
	s.got = append(s.got, sharedFrame{to: to, head: header, body: body})
}

// plainSender is a transport without the BatchSender sibling.
type plainSender struct {
	coreconsensus.Transport
	got [][]byte
}

func (s *plainSender) Send(_ coreconsensus.NodeID, frame []byte) { s.got = append(s.got, frame) }

// sharedEntries is a slice of the leader's log: n entries of size bytes.
func sharedEntries(first uint64, n, size int) []coreconsensus.EntryValue {
	ents := make([]coreconsensus.EntryValue, n)
	for i := range ents {
		data := bytes.Repeat([]byte{byte(i)}, size)
		ents[i] = coreconsensus.EntryValue{
			Position: coreconsensus.PositionValue{Term: 3, Index: first + uint64(i)},
			Kind:     coreconsensus.EntryCommand, Data: data, BodySum: wire.BodySum(data),
		}
	}
	return ents
}

// appendTo is the leader's append of ents to a follower.
func appendTo(to coreconsensus.NodeID, ents []coreconsensus.EntryValue) coreconsensus.MessageValue {
	return coreconsensus.MessageValue{
		Kind: coreconsensus.MsgApp, From: 1, To: to, Term: 3, LogTerm: 3,
		Index: ents[0].Position.Index - 1, Commit: 9, Entries: ents,
	}
}

// TestAReadyEncodesTheEntriesOfItsAppendsOnceForEveryFollower holds #912:
// the followers sent the same entries of the leader's log share one body,
// each under its own header, and header then body is the frame Send was
// handed before, which a node of the previous release decodes. A follower
// sent another slice gets its own body, a heartbeat goes through Send
// whole, and a learner the state outgrew is sent no entries (#613).
func TestAReadyEncodesTheEntriesOfItsAppendsOnceForEveryFollower(t *testing.T) {
	log := sharedEntries(10, 8, 40)
	ms := []coreconsensus.MessageValue{
		appendTo(2, log),
		{Kind: coreconsensus.MsgHeartbeat, From: 1, To: 4, Term: 3, Commit: 9},
		appendTo(3, log),
		appendTo(5, log[3:]),
		appendTo(6, log),
	}
	tr := &sharedSender{}
	n := &Node{cfg: Config{Transport: tr}, batch: tr, unfed: map[coreconsensus.NodeID]bool{6: true}}
	n.sendMessages(ms)
	if len(tr.got) != 4 {
		t.Fatalf("%d frames sent, want 4: three appends and a heartbeat, none to the unfed learner", len(tr.got))
	}
	byTo := map[coreconsensus.NodeID]sharedFrame{}
	for _, f := range tr.got {
		byTo[f.to] = f
	}
	if f := byTo[4]; !f.viaSendFrame || !bytes.Equal(f.head, wire.AppendMessage(nil, &ms[1])) {
		t.Fatal("the heartbeat was not sent whole through Send")
	}
	for _, i := range []int{0, 2, 3} {
		f := byTo[ms[i].To]
		if f.viaSendFrame {
			t.Fatalf("the append to %d went through Send", ms[i].To)
		}
		if got := append(bytes.Clone(f.head), f.body...); !bytes.Equal(got, wire.AppendMessage(nil, &ms[i])) {
			t.Fatalf("to %d: header then body differ from the frame AppendMessage writes", ms[i].To)
		}
	}
	if &byTo[2].body[0] != &byTo[3].body[0] {
		t.Error("two followers sent the same entries were handed two encodings of them")
	}
	if &byTo[5].body[0] == &byTo[2].body[0] {
		t.Error("a follower sent another slice was handed the body of the first")
	}
	if len(n.bodies.ranges) != 0 {
		t.Error("the Ready's bodies were kept past it")
	}
	h2, h3 := byTo[2].head, byTo[3].head
	if cap(h2) != len(h2) || cap(h3) != len(h3) {
		t.Error("a peer's header leaves room into the next peer's")
	}
}

// TestATransportWithoutTheSiblingIsSentWholeFrames holds the fallback: a
// transport that is no BatchSender gets AppendMessage's frame per peer, as
// before #912.
func TestATransportWithoutTheSiblingIsSentWholeFrames(t *testing.T) {
	log := sharedEntries(1, 4, 16)
	ms := []coreconsensus.MessageValue{appendTo(2, log), appendTo(3, log)}
	tr := &plainSender{}
	n := &Node{cfg: Config{Transport: tr}, unfed: map[coreconsensus.NodeID]bool{}}
	n.sendMessages(ms)
	if len(tr.got) != 2 {
		t.Fatalf("%d frames, want 2", len(tr.got))
	}
	for i := range ms {
		if !bytes.Equal(tr.got[i], wire.AppendMessage(nil, &ms[i])) {
			t.Fatalf("frame %d is not AppendMessage's", i)
		}
	}
}
