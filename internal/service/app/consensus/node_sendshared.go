package consensus

import (
	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// appendBodies is what one Ready's appends share (#912): the leader builds
// one message per follower over the same slice of its log, and every
// follower in replicate state at the same next index is sent the same
// entries. Their body is encoded once and handed to each peer under its own
// header, through the transport's BatchSender; only the header names the
// peer. It is the loop's alone, reset after every Ready so that it pins no
// body past the turn.
type appendBodies struct {
	// ranges are the bodies encoded in this Ready, one per entries slice.
	ranges []encodedBody
}

// encodedBody is the body of one entries slice: the slice is known by its
// first element's address and its length, so two messages share a body
// only when they carry the very same entries of the leader's log, which
// nothing mutates within a Ready.
type encodedBody struct {
	first *coreconsensus.EntryValue
	n     int
	body  []byte
}

// body returns the encoding of ents, the one already made in this Ready
// when another message carried the same entries, a new one otherwise,
// sized once to its length.
func (b *appendBodies) body(ents []coreconsensus.EntryValue) []byte {
	first := &ents[0]
	for i := range b.ranges {
		//: the same slice of the log already encoded in this Ready.
		if b.ranges[i].first == first && b.ranges[i].n == len(ents) {
			return b.ranges[i].body
		}
	}
	size := 0
	for i := range ents {
		size += wire.EntryLen(&ents[i])
	}
	body := wire.AppendEntries(make([]byte, 0, size), ents)
	b.ranges = append(b.ranges, encodedBody{first: first, n: len(ents), body: body})
	return body
}

// reset forgets this Ready's bodies, keeping the slice's room: the bodies
// are the transport's now, and only its queues keep them alive.
func (b *appendBodies) reset() {
	clear(b.ranges)
	b.ranges = b.ranges[:0]
}

// sendShared sends ms through bs: every append that carries entries as its
// header and the body it shares with the other appends of the same
// entries, every other message through Send as before. The headers of the
// Ready share one allocation, each peer owning its own part of it, so a
// Ready of N followers in replicate state allocates two buffers whatever
// N, where it encoded and copied the entries once per follower. The bytes
// on the wire are AppendMessage's (wire.AppendHeader): a node of the
// previous release reads them unchanged.
func (n *Node) sendShared(bs coreconsensus.BatchSender, ms []coreconsensus.MessageValue) {
	heads := 0
	for i := range ms {
		//: an append with entries to a fed peer takes a header of the slab.
		if len(ms[i].Entries) > 0 && !n.unfed[ms[i].To] {
			heads++
		}
	}
	var slab []byte
	//: a Ready of heartbeats and answers alone allocates no slab.
	if heads > 0 {
		slab = make([]byte, 0, heads*wire.MessageHeaderLen)
	}
	for i := range ms {
		m := &ms[i]
		//: a message without entries is encoded whole, as before.
		if len(m.Entries) == 0 {
			n.cfg.Transport.Send(m.To, wire.AppendMessage(nil, m))
			continue
		}
		//: a learner the newest snapshot is too large for is fed nothing (#613).
		if n.unfed[m.To] {
			continue
		}
		body := n.bodies.body(m.Entries)
		start := len(slab)
		slab = wire.AppendHeader(slab, m, len(body))
		bs.SendShared(m.To, slab[start:len(slab):len(slab)], body)
	}
	n.bodies.reset()
}
