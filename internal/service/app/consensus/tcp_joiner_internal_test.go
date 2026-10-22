package consensus

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// joinTCP starts a new node on slot, sends its JOIN to every member of ns
// and returns it once admitted as a learner.
func joinTCP(t *testing.T, ca *testCA, cid coreconsensus.ClusterID, ns []*tcpNode, slot string) *tcpNode {
	t.Helper()
	return joinTCPWith(t, ca, cid, ns, slot, nil)
}

// joinTCPWith is joinTCP with tweak applied to the joiner's configuration.
func joinTCPWith(t *testing.T, ca *testCA, cid coreconsensus.ClusterID, ns []*tcpNode, slot string, tweak func(*Config)) *tcpNode {
	t.Helper()
	j := &tcpNode{slot: slot, log: NewMemoryLog(), snaps: NewMemorySnapshots(), tweak: tweak}
	j.cert, j.spki = ca.issue(t, SAN(cid, j.slot), nil)
	j.listen(t, ca)
	id, _, err := InitLog(j.log, cid, j.slot)
	if err != nil {
		t.Fatal(err)
	}
	j.id = id
	j.start(t, cid)
	t.Cleanup(func() { j.stop(t) })
	st := must(j.log.Stamp())(t)
	addrs := make([]string, 0, len(ns))
	for _, tn := range ns {
		addrs = append(addrs, tn.addr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		_, err := j.tr.Join(ctx, coreconsensus.JoinConfig{Addresses: addrs, Stamp: st, Address: j.addr, Pins: pinsOf(ns)})
		if err == nil {
			return j
		}
		if ctx.Err() != nil {
			t.Fatalf("join %s: %v", slot, err)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// waitKV waits until tn's machine holds key=want.
func waitKV(t *testing.T, tn *tcpNode, key, want string, within time.Duration) bool {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if v, _ := tn.sm.get(key); v == want {
			return true
		}
	}
	return false
}

// #552: a joiner whose leader was admitted after the configuration the
// joiner replays (an older snapshot, or the first batch of a log fed from
// index 1) keeps admitting that leader and catches up. Its transport took
// the leader only from the JOIN answer, in the engine's own group: the
// first configuration the joiner replayed replaced it, cut the leader's
// connections and refused its handshakes for as long as it led.
func TestAJoinerKeepsALeaderNewerThanTheConfigurationItReplays(t *testing.T) {
	cases := []struct {
		name    string
		founder func(*Config)
		leader  func(*Config)
	}{
		{"older snapshot", func(c *Config) { c.SnapshotEvery, c.SnapshotTrailing = 8, 1 }, nil},
		{"first batch of the log", nil, func(c *Config) { c.MaxBatchBytes, c.MaxEntryBytes = 4096, 4096 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ca, cid, ns := newTCPClusterWith(t, c.founder)
			pad := strings.Repeat("x", 512)
			for i := range 40 {
				proposeOnTCPLeader(t, ns, fmt.Sprintf("k%d=%s", i, pad))
			}
			// D joins, catches up, is promoted and takes the leadership.
			d := joinTCPWith(t, ca, cid, ns, "site-d", c.leader)
			proposeOnTCPLeader(t, ns, "a=1")
			if !waitKV(t, d, "a", "1", 10*time.Second) {
				t.Fatalf("D never caught up: %+v", d.node.Status())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := tcpLeader(t, ns).node.Promote(ctx, "op:test", d.id); err != nil {
				t.Fatalf("promote D: %v", err)
			}
			if err := tcpLeader(t, ns).node.TransferLeadership(ctx, "op:test", d.id); err != nil {
				t.Fatalf("transfer to D: %v", err)
			}
			all := append(append([]*tcpNode(nil), ns...), d)
			if l := tcpLeader(t, all); l != d {
				t.Fatalf("D does not lead: %d does", l.id)
			}
			// E joins while D leads: D feeds it what predates D.
			e := joinTCP(t, ca, cid, all, "site-e")
			proposeOnTCPLeader(t, all, "b=2")
			if !waitKV(t, e, "b", "2", 4*testTimingFast().ElectionTimeoutMax+5*time.Second) {
				t.Fatalf("E never caught up under D: %+v", e.node.Status())
			}
		})
	}
}
