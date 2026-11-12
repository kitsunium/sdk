package consensus

import (
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// blackhole is a dialled connection that, once cut, takes every write and
// delivers none: the bytes are accepted, never sent, never acknowledged,
// what a stream into a partition looks like to its writer on a platform
// with no kernel bound on unacknowledged bytes.
type blackhole struct {
	net.Conn
	cut atomic.Bool
}

func (b *blackhole) Write(p []byte) (int, error) {
	if b.cut.Load() {
		return len(p), nil
	}
	return b.Conn.Write(p)
}

// A link that died quietly, its writes taken and never delivered, is
// closed once its peer stayed silent for the read-silence bound while
// written to, and the next batch goes out on a new connection: on the
// BSDs and illumos no kernel bound would ever see it, and every vote and
// append went into it until the kernel's own limit, minutes (#1116).
func TestTCPLinkSilentPastTheBoundIsDialledAgain(t *testing.T) {
	ca := newTestCA(t)
	cid := coreconsensus.ClusterID{0xc0, 0xff, 0xee}
	a, b := &tcpNode{id: 1, slot: "a"}, &tcpNode{id: 2, slot: "b"}
	var members []coreconsensus.MemberValue
	for _, tn := range []*tcpNode{a, b} {
		tn.cert, tn.spki = ca.issue(t, SAN(cid, tn.slot), nil)
		tn.listen(t, ca)
		defer tn.tr.Close()
		members = append(members, coreconsensus.MemberValue{ID: tn.id, Slot: tn.slot, SPKI: tn.spki, Address: tn.addr, Voter: true})
	}
	var mu sync.Mutex
	var dialled []*blackhole
	a.tr.wrapDialled = func(c net.Conn) net.Conn {
		h := &blackhole{Conn: c}
		mu.Lock()
		dialled = append(dialled, h)
		mu.Unlock()
		return h
	}
	// Twice the maximum election timeout of a 100-200 ms timing, as the
	// node sets it at each sweep.
	const bound = 400 * time.Millisecond
	a.tr.boundSilence(bound)

	// b answers every frame, as a peer answers a heartbeat or a vote.
	got := make(chan struct{}, 64)
	answer := appendFrame(&coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgPreVoteResp, From: b.id, To: a.id, Term: 2})
	if err := b.tr.Bind(1, func(coreconsensus.AuthenticatedPeerValue, []byte) {
		got <- struct{}{}
		b.tr.Send(a.id, answer)
	}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.tr.Bind(1, func(coreconsensus.AuthenticatedPeerValue, []byte) {}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	a.tr.SetMembership(1, members)
	b.tr.SetMembership(1, members)
	frame := appendFrame(&coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgPreVote, From: a.id, To: b.id, Term: 2})
	a.tr.Send(b.id, frame)
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the first frame never arrived")
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		a.tr.mu.Lock()
		_, heard := a.tr.heard[b.spki]
		a.tr.mu.Unlock()
		if heard {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the answer to the first frame never arrived")
		}
	}

	// The link dies quietly; the sender keeps writing at a heartbeat's
	// pace, each write accepted.
	mu.Lock()
	for _, h := range dialled {
		h.cut.Store(true)
	}
	mu.Unlock()
	start := time.Now()
	tick := time.NewTicker(bound / 8)
	defer tick.Stop()
	for delivered := false; !delivered; {
		a.tr.Send(b.id, frame)
		select {
		case <-got:
			delivered = true
		case <-tick.C:
		}
		if time.Since(start) > 10*bound {
			t.Fatalf("no frame delivered %v after the link died", time.Since(start))
		}
	}
	elapsed := time.Since(start)
	if elapsed < bound || elapsed > 2*bound {
		t.Fatalf("a frame was delivered %v after the link died, want within [%v, %v]", elapsed, bound, 2*bound)
	}
	mu.Lock()
	n := len(dialled)
	mu.Unlock()
	if n != 2 {
		t.Fatalf("%d connections dialled, want the dead one and its replacement", n)
	}
	if timedOut, silenced := a.tr.linksAborted(); timedOut != 0 || silenced != 1 {
		t.Fatalf("aborted links: user_timeout %d, read_silence %d, want 0 and 1", timedOut, silenced)
	}
}

// A sender link written on while its peer keeps answering is never closed
// for silence, however long it lives, and one idle since its last answer
// starts no wait: only a write does (#1116).
func TestTCPLinkWithAnAnsweringPeerIsKept(t *testing.T) {
	// The transport's ages are read on its clock (#948), the system's here.
	tr := &TCPTransport{cfg: TransportConfig{Clock: clock.System}}
	tr.boundSilence(time.Hour)
	tr.boundSilence(time.Minute) // a shorter bound from another group is not taken
	if got := time.Duration(tr.silence.Load()); got != time.Hour {
		t.Fatalf("bound %v, want the longest, 1h", got)
	}
	tr.silence.Store(int64(10 * time.Millisecond))
	link := &senderLink{pin: [32]byte{9}}
	if tr.silent(link) {
		t.Fatal("a link with no write awaiting an answer was found silent")
	}
	link.unanswered = time.Now().Add(-time.Second)
	tr.heard = map[[32]byte]time.Time{link.pin: time.Now()}
	if tr.silent(link) || !link.unanswered.IsZero() {
		t.Fatal("a peer heard after the write did not end the wait")
	}
	link.unanswered = time.Now().Add(-time.Second)
	tr.heard[link.pin] = link.unanswered.Add(-time.Second)
	if !tr.silent(link) {
		t.Fatal("a peer silent past the bound since the write was not found silent")
	}
}

// The kernel's abort of a connection whose bytes stayed unacknowledged is
// told apart from the link's other ends, which are not counted (#1116).
func TestKernelTimedOutNamesOnlyTheKernelsAbort(t *testing.T) {
	if !kernelTimedOut(&net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", errTimedOut)}) {
		t.Fatal("the kernel's timeout was not recognised")
	}
	for _, err := range []error{nil, io.EOF, net.ErrClosed, os.ErrDeadlineExceeded} {
		if kernelTimedOut(err) {
			t.Fatalf("%v taken for the kernel's timeout", err)
		}
	}
}
