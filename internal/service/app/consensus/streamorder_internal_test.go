package consensus

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

// rolesOf is a waiterRole lookup over a map, waiterGone for any peer it does
// not name: raft's view of the waiting peers when a slot frees, as a test
// sets it.
func rolesOf(m map[coreconsensus.NodeID]waiterRole) func(coreconsensus.NodeID) waiterRole {
	return func(to coreconsensus.NodeID) waiterRole { return m[to] }
}

// queueOf is a queue of peers that asked in the order given, none passed.
func queueOf(to ...coreconsensus.NodeID) []queuedStream {
	w := make([]queuedStream, 0, len(to))
	for _, id := range to {
		w = append(w, queuedStream{to: id})
	}
	return w
}

// served takes every waiter of w in nextWaiter's order, the roles fixed,
// and returns the peers in the order they were served. Nothing is left
// waiting once nextWaiter serves nobody: a waiter that is gone is dropped,
// never kept.
func served(t *testing.T, w []queuedStream, role func(coreconsensus.NodeID) waiterRole, limit int) []coreconsensus.NodeID {
	t.Helper()
	var order []coreconsensus.NodeID
	for len(w) > 0 {
		var to coreconsensus.NodeID
		var ok bool
		if w, to, ok = nextWaiter(w, role, limit); !ok {
			break
		}
		order = append(order, to)
	}
	if len(w) != 0 {
		t.Fatalf("served %v, and %+v still wait with nobody to serve", order, w)
	}
	return order
}

// #1483: a freed slot serves the first voter before a learner that asked
// first, and only the learners that asked before that voter are passed.
// Among voters, and among learners, the first to ask goes first.
func TestAFreedSlotServesTheFirstVoterBeforeAnEarlierLearner(t *testing.T) {
	const l1, v1, l2, v2 = 11, 21, 12, 22
	roles := rolesOf(map[coreconsensus.NodeID]waiterRole{l1: waiterLearner, l2: waiterLearner, v1: waiterVoter, v2: waiterVoter})
	w, to, ok := nextWaiter(queueOf(l1, v1, l2), roles, 2)
	if !ok || to != v1 {
		t.Fatalf("served %d (%v), want the voter %d before the learner that asked first", to, ok, v1)
	}
	if want := []queuedStream{{to: l1, passed: 1}, {to: l2}}; !slices.Equal(w, want) {
		t.Fatalf("left %+v, want %+v: only the learner that asked before the voter is passed", w, want)
	}
	if got, want := served(t, queueOf(l1, v1, l2, v2), roles, 4), []coreconsensus.NodeID{v1, v2, l1, l2}; !slices.Equal(got, want) {
		t.Fatalf("served %v, want %v: the voters first, each kind in the order it asked", got, want)
	}
	if got, want := served(t, queueOf(v1, l1, v2), roles, 2), []coreconsensus.NodeID{v1, v2, l1}; !slices.Equal(got, want) {
		t.Fatalf("served %v, want %v", got, want)
	}
	if got, want := served(t, queueOf(l1, l2), roles, 2), []coreconsensus.NodeID{l1, l2}; !slices.Equal(got, want) {
		t.Fatalf("learners alone: served %v, want %v, first asked first served", got, want)
	}
	if w, _, ok := nextWaiter(nil, roles, 2); ok || len(w) != 0 {
		t.Fatalf("an empty queue served a peer: %+v", w)
	}
}

// #1483: a learner is never starved by voters that keep coming back. It is
// passed by at most limit voters' streams — the configuration's voters
// besides the leader — then goes before the next voter, here a voter that
// asks again before each freed slot.
func TestALearnerIsPassedAtMostAsManyTimesAsThereAreOtherVoters(t *testing.T) {
	const learner = 11
	for limit := 1; limit <= 4; limit++ {
		t.Run(fmt.Sprintf("limit %d", limit), func(t *testing.T) {
			roles := map[coreconsensus.NodeID]waiterRole{learner: waiterLearner}
			w := queueOf(learner)
			for slot := range limit + 1 {
				voter := coreconsensus.NodeID(21 + slot%2)
				roles[voter] = waiterVoter
				w = append(w, queuedStream{to: voter})
				var to coreconsensus.NodeID
				var ok bool
				w, to, ok = nextWaiter(w, rolesOf(roles), limit)
				switch {
				case !ok:
					t.Fatalf("slot %d served nobody", slot)
				case slot < limit && to != voter:
					t.Fatalf("slot %d served %d, want the voter %d: the learner was passed %d times of %d", slot, to, voter, slot, limit)
				case slot == limit && to != learner:
					t.Fatalf("slot %d served %d, want the learner %d, passed %d times already", slot, to, learner, limit)
				case slot == limit && (len(w) != 1 || w[0].to != voter):
					t.Fatalf("left %+v, want the voter %d alone", w, voter)
				}
			}
		})
	}
}

// #1483: a waiter's place follows its role in raft when the slot frees, not
// when it asked: a learner promoted while it waits goes before a voter that
// asked after it, a voter removed — gone from raft's snapshot state — is
// dropped, and a voter that became a learner waits behind a voter that
// asked later.
func TestAWaitersRoleIsReadWhenTheSlotFrees(t *testing.T) {
	const a, b = 31, 32
	asked := map[coreconsensus.NodeID]waiterRole{a: waiterLearner, b: waiterVoter}
	cases := []struct {
		name string
		now  map[coreconsensus.NodeID]waiterRole
		want []coreconsensus.NodeID
	}{
		{name: "as they asked", now: asked, want: []coreconsensus.NodeID{b, a}},
		{name: "the learner promoted", now: map[coreconsensus.NodeID]waiterRole{a: waiterVoter, b: waiterVoter}, want: []coreconsensus.NodeID{a, b}},
		{name: "the voter removed", now: map[coreconsensus.NodeID]waiterRole{a: waiterLearner}, want: []coreconsensus.NodeID{a}},
		{name: "the voter made a learner", now: map[coreconsensus.NodeID]waiterRole{a: waiterLearner, b: waiterLearner}, want: []coreconsensus.NodeID{a, b}},
		{name: "both gone", now: map[coreconsensus.NodeID]waiterRole{}, want: nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := served(t, queueOf(a, b), rolesOf(c.now), 2); !slices.Equal(got, c.want) {
				t.Fatalf("served %v, want %v", got, c.want)
			}
		})
	}
}

// queuedPeers are the peers waiting for a stream on leader l, in the order
// they asked.
func queuedPeers(l *testNode) []coreconsensus.NodeID {
	var to []coreconsensus.NodeID
	l.node.exec(func() {
		for _, q := range l.node.streamWait {
			to = append(to, q.to)
		}
	})
	return to
}

// awaitQueue waits until the peers waiting for a stream on leader l are
// want, in that order, with the one stream in flight still to inFlight.
func awaitQueue(t *testing.T, l *testNode, inFlight coreconsensus.NodeID, want ...coreconsensus.NodeID) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		got, streaming := queuedPeers(l), streamsInFlight(l)
		if !slices.Equal(streaming, []coreconsensus.NodeID{inFlight}) {
			t.Fatalf("the stream in flight is %v, want %d's alone, %v waiting", streaming, inFlight, got)
		}
		if slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%v wait for a stream, want %v: the leader's status %+v", got, want, l.node.Status())
		}
	}
}

// awaitStreamTo waits for the leader's held transport to be offered a
// stream, and requires it to go to peer to.
func awaitStreamTo(t *testing.T, lt *holdTransport, l *testNode, to coreconsensus.NodeID) {
	t.Helper()
	awaitOffer(t, lt, l)
	if got := streamsInFlight(l); !slices.Equal(got, []coreconsensus.NodeID{to}) {
		t.Fatalf("the stream that started goes to %v, want %d; %v still wait", got, to, queuedPeers(l))
	}
}

// heldQueue is a cluster of three founders whose leader holds learner a's
// snapshot stream in flight under MaxSnapshotStreams 1, learner b and voter
// lag waiting behind it in the order they asked.
type heldQueue struct {
	c            *cluster
	lt           *holdTransport
	l, lag, a, b *testNode
}

// newHeldQueue builds a heldQueue: lag, stopped while the leader compacted
// past its log, restarts once a's stream is held and b waits.
func newHeldQueue(t *testing.T) *heldQueue {
	t.Helper()
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
		if old := trs[tn.id]; old != nil {
			ht.started, ht.release = old.started, old.release
			ht.holding.Store(old.holding.Load())
		}
		trs[tn.id] = ht
		cfg.Transport = ht
		cfg.InitialTiming = steadyTiming()
		cfg.MaxSnapshotStreams = 1
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	lag := c.otherThan(l)
	c.kill(lag)
	for i := range 200 {
		propose(t, l.node, fmt.Sprintf("a%d=v", i))
	}
	compactedPastItsEnd(t, l, lag)
	mu.Lock()
	lt := trs[l.id]
	mu.Unlock()
	lt.holding.Store(true)
	a := c.join(t, "slot-a")
	awaitStreamTo(t, lt, l, a.id)
	b := c.joinThrough(t, "slot-b", a, l)
	awaitQueue(t, l, a.id, b.id)
	hearOnlyTheLeader(c, l, lag)
	c.start(lag)
	awaitQueue(t, l, a.id, b.id, lag.id)
	return &heldQueue{c: c, lt: lt, l: l, lag: lag, a: a, b: b}
}

// roleNow is where peer to stands for leader l's queue, and how many voters
// besides l the configuration has.
func roleNow(l *testNode, to coreconsensus.NodeID) (waiterRole, int) {
	var r waiterRole
	var limit int
	l.node.exec(func() {
		role, n := l.node.waiterRoles()
		r, limit = role(to), n
	})
	return r, limit
}

// statOf is raft's own view of peer to on leader l, read apart from the
// queue's: its flags, and false when PeerStats does not name it.
func statOf(l *testNode, to coreconsensus.NodeID) (raft.PeerStatFlags, bool) {
	var flags raft.PeerStatFlags
	var ok bool
	l.node.exec(func() {
		for _, st := range l.node.r.PeerStats() {
			if st.ID == to {
				flags, ok = st.Flags, true
			}
		}
	})
	return flags, ok
}

// awaitLast waits until every node of want applied the write "last=v".
func awaitLast(t *testing.T, want ...*testNode) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		behind := slices.DeleteFunc(slices.Clone(want), func(tn *testNode) bool {
			v, _ := tn.sm.get("last")
			return v == "v"
		})
		if len(behind) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d peers never caught up, the first %+v", len(behind), behind[0].node.Status())
		}
	}
}

// #1483: with MaxSnapshotStreams at 1, a voter that needs a snapshot is
// streamed before a learner that asked first, once the slot frees, and the
// stream in flight is never cut for it. Learner a's stream is held in
// flight; learner b asks next and waits; voter lag, stopped while the
// leader compacted past its log, restarts and asks last. The queue was
// served first asked first served, b before lag, and the cluster ran
// without redundancy for as long as b's stream lasted. Now a's stream
// ends, lag is streamed, then b; each installs, and none needs a second
// snapshot.
func TestAVoterIsStreamedBeforeALearnerThatAskedFirst(t *testing.T) {
	q := newHeldQueue(t)
	l, lt, a, b, lag := q.l, q.lt, q.a, q.b, q.lag
	learner, limit := roleNow(l, b.id)
	voter, _ := roleNow(l, lag.id)
	if learner != waiterLearner || voter != waiterVoter || limit != 2 {
		t.Fatalf("raft holds b as %d and lag as %d, %d other voters; want a learner (%d), a voter (%d) and 2", learner, voter, limit, waiterLearner, waiterVoter)
	}
	// The voter waiting cuts no stream: a's goes on, and none starts.
	select {
	case <-lt.started:
		t.Fatalf("a stream started while a's was held: in flight %v", streamsInFlight(l))
	case <-time.After(300 * time.Millisecond):
	}
	awaitQueue(t, l, a.id, b.id, lag.id)
	releaseHeld(t, lt, l)
	awaitStreamTo(t, lt, l, lag.id)
	if got := queuedPeers(l); !slices.Equal(got, []coreconsensus.NodeID{b.id}) {
		t.Fatalf("%v wait, want the learner b alone", got)
	}
	releaseHeld(t, lt, l)
	awaitStreamTo(t, lt, l, b.id)
	lt.holding.Store(false)
	releaseHeld(t, lt, l)
	propose(t, l.node, "last=v")
	awaitLast(t, a, b, lag)
	if got, cut := lt.streams.Load(), lt.cancelled.Load(); got != 3 || cut != 0 {
		t.Fatalf("%d snapshot streams, %d cut, want 3 and none", got, cut)
	}
}

// #1483, O1 of its safety review: a Ready that names a learner before a
// voter, a slot free, streams the voter first. Each peer named went to the
// free slot in the Ready's order, so the learner took it and the voter
// waited for its whole stream. The Ready is handed on the loop while a's
// stream is held and the slot it holds is raised to two: b, then c, a
// founder raft feeds its log and holds in no snapshot state, then lag. lag
// takes the free slot, b waits, and c is streamed nothing.
func TestAReadysVoterIsStreamedBeforeItsLearner(t *testing.T) {
	q := newHeldQueue(t)
	l, lt, a, b, lag := q.l, q.lt, q.a, q.b, q.lag
	var third *testNode
	for _, tn := range q.c.nodes[:3] {
		if tn != l && tn != lag {
			third = tn
		}
	}
	if flags, ok := statOf(l, third.id); !ok || flags.Has(raft.StatSnapshot) || !flags.Has(raft.StatVoter) {
		t.Fatalf("setup: raft holds the founder %d with flags %v (named %v), want a voter in no snapshot state", third.id, flags, ok)
	}
	var streaming, waiting []coreconsensus.NodeID
	l.node.exec(func() {
		n := l.node
		n.streamWait = n.streamWait[:0]
		n.cfg.MaxSnapshotStreams = 2
		n.handleOne(&raft.ReadyValue{SendSnapshot: []coreconsensus.NodeID{b.id, third.id, lag.id}})
		n.cfg.MaxSnapshotStreams = 1
		streaming = slices.Sorted(maps.Keys(n.sendStop))
		for _, w := range n.streamWait {
			waiting = append(waiting, w.to)
		}
	})
	if want := slices.Sorted(slices.Values([]coreconsensus.NodeID{a.id, lag.id})); !slices.Equal(streaming, want) {
		t.Fatalf("the Ready started streams to %v, want a's and the voter lag's %v; %v wait", streaming, want, waiting)
	}
	if !slices.Equal(waiting, []coreconsensus.NodeID{b.id}) {
		t.Fatalf("%v wait, want the learner b alone", waiting)
	}
	awaitOffer(t, lt, l)
	releaseHeld(t, lt, l)
	releaseHeld(t, lt, l)
	awaitStreamTo(t, lt, l, b.id)
	lt.holding.Store(false)
	releaseHeld(t, lt, l)
	propose(t, l.node, "last=v")
	awaitLast(t, a, b, lag, third)
	if got, cut := lt.streams.Load(), lt.cancelled.Load(); got != 3 || cut != 0 {
		t.Fatalf("%d snapshot streams, %d cut, want 3 and none", got, cut)
	}
}

// #1483, O7 of its safety review: a waiter removed before the slot frees is
// dropped, never streamed. Learner b, waiting behind a's stream, is removed;
// raft's PeerStats no longer names it, so when a's stream ends the voter
// lag is streamed and nobody is left waiting. A waiter never taken for gone
// stayed queued, and the removed peer was streamed a snapshot next.
func TestAWaiterRemovedBeforeTheSlotFreesIsDropped(t *testing.T) {
	q := newHeldQueue(t)
	l, lt, a, b, lag := q.l, q.lt, q.a, q.b, q.lag
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	retryAdmin(t, "remove the waiting learner b", func() error { return l.node.Remove(ctx, "op:test", b.id) })
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, ok := statOf(l, b.id); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("setup: raft still holds the removed b: %+v", l.node.Status())
		}
	}
	awaitQueue(t, l, a.id, b.id, lag.id)
	releaseHeld(t, lt, l)
	awaitStreamTo(t, lt, l, lag.id)
	if got := queuedPeers(l); len(got) != 0 {
		t.Fatalf("%v wait once lag's stream started, want nobody: the removed b is dropped", got)
	}
	lt.holding.Store(false)
	releaseHeld(t, lt, l)
	propose(t, l.node, "last=v")
	awaitLast(t, a, lag)
	if got, cut := lt.streams.Load(), lt.cancelled.Load(); got != 2 || cut != 0 {
		t.Fatalf("%d snapshot streams, %d cut, want 2 — a's and lag's — and none", got, cut)
	}
}
