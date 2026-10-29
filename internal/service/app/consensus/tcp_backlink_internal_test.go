package consensus

import (
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// #748: a connection carries frames one way, from the node that dialled
// it. A node whose configuration names a member at an address where it
// does not listen — a voter down while UpdateAddress moved every other
// member — heard that member on the connection it dialled, but answered
// it at the dead address, and nothing reached it. The batch its own link
// cannot carry now goes back on the connection the member dialled, and the
// member delivers it only as the member its own configuration pins under
// that key and slot.

// backPair is two transports, a and b, each delivering what it reads on
// a channel with the peer the transport authenticated it as.
type backPair struct {
	ca     *testCA
	cid    coreconsensus.ClusterID
	a, b   *tcpNode
	atA    chan coreconsensus.AuthenticatedPeerValue
	atB    chan coreconsensus.AuthenticatedPeerValue
	failed atomic.Int64
}

func newBackPair(t *testing.T) *backPair {
	t.Helper()
	p := &backPair{
		ca: newTestCA(t), cid: coreconsensus.ClusterID{0xba, 0xc4},
		atA: make(chan coreconsensus.AuthenticatedPeerValue, 64), atB: make(chan coreconsensus.AuthenticatedPeerValue, 64),
	}
	p.a, p.b = &tcpNode{id: 1, slot: "a"}, &tcpNode{id: 2, slot: "b"}
	for _, tn := range []*tcpNode{p.a, p.b} {
		tn.cert, tn.spki = p.ca.issue(t, SAN(p.cid, tn.slot), nil)
		tn.listen(t, p.ca)
	}
	t.Cleanup(func() {
		abandon(p.a.tr.Close())
		abandon(p.b.tr.Close())
	})
	for tn, at := range map[*tcpNode]chan coreconsensus.AuthenticatedPeerValue{p.a: p.atA, p.b: p.atB} {
		if err := tn.tr.Bind(1, func(from coreconsensus.AuthenticatedPeerValue, _ []byte) { at <- from }, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	p.b.tr.Unreachable(func(coreconsensus.NodeID) { p.failed.Add(1) })
	return p
}

// member is tn's record, at addr.
func (p *backPair) member(tn *tcpNode, addr string) coreconsensus.MemberValue {
	return coreconsensus.MemberValue{ID: tn.id, Slot: tn.slot, SPKI: tn.spki, Address: addr, Voter: true}
}

func (p *backPair) frame(from, to *tcpNode) []byte {
	return appendFrame(&coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgHeartbeat, From: from.id, To: to.id, Term: 2})
}

// firstFrom is the peer of the first frame at within wait, ok false when none
// arrived.
func firstFrom(at chan coreconsensus.AuthenticatedPeerValue, wait time.Duration) (coreconsensus.AuthenticatedPeerValue, bool) {
	select {
	case from := <-at:
		return from, true
	case <-time.After(wait):
		return coreconsensus.AuthenticatedPeerValue{}, false
	}
}

// staleB has b name a at an address where nothing listens, a's view of b
// being bView; then, when aDials, a dials b and b reads that frame.
func (p *backPair) staleB(t *testing.T, bView coreconsensus.MemberValue, aDials bool) {
	t.Helper()
	p.a.tr.SetMembership(1, []coreconsensus.MemberValue{p.member(p.a, p.a.addr), bView})
	p.b.tr.SetMembership(1, []coreconsensus.MemberValue{p.member(p.a, movedFreeAddr(t)), p.member(p.b, p.b.addr)})
	if !aDials {
		return
	}
	p.a.tr.Send(p.b.id, p.frame(p.a, p.b))
	if from, ok := firstFrom(p.atB, 2*time.Second); !ok || from.ID != p.a.id {
		t.Fatalf("setup: b never read a's frame (%+v, %v)", from, ok)
	}
}

// TestTCPBackLinkCarriesTheAnswerTheDialCannot: b cannot dial a, a dialled
// b; b's frame reaches a on a's own link, as b's, and b still reports a
// unreachable. With no connection from a, nothing reaches a.
func TestTCPBackLinkCarriesTheAnswerTheDialCannot(t *testing.T) {
	for _, aDials := range []bool{true, false} {
		p := newBackPair(t)
		p.staleB(t, p.member(p.b, p.b.addr), aDials)
		p.b.tr.Send(p.a.id, p.frame(p.b, p.a))
		wait := 2 * time.Second
		if !aDials {
			wait = 500 * time.Millisecond
		}
		from, ok := firstFrom(p.atA, wait)
		switch {
		case !aDials && ok:
			t.Fatalf("a connection a never dialled carried b's frame, as %+v", from)
		case !aDials:
		case !ok:
			t.Fatal("b's answer never reached a: b dials a at a dead address, and a's own link to b carried nothing back")
		case from.ID != p.b.id || from.SPKI != p.b.spki || from.Slot != p.b.slot:
			t.Fatalf("b's frame reached a as %+v, want b (%d, slot %q) under its key", from, p.b.id, p.b.slot)
		}
		if p.failed.Load() == 0 {
			t.Fatalf("aDials=%v: b's failed dial to a was not reported", aDials)
		}
	}
}

// TestTCPBackLinkSpeaksOnlyForThePinnedMember: a frame back on a's link is
// delivered only as the member a lists under that key and slot. A listing
// b under another slot reads it as nobody's, and once a no longer admits
// b's key, nothing b sends back is delivered.
func TestTCPBackLinkSpeaksOnlyForThePinnedMember(t *testing.T) {
	t.Run("another slot", func(t *testing.T) {
		p := newBackPair(t)
		other := p.member(p.b, p.b.addr)
		other.Slot = "x"
		p.staleB(t, other, true)
		p.b.tr.Send(p.a.id, p.frame(p.b, p.a))
		if from, ok := firstFrom(p.atA, 500*time.Millisecond); ok {
			t.Fatalf("a delivered b's frame as %+v while it lists b under slot %q", from, other.Slot)
		}
	})
	t.Run("key no longer admitted", func(t *testing.T) {
		p := newBackPair(t)
		p.staleB(t, p.member(p.b, p.b.addr), true)
		p.b.tr.Send(p.a.id, p.frame(p.b, p.a))
		if _, ok := firstFrom(p.atA, 2*time.Second); !ok {
			t.Fatal("setup: b's first frame never reached a")
		}
		denied := p.member(p.b, p.b.addr)
		denied.SPKI = [32]byte{}
		p.a.tr.SetMembership(1, []coreconsensus.MemberValue{p.member(p.a, p.a.addr), denied})
		for range 3 {
			p.b.tr.Send(p.a.id, p.frame(p.b, p.a))
			time.Sleep(150 * time.Millisecond)
		}
		if from, ok := firstFrom(p.atA, 300*time.Millisecond); ok {
			t.Fatalf("a delivered %+v after it denied b's key", from)
		}
	})
}

// TestTCPBackLinkIsTheNewestConnection: a connection the member dialled
// again replaces the older one as its back link, and the older one's
// reader, once it stops, forgets it without taking the newer with it.
func TestTCPBackLinkIsTheNewestConnection(t *testing.T) {
	tr := &TCPTransport{back: map[outKey]*backLink{}}
	k := outKey{1, laneControl}
	older := tr.holdBack(k, nil, [32]byte{1})
	newer := tr.holdBack(k, nil, [32]byte{1})
	tr.dropBack(k, older)
	if tr.back[k] != newer {
		t.Fatalf("the older link's end left %p, want the newer %p", tr.back[k], newer)
	}
	tr.dropBack(k, newer)
	if bl, ok := tr.back[k]; ok {
		t.Fatalf("the newer link's end left %p", bl)
	}
}
