package consensus

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
)

// #669: TransportConfig accepted a PeerQueueBytes below the largest append
// frame, and Send dropped, without a word, any frame larger than the queue,
// even into an empty one: an entry legal under MaxEntryBytes, or a lagging
// follower's catch-up batch, was never delivered, and the commit stopped
// for good under the same leader. The queue is now refused below
// MaxFrameBytes, the bound every frame a peer can read is under (Config
// already holds a batch below it), and a frame larger than the queue is
// counted and logged.

// queueCluster is newTCPCluster with every transport's MaxFrameBytes and
// PeerQueueBytes set to frame and queue; the rest is the default.
func queueCluster(t *testing.T, ca *testCA, frame int, queue int64) []*tcpNode {
	t.Helper()
	cid := coreconsensus.ClusterID{0x66, 0x9}
	var ns []*tcpNode
	var members []coreconsensus.MemberValue
	for i := range 3 {
		tn := &tcpNode{slot: fmt.Sprintf("site-%d", i), log: NewMemoryLog(), snaps: NewMemorySnapshots()}
		tn.cert, tn.spki = ca.issue(t, SAN(cid, tn.slot), nil)
		queueListen(t, tn, ca, frame, queue)
		id, inc, err := InitLog(tn.log, cid, tn.slot)
		if err != nil {
			t.Fatal(err)
		}
		tn.id, tn.inc = id, inc
		ns = append(ns, tn)
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: tn.slot, SPKI: tn.spki, Address: tn.addr, Voter: true})
	}
	for _, tn := range ns {
		tn.sm = newKV()
		if err := Bootstrap(context.Background(), new(tcpConfig(cid, tn)), members); err != nil {
			t.Fatal(err)
		}
		tn.start(t, cid)
	}
	t.Cleanup(func() {
		for _, tn := range ns {
			tn.stop(t)
		}
	})
	return ns
}

// queueListen opens tn's transport, on its address when it has one, with
// the frame and queue bounds, retrying while the port is not free again.
func queueListen(t *testing.T, tn *tcpNode, ca *testCA, frame int, queue int64) {
	t.Helper()
	addr := tn.addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		tr, err := NewTCPTransport(&TransportConfig{Certificate: tn.cert, Roots: ca.pool, ListenAddress: addr, MaxFrameBytes: frame, PeerQueueBytes: queue})
		if err == nil {
			tn.tr, tn.addr = tr, tr.Addr()
			return
		}
		if tn.addr == "" || !addrInUse(err) || time.Now().After(deadline) {
			t.Fatalf("listen on %s: %v (%s)", addr, err, fieldOf(err, "why"))
		}
	}
}

// proposeAnyLeader proposes data to whichever of ns leads, again on the
// next leader when the one asked lost its leadership first, and fails the
// test on any other refusal or after 20 s. The tests write each value once
// and read none back, so a retried write is harmless.
func proposeAnyLeader(t *testing.T, ns []*tcpNode, data []byte) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		l := tcpLeader(t, ns)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _, err := l.node.Propose(ctx, data)
		cancel()
		if err == nil {
			return
		}
		if !errors.Is(err, coreconsensus.NotLeader) || time.Now().After(deadline) {
			t.Fatalf("Propose(%d bytes): %v", len(data), err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// openQueue builds a transport of the configuration cfg, which it completes
// with a certificate, roots and an address, and closes it again.
func openQueue(t *testing.T, ca *testCA, cfg TransportConfig) (TransportConfig, error) {
	t.Helper()
	cert, _ := ca.issue(t, SAN(coreconsensus.ClusterID{0x66, 0x9}, "site-q"), nil)
	cfg.Certificate, cfg.Roots, cfg.ListenAddress = cert, ca.pool, "127.0.0.1:0"
	tr, err := NewTCPTransport(&cfg)
	if err != nil {
		return cfg, err
	}
	got := tr.cfg
	_ = tr.Close()
	return got, nil
}

// A queue below MaxFrameBytes is refused, as a control frame bound not
// below it is; one at the bound is accepted, and an unset queue is never
// below the bound it is checked against.
func TestAPeerQueueBelowTheFrameBoundIsRefused(t *testing.T) {
	ca := newTestCA(t)
	for _, c := range []struct {
		name    string
		cfg     TransportConfig
		refused bool
	}{
		{"the issue's 1 MiB queue", TransportConfig{PeerQueueBytes: 1 << 20}, true},
		{"the issue's 256 KiB queue", TransportConfig{PeerQueueBytes: 256 << 10}, true},
		{"one byte below the frame bound", TransportConfig{MaxFrameBytes: 4 << 20, PeerQueueBytes: 4<<20 - 1}, true},
		{"the frame bound", TransportConfig{MaxFrameBytes: 4 << 20, PeerQueueBytes: 4 << 20}, false},
		{"the defaults", TransportConfig{}, false},
		{"an unset queue under a frame bound above its default", TransportConfig{MaxFrameBytes: 96 << 20}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := openQueue(t, ca, c.cfg)
			if c.refused {
				if !errors.Is(err, coreconsensus.TransportMisconfigured) || fieldOf(err, "field") != "PeerQueueBytes" {
					t.Fatalf("PeerQueueBytes %d under MaxFrameBytes %d: %v, want TRANSPORT_MISCONFIGURED on PeerQueueBytes", c.cfg.PeerQueueBytes, got.MaxFrameBytes, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if got.PeerQueueBytes < int64(got.MaxFrameBytes) {
				t.Fatalf("accepted a queue of %d under a frame bound of %d", got.PeerQueueBytes, got.MaxFrameBytes)
			}
		})
	}
}

// The issue's scenario A, turned around: its configuration is refused, and
// with the smallest queue accepted an entry legal under MaxEntryBytes, whose
// append frame is larger than MaxEntryBytes, commits, a write behind it too.
func TestAnEntryUnderMaxEntryBytesCommitsThroughTheSmallestPeerQueue(t *testing.T) {
	ca := newTestCA(t)
	if _, err := openQueue(t, ca, TransportConfig{PeerQueueBytes: 1 << 20}); !errors.Is(err, coreconsensus.TransportMisconfigured) {
		t.Fatalf("the issue's PeerQueueBytes of 1 MiB under the default MaxFrameBytes: %v", err)
	}
	// 1 048 480 bytes: under the default MaxEntryBytes (1 048 576), an
	// append frame of 1 048 625 bytes.
	big := "big=" + strings.Repeat("x", 1<<20-100)
	const bound = 2 << 20
	ns := queueCluster(t, ca, bound, bound)
	proposeAnyLeader(t, ns, []byte("a=1"))
	proposeAnyLeader(t, ns, []byte(big))
	proposeAnyLeader(t, ns, []byte("c=3"))
}

// The issue's scenario B, turned around: its configuration is refused, and
// with the smallest queue accepted a follower behind by more than its
// catch-up batch catches up by appends, and commits with the leader alone
// once the other follower goes down.
func TestAFollowerBehindCatchesUpThroughTheSmallestPeerQueue(t *testing.T) {
	ca := newTestCA(t)
	if _, err := openQueue(t, ca, TransportConfig{PeerQueueBytes: 256 << 10}); !errors.Is(err, coreconsensus.TransportMisconfigured) {
		t.Fatalf("the issue's PeerQueueBytes of 256 KiB under the default MaxFrameBytes: %v", err)
	}
	const bound = 2 << 20
	ns := queueCluster(t, ca, bound, bound)
	l := tcpLeader(t, ns)
	var f1, f2 *tcpNode
	for _, tn := range ns {
		switch {
		case tn == l:
		case f1 == nil:
			f1 = tn
		default:
			f2 = tn
		}
	}
	proposeAnyLeader(t, ns, []byte("a=1"))
	f1.stop(t)
	entry := strings.Repeat("v", 1000)
	for i := range 600 {
		proposeAnyLeader(t, ns, fmt.Appendf(nil, "k%d=%s", i, entry))
	}
	want := tcpLeader(t, ns).node.Status().Commit
	queueListen(t, f1, ca, bound, bound)
	f1.start(t, coreconsensus.ClusterID{0x66, 0x9})
	waitFor(t, "the restarted follower catches up", func() bool { return f1.node.Status().Commit >= want })
	f2.stop(t)
	proposeAnyLeader(t, ns, []byte("z=9"))
}

// queueWarns records what a transport logs through bindWarn, with the
// dropped count each line carries.
type queueWarns struct {
	mu      sync.Mutex
	lines   []string
	dropped []string
}

func (w *queueWarns) warn(msg string, attrs ...corelogger.AttrValue) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines = append(w.lines, msg)
	for _, a := range attrs {
		if a.Key == "dropped" {
			w.dropped = append(w.dropped, strconv.FormatUint(a.Value.Uint64(), 10))
		}
	}
}

// A frame larger than the whole queue can never be sent: it is still
// dropped, as Send never blocks (D4), but counted and logged, at the first
// drop and then at each power of two, where it said nothing. A frame that
// fits is queued and logs nothing.
func TestAFrameAboveThePeerQueueIsCountedAndLogged(t *testing.T) {
	ca := newTestCA(t)
	cid := coreconsensus.ClusterID{0x66, 0x9}
	cert, spki := ca.issue(t, SAN(cid, "site-a"), nil)
	const bound = 1 << 20
	tr, err := NewTCPTransport(&TransportConfig{Certificate: cert, Roots: ca.pool, ListenAddress: "127.0.0.1:0", MaxFrameBytes: bound, PeerQueueBytes: bound})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	var w queueWarns
	tr.bindWarn(w.warn)
	tr.SetMembership(1, []coreconsensus.MemberValue{
		{ID: 1, Slot: "site-a", SPKI: spki, Address: tr.Addr(), Voter: true},
		{ID: 2, Slot: "site-b", SPKI: [32]byte{2}, Address: "127.0.0.1:1", Voter: true},
	})
	tr.Send(2, make([]byte, 64))
	for range 4 {
		tr.Send(2, make([]byte, bound+1))
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	t.Logf("logged %v, dropped %v", w.lines, w.dropped)
	if len(w.lines) != 3 || !strings.Contains(w.lines[0], "larger than the peer queue") {
		t.Fatalf("four frames above the queue logged %d lines %v, want 3 (the first, second and fourth drop)", len(w.lines), w.lines)
	}
	if strings.Join(w.dropped, ",") != "1,2,4" {
		t.Fatalf("the lines counted %v drops, want 1, 2 and 4", w.dropped)
	}
}
