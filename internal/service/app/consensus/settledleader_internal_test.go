package consensus

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// noOpHoldLog is a memory log whose appends carrying a term's no-op wait
// until its gate opens: a leader is elected, and its no-op commits nowhere
// while the gate is shut.
type noOpHoldLog struct {
	*MemoryLog
	gate <-chan struct{}
}

// Append waits for the gate before an append that carries a no-op.
func (l *noOpHoldLog) Append(entries []coreconsensus.EntryValue, hs *coreconsensus.HardState) error {
	if slices.ContainsFunc(entries, func(e coreconsensus.EntryValue) bool { return e.Kind == coreconsensus.EntryNoOp }) {
		<-l.gate
	}
	return l.MemoryLog.Append(entries, hs)
}

// noOpHold is how long the no-ops stay held once a node first reports
// itself leader: long past a JOIN sent to it at once, and half the maximum
// election timeout of steadyTiming, the cluster's. A leader whose durable
// index trails its log for a maximum election timeout hands its leadership
// over (watchProgress, #554), and the JOIN then met a transfer, NOT_LEADER.
const noOpHold = 500 * time.Millisecond

// #1458: the TCP JOIN tests took their leader from tcpLeader, which
// returns a node the moment it reports itself leader. raft admits a
// membership change only once its term's no-op committed (checkMembership),
// so a JOIN that reached that leader first, as on a slow NetBSD guest, was
// refused MEMBERSHIP_CHANGE_PENDING. The no-ops are held here for noOpHold
// after the first election, and the JOIN sent to the leader
// tcpSettledLeader returns is admitted: that leader committed an entry of
// its own term. Sent to tcpLeader's, it is refused on every run.
func TestATCPJoinSentToTheSettledLeaderIsAdmitted(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	ca, cid, ns := newTCPClusterWith(t, func(c *Config) {
		c.Log = &noOpHoldLog{MemoryLog: c.Log.(*MemoryLog), gate: gate}
		c.InitialTiming = steadyTiming()
	})
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer release()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if slices.ContainsFunc(ns, func(tn *tcpNode) bool { return tn.node.Status().Role == coreconsensus.RoleLeader }) {
				break
			}
		}
		select {
		case <-time.After(noOpHold):
		case <-stop:
		}
	}()
	// Before the cluster stops: a held append would hold its node's close.
	t.Cleanup(func() {
		close(stop)
		<-done
	})
	l := tcpSettledLeader(t, ns)
	j := &tcpNode{slot: "site-j", log: NewMemoryLog(), snaps: NewMemorySnapshots()}
	j.cert, j.spki = ca.issue(t, SAN(cid, j.slot), nil)
	j.listen(t, ca)
	id, _, err := InitLog(j.log, cid, j.slot)
	if err != nil {
		t.Fatal(err)
	}
	j.id = id
	j.start(t, cid)
	t.Cleanup(func() { j.stop(t) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err = j.tr.Join(ctx, coreconsensus.JoinConfig{
		Addresses: []string{l.addr}, Stamp: must(j.log.Stamp())(t), Address: j.addr, Pins: pinsOf(ns),
	})
	if err != nil {
		t.Fatalf("a JOIN sent to the leader at %s: %v, want admitted", l.addr, err)
	}
	if _, ok := l.node.Membership().Member(j.id); !ok {
		t.Fatal("the joiner is not in the leader's configuration")
	}
}
