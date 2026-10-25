package consensus

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// fetchFrameCID is the cluster of these tests.
var fetchFrameCID = coreconsensus.ClusterID{0xc0, 0xff, 0xee}

// fetchFrameTransport opens tn's transport on addr with MaxFrameBytes frame
// (0 keeps the default, 16 MiB), retrying while the port is not free yet.
func fetchFrameTransport(t *testing.T, tn *tcpNode, ca *testCA, addr string, frame int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		tr, err := NewTCPTransport(&TransportConfig{Certificate: tn.cert, Roots: ca.pool, ListenAddress: addr, MaxFrameBytes: frame})
		if err == nil {
			tn.tr, tn.addr = tr, tr.Addr()
			return
		}
		if !addrInUse(err) || time.Now().After(deadline) {
			t.Fatalf("NewTCPTransport(MaxFrameBytes=%d) on %s: %v (%s)", frame, addr, err, fieldOf(err, "why"))
		}
	}
}

// fetchFrameCluster is newTCPCluster with every transport's MaxFrameBytes
// set to frame and every node's configuration changed by tweak. NewNode is
// the judge of the pair: it refuses a MaxBatchBytes whose batch plus 1 KiB
// of framing the transport cannot carry (review M2).
func fetchFrameCluster(t *testing.T, ca *testCA, frame int, tweak func(*Config)) []*tcpNode {
	t.Helper()
	var ns []*tcpNode
	var members []coreconsensus.MemberValue
	for i := range 3 {
		tn := &tcpNode{slot: fmt.Sprintf("site-%d", i), log: NewMemoryLog(), snaps: NewMemorySnapshots(), tweak: tweak}
		tn.cert, tn.spki = ca.issue(t, SAN(fetchFrameCID, tn.slot), nil)
		fetchFrameTransport(t, tn, ca, "127.0.0.1:0", frame)
		id, inc, err := InitLog(tn.log, fetchFrameCID, tn.slot)
		if err != nil {
			t.Fatal(err)
		}
		tn.id, tn.inc = id, inc
		ns = append(ns, tn)
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: tn.slot, SPKI: tn.spki, Address: tn.addr, Voter: true})
	}
	for _, tn := range ns {
		tn.sm = newKV()
		if err := Bootstrap(context.Background(), new(tcpConfig(fetchFrameCID, tn)), members); err != nil {
			t.Fatal(err)
		}
		tn.start(t, fetchFrameCID)
	}
	t.Cleanup(func() {
		for _, tn := range ns {
			tn.stop(t)
		}
	})
	return ns
}

// storeReadFrame is the length of the append frame a store's read from lo
// under MaxBatchBytes makes when sent whole, as the leader sent a fetched
// batch before #683: Log.Entries as Node.fetch reads it, encoded as
// raft.sendEntries sends it.
func storeReadFrame(t *testing.T, log *MemoryLog, lo uint64, batch int) (frame, entries, payload int) {
	t.Helper()
	_, last := log.Bounds()
	ents, err := log.Entries(lo, last+1, batch, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range ents {
		payload += len(ents[i].Data)
	}
	m := coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgApp, Entries: ents}
	return len(wire.AppendMessage(nil, &m)), len(ents), payload
}

// Config accepts any MaxBatchBytes whose batch plus 1 KiB of framing fits
// the transport's MaxFrameBytes (review M2, refuseBounds). The in-memory
// append path held that: raft's memLog.slice counts 64 bytes per entry on
// top of its payload. The fetch path that feeds a follower behind the
// evicted payloads (#664) did not: Node.fetch reads Log.Entries(lo, hi,
// MaxBatchBytes), and both shipped stores count the payload alone, so a
// batch of N entries was 112 + 33*N bytes over its payload. Past 1 KiB the
// follower's reader refused the frame (readFrame) and dropped the
// connection without a word, the leader probed and fetched the same batch
// again, and the follower never caught up by appends; with no cut before
// SnapshotEvery there was no install either, and one more failure left the
// leader with no quorum for good (#683). The leader now cuts a fetched
// batch to MaxBatchBytes of its encoding, and the follower refuses nothing.
func TestAFollowerBehindTheEvictedPayloadsCatchesUpUnderTheTightestFrame(t *testing.T) {
	const batch = 64 << 10
	entry := strings.Repeat("v", 500)
	cases := []struct {
		name   string
		frame  int
		recent int64
	}{
		{"default MaxFrameBytes, payloads evicted", 0, 16 << 10},
		{"MaxFrameBytes = MaxBatchBytes + 1 KiB, payloads in memory", batch + batchFrameOverhead, 0},
		{"MaxFrameBytes = MaxBatchBytes + 1 KiB, payloads evicted", batch + batchFrameOverhead, 16 << 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ca := newTestCA(t)
			ns := fetchFrameCluster(t, ca, c.frame, func(cfg *Config) {
				cfg.MaxBatchBytes = batch
				cfg.MaxEntryBytes = 4 << 10
				cfg.RecentEntriesBytes = c.recent
			})
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
			propose(t, l.node, "a=1")
			lo := f1.node.Status().Commit + 1
			f1.stop(t)
			// About 300 KB of 500-byte writes while f1 is down: committed by
			// l and f2, far below SnapshotEvery, so no cut and no compaction.
			for i := range 600 {
				propose(t, l.node, fmt.Sprintf("k%d=%s", i, entry))
			}
			want := l.node.Status().Commit
			bound := c.frame
			if bound == 0 {
				bound = defaultMaxFrameBytes
			}
			frame, n, payload := storeReadFrame(t, l.log, lo, batch)
			t.Logf("a store's read from %d under MaxBatchBytes %d: %d entries, %d payload bytes, an append frame of %d bytes sent whole; the follower reads under MaxFrameBytes %d",
				lo, batch, n, payload, frame, bound)
			if c.frame != 0 && frame <= bound {
				t.Fatalf("setup: a store's read sent whole (%d bytes) is within the bound (%d), so nothing here is #683's", frame, bound)
			}
			fetchFrameTransport(t, f1, ca, f1.addr, c.frame)
			f1.start(t, fetchFrameCID)
			deadline := time.Now().Add(6 * time.Second)
			for time.Now().Before(deadline) && f1.node.Status().Commit < want {
				time.Sleep(20 * time.Millisecond)
			}
			st := f1.node.Status()
			refused := f1.tr.refused.Load()
			t.Logf("restarted follower: commit %d applied %d leader %d, the leader's commit %d; it refused %d frames above its bound", st.Commit, st.Applied, st.Leader, want, refused)
			if st.Commit < want {
				t.Fatalf("the restarted follower stayed at commit %d under the same leader, whose commit is %d", st.Commit, want)
			}
			if refused != 0 {
				t.Fatalf("the restarted follower caught up, but refused %d frames above its MaxFrameBytes %d on the way", refused, bound)
			}
			// One more failure: the leader and f1 alone still commit.
			f2.stop(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, _, err := l.node.Propose(ctx, []byte("z=9"))
			cancel()
			if err != nil {
				t.Fatalf("with f2 down, Propose(\"z=9\"): %v; leader status role %v term %d commit %d", err, l.node.Status().Role, l.node.Status().Term, l.node.Status().Commit)
			}
		})
	}
}

// readWarns records what a transport logs through bindWarn: each line, and
// the refusal count it carries.
type readWarns struct {
	mu      sync.Mutex
	lines   []string
	refused []uint64
	length  []uint64
	bound   []uint64
}

func (w *readWarns) warn(msg string, attrs ...corelogger.AttrValue) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines = append(w.lines, msg)
	for _, a := range attrs {
		switch a.Key {
		case "refused":
			w.refused = append(w.refused, a.Value.Uint64())
		case "length":
			w.length = append(w.length, a.Value.Uint64())
		case "bound":
			w.bound = append(w.bound, a.Value.Uint64())
		}
	}
}

// The follower refused the leader's oversized frame without a word, and
// the leader sent it again at every probe (#683). A frame a member
// announces above the bound its lane is read under is still refused, which
// ends the connection, but counted and logged, at the first refusal and at
// each power of two after it, as Send's drops are (#669). A frame within
// the bound is delivered, on a new connection, and logs nothing.
func TestAFrameAboveTheReadBoundIsCountedAndLogged(t *testing.T) {
	ca := newTestCA(t)
	cid := coreconsensus.ClusterID{0x68, 0x3}
	const bound = 1 << 20
	a, b := &tcpNode{id: 1, slot: "a"}, &tcpNode{id: 2, slot: "b"}
	var members []coreconsensus.MemberValue
	for _, tn := range []*tcpNode{a, b} {
		tn.cert, tn.spki = ca.issue(t, SAN(cid, tn.slot), nil)
		frame := 0
		if tn == b {
			frame = bound
		}
		fetchFrameTransport(t, tn, ca, "127.0.0.1:0", frame)
		t.Cleanup(func() { _ = tn.tr.Close() })
		members = append(members, coreconsensus.MemberValue{ID: tn.id, Slot: tn.slot, SPKI: tn.spki, Address: tn.addr, Voter: true})
	}
	var w readWarns
	b.tr.bindWarn(w.warn)
	got := make(chan int, 64)
	if err := b.tr.Bind(1, func(_ coreconsensus.AuthenticatedPeerValue, f []byte) { got <- len(f) }, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	a.tr.SetMembership(1, members)
	b.tr.SetMembership(1, members)
	app := func(n int) []byte {
		return appendFrame(&coreconsensus.MessageValue{
			Version: 1, Group: 1, Kind: coreconsensus.MsgApp, From: a.id, To: b.id, Term: 2,
			Entries: []coreconsensus.EntryValue{{Position: coreconsensus.PositionValue{Term: 2, Index: 1}, Kind: coreconsensus.EntryCommand, Data: make([]byte, n)}},
		})
	}
	small, big := app(64), app(bound)
	// delivered sends frame until b delivers one of its length.
	delivered := func(what string, frame []byte) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			a.tr.Send(b.id, frame)
			select {
			case n := <-got:
				if n == len(frame) {
					return
				}
			case <-time.After(200 * time.Millisecond):
			}
		}
		t.Fatalf("%s: the frame of %d bytes never arrived", what, len(frame))
	}
	delivered("a frame within the bound", small)
	// Each refusal ends the connection: the next frame goes on a new one.
	for deadline := time.Now().Add(10 * time.Second); b.tr.refused.Load() < 4; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d frames above the bound refused in 10 s, want 4", b.tr.refused.Load())
		}
		a.tr.Send(b.id, big)
	}
	delivered("a frame within the bound after the refusals", small)
	refused := uint64(b.tr.refused.Load())
	w.mu.Lock()
	defer w.mu.Unlock()
	t.Logf("%d refused, logged %v at %v", refused, w.lines, w.refused)
	var want []uint64
	for p := uint64(1); p <= refused; p *= 2 {
		want = append(want, p)
	}
	if fmt.Sprint(w.refused) != fmt.Sprint(want) || len(w.lines) != len(want) {
		t.Fatalf("%d refusals logged %d lines counting %v, want one at each power of two, %v", refused, len(w.lines), w.refused, want)
	}
	for i := range w.lines {
		if !strings.Contains(w.lines[i], "above the bound") || w.length[i] != uint64(len(big)) || w.bound[i] != bound {
			t.Fatalf("line %d: %q length %d bound %d, want the refused frame's %d under %d", i, w.lines[i], w.length[i], w.bound[i], len(big), bound)
		}
	}
}
