package consensus

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// loggedCluster is a cluster of three founders over the memory network with
// its admission model, as the TCP transport admits by key, cutting a
// snapshot every entries, every node logging its Warn lines to its own
// attrLogger.
func loggedCluster(t *testing.T, cid coreconsensus.ClusterID, every uint64) (*cluster, func(id coreconsensus.NodeID) *attrLogger) {
	t.Helper()
	logs := map[coreconsensus.NodeID]*attrLogger{}
	var mu sync.Mutex
	logOf := func(id coreconsensus.NodeID) *attrLogger {
		mu.Lock()
		defer mu.Unlock()
		//: one logger per node, kept across its restarts.
		if logs[id] == nil {
			logs[id] = &attrLogger{}
		}
		return logs[id]
	}
	net := NewMemoryNetwork()
	net.admission = true
	c := &cluster{t: t, net: net, cid: cid, every: every, size: 3}
	c.configure = func(tn *testNode, cfg *Config) { cfg.Logger = logOf(tn.id) }
	return newClusterWith(t, c), logOf
}

// peerFlags is the flags of peer id in the leader l's health verdict, and
// whether l lists it.
func peerFlags(l *testNode, id coreconsensus.NodeID) (PeerFlags, bool) {
	//: the line of id among the leader's peers.
	for _, p := range l.node.Health().Peers {
		//: id's own line.
		if p.ID == id {
			return p.Flags, true
		}
	}
	return 0, false
}

// waitUnheard waits until the leader l has not heard peer id for twice the
// minimum election timeout, or never did in its term: id no longer answers,
// for #603's guard as for the health verdict, whose windows are that
// timeout.
func waitUnheard(t *testing.T, l *testNode, id coreconsensus.NodeID) {
	t.Helper()
	window := 2 * testTimingFast().ElectionTimeoutMin
	waitFor(t, "the leader no longer hears the peer", func() bool {
		//: id's line among the leader's peers.
		for _, p := range l.node.Health().Peers {
			//: unheard long enough, or never heard.
			if p.ID == id {
				return p.LastContact < 0 || p.LastContact >= window
			}
		}
		return false
	})
}

// appendedLine is the attributes of the leader's audit line for the
// administrative entry of kind, change and target it appended, nil when it
// logged none (ADR 0152 D8).
func appendedLine(lg *attrLogger, kind coreconsensus.EntryKind, change uint8, target coreconsensus.NodeID) map[string]string {
	lg.mu.Lock()
	defer lg.mu.Unlock()
	//: the last line for that entry.
	for i := len(lg.lines) - 1; i >= 0; i-- {
		line := lg.lines[i]
		//: the audit line of the entry asked for.
		if line["msg"] == "consensus: administrative entry appended" && line["kind"] == fmt.Sprint(uint64(kind)) &&
			line["change"] == fmt.Sprint(uint64(change)) && line["target"] == fmt.Sprint(uint64(target)) {
			return line
		}
	}
	return nil
}

// onTheMerits runs op until it is admitted or refused MEMBERSHIP_INVALID,
// for up to ten seconds: a change is refused for another reason while the
// previous one, or a new leader's no-op, is pending.
func onTheMerits(op func() error) error {
	deadline := time.Now().Add(10 * time.Second)
	//: until an answer on the merits, or the deadline.
	for {
		err := op()
		//: admitted, refused on its merits, or out of time.
		if err == nil || errs.HasCode(err, coreconsensus.CodeMembershipInvalid) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// #773, option (c): a DenyPin is admitted whatever it leaves a stopped voter
// with, and the risk is said instead. A voter (A) down knows {A, B, C}; D,
// E and F joined and were promoted meanwhile, and D leads. C's removal is
// admitted, since B, pinned, vouches for A; then B's pin is denied, the
// first step of the key-compromise runbook: A is left with no member it
// knows that would answer its PreVote under the key it pins (#551). The
// DenyPin stays admitted, and D says so: its health names A, stranded by a
// withdrawn pin; the audit line of the DenyPin names A; and the move of E,
// which #603's guard refuses because of A, names that cause. Nothing said
// any of it: the status was silent, the audit line named the change alone,
// and the refusal was a bare MEMBERSHIP_INVALID.
func TestADenyPinThatLeavesAStoppedVoterNoVoucherIsSaidEverywhere(t *testing.T) {
	c, logOf := loggedCluster(t, coreconsensus.ClusterID{7, 7, 3}, 1000)
	a, b, cc := c.nodes[0], c.nodes[1], c.nodes[2]
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	lead := func() *testNode { return c.leader(5 * time.Second) }
	tok := propose(t, c.settledLeader(5*time.Second).node, "a=1")
	waitFor(t, "A applied a=1", func() bool { return a.node.Status().Applied >= tok.Index })

	// A goes down knowing {A, B, C}. D, E and F join and are promoted.
	c.kill(a)
	var joined []*testNode
	//: D, E and F one after the other.
	for _, slot := range []string{"slot-d", "slot-e", "slot-f"} {
		j := c.join(t, slot)
		retryAdmin(t, "promote "+slot, func() error { return lead().node.Promote(ctx, "op:test", j.id) })
		joined = append(joined, j)
	}
	d, e := joined[0], joined[1]
	retryAdmin(t, "transfer to D", func() error { return lead().node.TransferLeadership(ctx, "op:test", d.id) })
	waitFor(t, "D leads", func() bool { return d.node.Status().Role == coreconsensus.RoleLeader })
	waitUnheard(t, d, a.id)

	// C answers, and B, pinned, still vouches for A: C's removal is admitted.
	retryAdmin(t, "remove C", func() error { return d.node.Remove(ctx, "op:test", cc.id) })
	c.kill(cc)
	//: B vouches for A: nothing to say yet.
	if f, ok := peerFlags(d, a.id); !ok || f.Has(PeerStranded) {
		t.Fatalf("setup: D's health lists A %v with flags %#b before B's pin was denied", ok, f)
	}

	// B's pin is denied: C, gone, and B, refused, leave A no voucher.
	//: the DenyPin is never refused for what it leaves a stopped voter with.
	if err := onTheMerits(func() error { return d.node.DenyPin(ctx, "op:test", b.id) }); err != nil {
		t.Fatalf("DenyPin(B) = %v, want it admitted: a key's fence never waits for a voter that is down", err)
	}

	aID := fmt.Sprint(uint64(a.id))
	// The status: D's health names A, and the withdrawn pin.
	//: the status must name A, stranded by the DenyPin.
	if f, ok := peerFlags(d, a.id); !ok || !f.Has(PeerStranded|PeerPinDenied) {
		t.Errorf("D's health lists A %v with flags %#b: want PeerStranded and PeerPinDenied, the DenyPin left it no voucher", ok, f)
	}
	// The audit: the line of the DenyPin D appended names A.
	line := appendedLine(logOf(d.id), coreconsensus.EntryMembership, wire.ChangeDenyPin, b.id)
	//: the audit line must name A and the cause.
	if line == nil || !strings.Contains(line["isolated"], aID) || !strings.Contains(line["isolated"], "pin-denied") {
		t.Errorf("D's audit line for the DenyPin of B = %v: want its isolated attribute naming A (%s) and pin-denied", line, aID)
	}
	// The refusal: E's move, refused because of A, names the DenyPin.
	err := onTheMerits(func() error { return d.node.UpdateAddress(ctx, "op:test", e.id, "elsewhere") })
	//: the guard refuses the move while A is down.
	if !errs.HasCode(err, coreconsensus.CodeMembershipInvalid) {
		t.Fatalf("UpdateAddress(E) while A, down, has no voucher = %v, want MEMBERSHIP_INVALID", err)
	}
	//: the refusal must name A and the DenyPin.
	if why := fieldOf(err, "why"); !strings.Contains(why, "DenyPin") || !strings.Contains(fieldOf(err, "isolated"), aID) {
		t.Errorf("the refusal of E's move says why %q, isolated %q: want the DenyPin named, and A (%s)", why, fieldOf(err, "isolated"), aID)
	}
}
