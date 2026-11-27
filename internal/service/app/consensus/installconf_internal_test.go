package consensus

import (
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

// heldRestore is a state machine whose Restore says it was entered, then
// waits for release: the install is held on the applier, past the offer's
// last check on the event loop and before the applied position it gives.
type heldRestore struct {
	coreconsensus.StateMachine
	entered chan<- struct{}
	release <-chan struct{}
}

func (h heldRestore) Restore(r io.Reader, meta coreconsensus.SnapshotMetaValue) error {
	//: a second restore finds the signal already given and is not held.
	select {
	case h.entered <- struct{}{}:
	default:
	}
	<-h.release
	return h.StateMachine.Restore(r, meta)
}

// A node that catches up by installing a snapshot publishes the applied
// position the snapshot gives together with the configuration it carries:
// a reader that waited for Applied to cover a removal reads a Membership that
// holds the removed member's key retired. restoreFrom sets the applied
// position on the applier, and installed moves raft onto the snapshot's
// configuration one exec later, on the loop: a turn between the two
// published Applied past the removal with the configuration from before it,
// which TestANodeThatInstallsASnapshotRefusesItsRetiredKeys read under
// GOMAXPROCS=1. The turn is forced here: the loop is held while the restore
// runs, and the observation is queued on it ahead of installed's exec.
func TestAnInstallPublishesItsAppliedPositionWithItsConfiguration(t *testing.T) {
	type tc struct{ name string }
	runCase := func(t *testing.T, _ tc) {
		c, _ := fileSnapshotCluster(t)
		raiseNodesTo(t, liveNodes(c), raft.Protocol3)
		lag := c.otherThan(c.settledLeader(5 * time.Second))
		entered, release := make(chan struct{}, 1), make(chan struct{})
		prev := c.configure
		c.configure = func(tn *testNode, cfg *Config) {
			prev(tn, cfg)
			//: only the node that installs restores through the hold.
			if tn == lag {
				cfg.Machine = heldRestore{StateMachine: cfg.Machine, entered: entered, release: release}
			}
		}
		end := stopBehind(c, lag)
		key := [32]byte{0x99}
		//: the member whose key the removal retires.
		if code, why := joinOn(t, c, 991, "y", key); code != 0 {
			t.Fatalf("setup: JOIN: %v %q", code, why)
		}
		removeOn(t, c, 991)
		removal := c.settledLeader(5 * time.Second).node.Membership().Index
		//: entries enough for a cut past the stopped node's end.
		for i := range 70 {
			proposeOnLeader(t, c, "k"+string(rune('a'+i%26))+"=1")
		}
		compactedPast(t, c, end+1)
		c.net.Isolate(lag.id, false)
		c.start(lag)
		n := lag.node
		//: the node catches up by an install, never by appends.
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			close(release)
			t.Fatalf("the node stopped behind index %d never restored a snapshot", end)
		}
		held, free := make(chan struct{}), make(chan struct{})
		go n.exec(func() { close(held); <-free })
		<-held
		var applied uint64
		var conf coreconsensus.MembershipValue
		var snapAt uint64
		observed := make(chan struct{})
		// Queued while the loop is held and before the restore ends, so
		// ahead of installed's exec: execc is a FIFO.
		n.execc <- func() {
			applied, conf = n.Status().Applied, n.Membership()
			close(observed)
		}
		close(release)
		//: the restore gives the applied position before installed runs.
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
			n.am.Lock()
			at := n.applied
			n.am.Unlock()
			//: the snapshot covers the removal.
			if at >= removal {
				snapAt = at
				break
			}
			//: a restore that never ends fails here, not in the cleanup.
			if time.Now().After(deadline) {
				close(free)
				t.Fatalf("the restore never gave an applied position at or past the removal %d: %d", removal, at)
			}
		}
		close(free)
		<-observed
		//: while the install is held the published position stays under the removal: it never runs ahead of the configuration.
		if applied >= removal {
			t.Fatalf("Status().Applied is %d, at or past the removal at %d, while the install is held and raft is still on the configuration at %d", applied, removal, conf.Index)
		}
		//: once the install is done the hold is let go and the published position joins the applied one.
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
			pub := n.Status().Applied
			//: the published position has caught up with what the install applied.
			if pub >= snapAt {
				//: and the configuration it publishes holds the removed member's key retired.
				if !n.Membership().IsRetiredKey(key) {
					t.Fatalf("Status().Applied is %d, past the removal at %d, and Membership() does not hold the removed member's key retired", pub, removal)
				}
				break
			}
			//: a hold never let go keeps the published position below the applied one.
			if time.Now().After(deadline) {
				t.Fatalf("Status().Applied is %d, below the %d the install applied, after it ended", pub, snapAt)
			}
		}
	}
	for _, tt := range []tc{{name: "a removal the snapshot covers"}} {
		t.Run(tt.name, func(t *testing.T) { runCase(t, tt) })
	}
}

// appliedAndHolds is n's applied position and the number of installs
// holding the published one, read under the lock that guards both.
func appliedAndHolds(n *Node) (uint64, int) {
	n.am.Lock()
	defer n.am.Unlock()
	return n.applied, n.pubHolds
}

// A node restarted on a snapshot publishes, from its first status, an
// applied position the configuration published with it covers, and never
// one held below the snapshot it restored: a start restores before raft is
// built on the snapshot's configuration, so it takes no hold, and a hold
// taken there would have no installed to let it go (#1584).
func TestAStartOnASnapshotPublishesItsAppliedPositionWithItsConfiguration(t *testing.T) {
	type tc struct{ name string }
	runCase := func(t *testing.T, _ tc) {
		c, stores := fileSnapshotCluster(t)
		raiseNodesTo(t, liveNodes(c), raft.Protocol3)
		f := c.otherThan(c.settledLeader(5 * time.Second))
		key := [32]byte{0x98}
		//: the member whose key the removal retires.
		if code, why := joinOn(t, c, 992, "z", key); code != 0 {
			t.Fatalf("setup: JOIN: %v %q", code, why)
		}
		removeOn(t, c, 992)
		removal := c.settledLeader(5 * time.Second).node.Membership().Index
		var tok coreconsensus.TokenValue
		//: entries enough for every log to start past the removal.
		for i := range 70 {
			tok = proposeOnLeader(t, c, fmt.Sprintf("s%d=%d", i, i))
		}
		compactedPast(t, c, removal)
		c.kill(f)
		c.start(f)
		n := f.node
		st := n.Status()
		conf := n.Membership()
		_, holds := appliedAndHolds(n)
		//: a start takes no hold.
		if holds != 0 {
			t.Fatalf("a start on a snapshot left %d hold(s) on the published applied position", holds)
		}
		s, _ := stores(f.id)
		metas := must(s.List())(t)
		//: the restart restored a snapshot past the removal.
		if len(metas) == 0 || metas[0].Position.Index < removal {
			t.Fatalf("no snapshot at or past the removal %d to restart on: %+v", removal, metas)
		}
		//: the first status read is not held below the snapshot the start restored.
		if st.Applied < metas[0].Position.Index {
			t.Fatalf("Status().Applied is %d after a start on the snapshot at %d", st.Applied, metas[0].Position.Index)
		}
		//: Applied covers the removal, so the configuration must too.
		if st.Applied >= removal && !conf.IsRetiredKey(key) {
			t.Fatalf("Status().Applied is %d, past the removal at %d, and Membership() is the configuration at %d, which does not hold the removed member's key retired: %v", st.Applied, removal, conf.Index, conf.RetiredKeys)
		}
		c.waitApplied(tok.Index, 10*time.Second)
	}
	for _, tt := range []tc{{name: "a removal the snapshot covers"}} {
		t.Run(tt.name, func(t *testing.T) { runCase(t, tt) })
	}
}

// refusedOnce is a state machine whose first Restore is refused, and says
// so; every later one restores.
type refusedOnce struct {
	coreconsensus.StateMachine
	refused *atomic.Bool
	said    chan<- struct{}
}

func (r refusedOnce) Restore(rd io.Reader, meta coreconsensus.SnapshotMetaValue) error {
	//: the first restore is refused before reading anything.
	if r.refused.CompareAndSwap(false, true) {
		r.said <- struct{}{}
		return errors.New("refused by the test")
	}
	return r.StateMachine.Restore(rd, meta)
}

// An install whose restore is refused lets go of the hold it took on the
// published applied position: left, the hold of the next install, which
// restores, is let go by its installed, and the first one kept
// Status().Applied below every entry applied after, for good (#1584).
// The refusal now stops the node (#1582): the hold is let go all the same,
// and the node started again is installed.
func TestARefusedRestoreDoesNotHoldThePublishedAppliedPosition(t *testing.T) {
	type tc struct{ name string }
	runCase := func(t *testing.T, _ tc) {
		c, _ := fileSnapshotCluster(t)
		lag := c.otherThan(c.settledLeader(5 * time.Second))
		said := make(chan struct{}, 1)
		var refused atomic.Bool
		prev := c.configure
		c.configure = func(tn *testNode, cfg *Config) {
			prev(tn, cfg)
			//: only the node that installs is refused, once.
			if tn == lag {
				cfg.Machine = refusedOnce{StateMachine: cfg.Machine, refused: &refused, said: said}
			}
		}
		end := stopBehind(c, lag)
		var tok coreconsensus.TokenValue
		//: entries enough for a cut past the stopped node's end.
		for i := range 70 {
			tok = proposeOnLeader(t, c, fmt.Sprintf("r%d=%d", i, i))
		}
		compactedPast(t, c, end+1)
		c.net.Isolate(lag.id, false)
		c.start(lag)
		n := lag.node
		//: the node catches up by an install, whose first restore is refused.
		select {
		case <-said:
		case <-time.After(10 * time.Second):
			t.Fatalf("the node stopped behind index %d never restored a snapshot", end)
		}
		// The refusal stops the node (#1582): the hold goes with the
		// restore, and the published position is the applied one.
		for deadline := time.Now().Add(10 * time.Second); !n.stopped.Load(); time.Sleep(time.Millisecond) {
			//: not in time.
			if time.Now().After(deadline) {
				t.Fatal("a refused restore did not stop the node")
			}
		}
		// The restore answers after the stop: its hold is let go once it is.
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
			pub := n.Status().Applied
			at, holds := appliedAndHolds(n)
			//: no hold left, published as applied.
			if holds == 0 && pub == at {
				break
			}
			//: a hold left behind keeps the published position below the applied one.
			if time.Now().After(deadline) {
				t.Fatalf("Status().Applied is %d, applied %d, %d hold(s) left, after a refused restore", pub, at, holds)
			}
		}
		// Started again, as a supervisor does, the node is installed.
		c.kill(lag)
		c.start(lag)
		c.waitApplied(tok.Index, 10*time.Second)
	}
	for _, tt := range []tc{{name: "a restore the machine refuses"}} {
		t.Run(tt.name, func(t *testing.T) { runCase(t, tt) })
	}
}
