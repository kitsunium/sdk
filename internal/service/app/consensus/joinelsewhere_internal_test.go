package consensus

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// #1422: a learner still waiting for its snapshot forwarded a JOIN and
// answered it only once it had applied the AddLearner itself, which it
// never did while its snapshot was held: the JOIN waited out its ten seconds
// and returned OUTCOME_UNKNOWN although the leader had committed the
// admission. It now refuses at once, NOT_LEADER naming the leader, and
// proposes nothing; the JOIN sent again reaches the leader, which admits it.
func TestAJoinThroughALearnerAwaitingItsSnapshotIsRefusedNotLeaderAtOnce(t *testing.T) {
	trs := map[coreconsensus.NodeID]*holdTransport{}
	var mu sync.Mutex
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{9, 2, 2}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		ht := &holdTransport{
			MemoryTransport: cfg.Transport.(*MemoryTransport),
			started:         make(chan coreconsensus.InstallOfferValue), release: make(chan struct{}),
		}
		trs[tn.id] = ht
		cfg.Transport = ht
		cfg.InitialTiming = steadyTiming()
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	for i := range 100 {
		propose(t, l.node, fmt.Sprintf("a%d=v", i))
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if first, _ := l.log.Bounds(); first > 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup: the leader never compacted its log")
		}
	}
	cutsSettled(t, l)
	mu.Lock()
	lt := trs[l.id]
	mu.Unlock()
	lt.holding.Store(true)
	a := c.join(t, "slot-a")
	awaitOffer(t, lt, l)
	if v, _ := a.sm.get("a0"); v != "" || a.node.Status().Role != coreconsensus.RoleLearner {
		t.Fatalf("setup: A is not a learner waiting for its snapshot: %+v", a.node.Status())
	}
	// B's JOIN reaches A alone.
	b := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
	id, _, err := InitLog(b.log, c.cid, "slot-b")
	if err != nil {
		t.Fatal(err)
	}
	b.id = id
	cut := func(on bool) {
		for _, x := range c.nodes {
			if x != a {
				c.net.Cut(id, x.id, on)
			}
		}
	}
	cut(true)
	c.nodes = append(c.nodes, b)
	c.start(b)
	jc := coreconsensus.JoinConfig{Stamp: must(b.log.Stamp())(t), Address: "mem"}
	tr := NewMemoryTransport(c.net, id)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err = tr.Join(ctx, jc)
	took := time.Since(start)
	if !errs.HasCode(err, coreconsensus.CodeNotLeader) || took > 2*time.Second {
		t.Fatalf("a JOIN through a learner waiting for its snapshot: %v after %v, want NOT_LEADER at once", err, took)
	}
	if lead, ok := LeaderHint(err); !ok || lead != l.id {
		t.Fatalf("the refusal names leader %d (%v), want %d: %v", lead, ok, l.id, err)
	}
	if _, ok := l.node.Membership().Member(id); ok {
		t.Fatal("the refused JOIN admitted B")
	}
	// Sent again with every link up, the JOIN is admitted, by the leader or
	// through it.
	cut(false)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	start = time.Now()
	if _, err = tr.Join(ctx2, jc); err != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("the JOIN sent again: %v after %v, want admitted at once", err, time.Since(start))
	}
	if _, ok := l.node.Membership().Member(id); !ok {
		t.Fatal("the JOIN answered is not in the leader's configuration")
	}
	lt.holding.Store(false)
	releaseHeld(t, lt, l)
}

// #1422: a voter level with its leader still forwards a JOIN and answers
// it once applied, at once.
func TestAJoinThroughALevelVoterIsForwarded(t *testing.T) {
	c := newClusterWith(t, &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{9, 2, 3}, size: 3})
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	propose(t, l.node, "a=1")
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if v, _ := f.sm.get("a"); v == "1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("setup: the follower never applied the write: %+v", f.node.Status())
		}
	}
	b := &testNode{log: NewMemoryLog(), snaps: NewMemorySnapshots(), sm: newKV()}
	id, _, err := InitLog(b.log, c.cid, "slot-b")
	if err != nil {
		t.Fatal(err)
	}
	b.id = id
	for _, x := range c.nodes {
		if x != f {
			c.net.Cut(id, x.id, true)
		}
	}
	c.nodes = append(c.nodes, b)
	c.start(b)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err = NewMemoryTransport(c.net, id).Join(ctx, coreconsensus.JoinConfig{Stamp: must(b.log.Stamp())(t), Address: "mem"})
	if err != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("a JOIN through a level voter: %v after %v, want admitted at once", err, time.Since(start))
	}
	if _, ok := l.node.Membership().Member(id); !ok {
		t.Fatal("the JOIN answered is not in the leader's configuration")
	}
}

// #1422: a TCP joiner whose only address is a member that cannot answer at
// once follows the leader's address that member's NOT_LEADER names, and is
// admitted there.
func TestATCPJoinerFollowsTheLeaderANotLeaderRefusalNames(t *testing.T) {
	ca, cid, ns := newTCPCluster(t)
	l := tcpSettledLeader(t, ns)
	x := &tcpNode{slot: "site-x"}
	x.cert, x.spki = ca.issue(t, SAN(cid, x.slot), nil)
	x.listen(t, ca)
	t.Cleanup(func() {
		if err := x.tr.Close(); err != nil {
			t.Errorf("close X: %v", err)
		}
	})
	var asked atomic.Int32
	x.tr.BindJoinReason(defaultGroup, func(context.Context, coreconsensus.AuthenticatedPeerValue, coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code, string) {
		asked.Add(1)
		return coreconsensus.PositionValue{}, coreconsensus.CodeNotLeader, notLeaderJoinReason(l.id, l.addr)
	})
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
		Addresses: []string{x.addr}, Stamp: must(j.log.Stamp())(t), Address: j.addr, Pins: append(pinsOf(ns), x.spki),
	})
	if err != nil {
		t.Fatalf("a JOIN refused NOT_LEADER naming the leader at %s: %v, want admitted there", l.addr, err)
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("X was asked %d times, want 1", n)
	}
	if _, ok := l.node.Membership().Member(j.id); !ok {
		t.Fatal("the joiner is not in the leader's configuration")
	}
}

// #1422: the reason of a JOIN refused NOT_LEADER names the leader, and its
// address when the reason's bound holds it, and the joiner reads both back.
func TestANotLeaderJoinReasonNamesTheLeader(t *testing.T) {
	type tc struct {
		name     string
		lead     coreconsensus.NodeID
		addr     string
		wantLead coreconsensus.NodeID
		wantAddr string
		wantOK   bool
	}
	long := strings.Repeat("h", MaxJoinReasonBytes)
	cases := []tc{
		{"leader and address", 7, "10.0.0.1:7000", 7, "10.0.0.1:7000", true},
		{"leader alone", 7, "", 7, "", true},
		{"an address the bound would cut is not named", 7, long, 7, "", true},
		{"an address with a rune the reason drops is not named", 7, "a\tb", 7, "", true},
		{"no leader known", 0, "10.0.0.1:7000", 0, "", false},
	}
	runCase := func(t *testing.T, c tc) {
		why := notLeaderJoinReason(c.lead, c.addr)
		if sanitizeReason(why) != why {
			t.Fatalf("the reason %q does not travel as written", why)
		}
		err := joinRefused(coreconsensus.CodeNotLeader, why)
		lead, addr, ok := leaderOfJoinRefusal(err)
		if lead != c.wantLead || addr != c.wantAddr || ok != c.wantOK {
			t.Fatalf("read back %d %q %v, want %d %q %v", lead, addr, ok, c.wantLead, c.wantAddr, c.wantOK)
		}
		if hint, hinted := LeaderHint(err); hinted != c.wantOK || hint != c.wantLead {
			t.Fatalf("LeaderHint %d %v, want %d %v", hint, hinted, c.wantLead, c.wantOK)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
	if _, _, ok := leaderOfJoinRefusal(joinRefused(coreconsensus.CodeJoinRefused, notLeaderJoinReason(7, "x:1"))); ok {
		t.Fatal("a JOIN_REFUSED read as naming a leader")
	}
}

// #1422: a joiner tries each target once, the leader a refusal names first
// among those left, and follows at most joinFollows leaders.
func TestAJoinQueueFollowsTheNamedLeaderOnceEach(t *testing.T) {
	type tc struct {
		name    string
		targets []string
		follow  map[string]string
		want    string
	}
	cases := []tc{
		{"no refusal names a leader", []string{"a", "b", "c"}, nil, "a b c"},
		{"the leader is tried next", []string{"a", "b", "c"}, map[string]string{"a": "c"}, "a c b"},
		{"a leader outside the targets", []string{"a", "b"}, map[string]string{"a": "z"}, "a z b"},
		{"a leader tried already", []string{"a", "b"}, map[string]string{"b": "a"}, "a b"},
		{"at most joinFollows leaders", []string{"a"}, map[string]string{"a": "x", "x": "y", "y": "z"}, "a x y"},
	}
	runCase := func(t *testing.T, c tc) {
		q := newJoinQueue(c.targets)
		var got []string
		for k, ok := q.next(); ok; k, ok = q.next() {
			got = append(got, k)
			if to, named := c.follow[k]; named {
				q.follow(to)
			}
		}
		if s := strings.Join(got, " "); s != c.want {
			t.Fatalf("tried %q, want %q", s, c.want)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
