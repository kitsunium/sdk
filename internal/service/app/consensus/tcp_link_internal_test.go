package consensus

import (
	"crypto/tls"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// linkPair is two transports, a sending to b, each admitting the other,
// with b's delivered frames counted on got.
type linkPair struct {
	ca      *testCA
	cid     coreconsensus.ClusterID
	a, b    *tcpNode
	members []coreconsensus.MemberValue
	got     chan struct{}
}

func newLinkPair(t *testing.T) *linkPair {
	t.Helper()
	p := &linkPair{ca: newTestCA(t), cid: coreconsensus.ClusterID{0xc0, 0xff, 0xee}, got: make(chan struct{}, 64)}
	p.a, p.b = &tcpNode{id: 1, slot: "a"}, &tcpNode{id: 2, slot: "b"}
	for _, tn := range []*tcpNode{p.a, p.b} {
		tn.cert, tn.spki = p.ca.issue(t, SAN(p.cid, tn.slot), nil)
		tn.listen(t, p.ca)
		p.members = append(p.members, coreconsensus.MemberValue{ID: tn.id, Slot: tn.slot, SPKI: tn.spki, Address: tn.addr, Voter: true})
	}
	t.Cleanup(func() {
		abandon(p.a.tr.Close())
		abandon(p.b.tr.Close())
	})
	p.bindB(t)
	p.a.tr.SetMembership(1, p.members)
	p.b.tr.SetMembership(1, p.members)
	return p
}

func (p *linkPair) bindB(t *testing.T) {
	t.Helper()
	if err := p.b.tr.Bind(1, func(coreconsensus.AuthenticatedPeerValue, []byte) { p.got <- struct{}{} }, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func (p *linkPair) frame(term uint64) []byte {
	return appendFrame(&coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgPreVote, From: p.a.id, To: p.b.id, Term: term})
}

// delivered reports whether a frame reached b within wait.
func (p *linkPair) delivered(wait time.Duration) bool {
	select {
	case <-p.got:
		return true
	case <-time.After(wait):
		return false
	}
}

// #553: the first frame written after the receiver closed the link
// cleanly (a FIN, not a reset) is delivered. The sender never read its
// links, so a write into a socket in CLOSE_WAIT succeeded, the receiver's
// orphaned socket answered with a reset, and that frame was lost: only the
// one after it failed and went through the redial.
func TestTCPFrameAfterACleanCloseByTheReceiverIsDelivered(t *testing.T) {
	closeIn := func(p *linkPair) {
		p.b.tr.mu.Lock()
		for c := range p.b.tr.in {
			// The raw socket, as serve closes it at a read deadline: a FIN.
			abandon(c.(*tls.Conn).NetConn().Close())
		}
		p.b.tr.mu.Unlock()
	}
	cases := []struct {
		name  string
		close func(t *testing.T, p *linkPair)
	}{
		{"receiver closes the connection", func(_ *testing.T, p *linkPair) { closeIn(p) }},
		{"membership cut then readmitted", func(_ *testing.T, p *linkPair) {
			p.b.tr.SetMembership(1, p.members[1:])
			p.b.tr.SetMembership(1, p.members)
		}},
		{"receiver restarts on the same address", func(t *testing.T, p *linkPair) {
			abandon(p.b.tr.Close())
			p.b.listen(t, p.ca)
			p.bindB(t)
			p.b.tr.SetMembership(1, p.members)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newLinkPair(t)
			p.a.tr.Send(p.b.id, p.frame(2))
			if !p.delivered(2 * time.Second) {
				t.Fatal("the first frame never arrived")
			}
			c.close(t, p)
			time.Sleep(300 * time.Millisecond)
			p.a.tr.Send(p.b.id, p.frame(3))
			if !p.delivered(2 * time.Second) {
				t.Fatal("the frame written right after the receiver's clean close was lost")
			}
		})
	}
}

// #551: once a member's key is no longer admitted — DenyPin left it
// without a pin, or it left the membership — nothing more is written to
// it on a lane dialled before. The sender reused its open lanes with no
// check, so the leader kept replicating to the holder of a fenced key.
func TestTCPNothingIsSentOnAnOldLaneToAKeyNoLongerAdmitted(t *testing.T) {
	cases := []struct {
		name string
		view func(p *linkPair) []coreconsensus.MemberValue
	}{
		{"pin denied", func(p *linkPair) []coreconsensus.MemberValue {
			ms := append([]coreconsensus.MemberValue(nil), p.members...)
			ms[1].SPKI = [32]byte{}
			return ms
		}},
		{"pin replaced", func(p *linkPair) []coreconsensus.MemberValue {
			ms := append([]coreconsensus.MemberValue(nil), p.members...)
			ms[1].SPKI = [32]byte{9}
			return ms
		}},
		{"member removed", func(p *linkPair) []coreconsensus.MemberValue { return p.members[:1] }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newLinkPair(t)
			p.a.tr.Send(p.b.id, p.frame(2))
			if !p.delivered(2 * time.Second) {
				t.Fatal("the first frame never arrived")
			}
			p.a.tr.SetMembership(1, c.view(p))
			for term := uint64(3); term < 8; term++ {
				p.a.tr.Send(p.b.id, p.frame(term))
			}
			if p.delivered(500 * time.Millisecond) {
				t.Fatal("a frame reached a key the sender no longer admits, on a lane dialled before")
			}
		})
	}
}
