package consensus_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/app/consensus"
)

// counter is the smallest StateMachine: it adds every command's length.
type counter struct {
	mu  sync.Mutex
	sum int
}

func (c *counter) Apply(e consensus.EntryValue) any {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sum += len(e.Data)
	return c.sum
}

type counterSnap struct{ sum int }

func (s counterSnap) WriteTo(w io.Writer) (int64, error) {
	n, err := fmt.Fprintf(w, "%d", s.sum)
	return int64(n), err
}
func (counterSnap) Release() {}

func (c *counter) Snapshot() (consensus.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return counterSnap{c.sum}, nil
}

func (c *counter) Restore(r io.Reader, _ consensus.SnapshotMetaValue) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := fmt.Fscan(r, &c.sum)
	return err
}

// closer is the one capability teardown needs from a node: a graceful
// Close bounded by its context.
type closer interface {
	Close(ctx context.Context) error
}

// closeAtCleanup stops n when the test ends and reports a Close that failed:
// a node that cannot shut down cleanly is a defect the test should surface,
// not one its teardown should hide.
func closeAtCleanup(t *testing.T, n closer) {
	t.Helper()
	t.Cleanup(func() {
		if err := n.Close(context.Background()); err != nil {
			t.Errorf("close node: %v", err)
		}
	})
}

// TestFacadeThreeNodesInMemory runs three nodes through the facade alone.
// A Barrier on node 0 follows a write the cluster committed: node 0 may not
// know the leader yet, the write needing only the two others, and its read
// is then NOT_LEADER, which NotLeader documents as safe to retry. The second
// case holds node 0 out of the election until the write commits, the order a
// loaded runner produced (a Barrier asked before any frame of the leader
// reached it), so the retry is exercised on every run.
func TestFacadeThreeNodesInMemory(t *testing.T) {
	type tc struct {
		name string
		// isolated keeps node 0 cut from the others until the write commits.
		isolated bool
	}
	cases := []tc{
		{name: "every node takes part in the election"},
		{name: "the barrier node hears no leader before the write commits", isolated: true},
	}
	runCase := func(t *testing.T, c tc) {
		net := consensus.NewMemoryNetwork()
		cluster := consensus.ClusterID{9}
		type nodeParts struct {
			log   *consensus.MemoryLog
			snaps *consensus.MemorySnapshots
			tr    *consensus.MemoryTransport
			id    consensus.NodeID
		}
		var parts []nodeParts
		var members []consensus.MemberValue
		for i := range 3 {
			p := nodeParts{log: consensus.NewMemoryLog(), snaps: consensus.NewMemorySnapshots()}
			slot := fmt.Sprintf("s%d", i)
			id, inc, err := consensus.InitLog(p.log, cluster, slot)
			//: a log that cannot be initialised builds no cluster.
			if err != nil {
				t.Fatal(err)
			}
			p.id = id
			p.tr = consensus.NewMemoryTransport(net, id)
			parts = append(parts, p)
			members = append(members, consensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: consensus.MemorySPKI(id), Voter: true})
		}
		//: node 0 is cut before any node runs, so it hears no frame of the election.
		if c.isolated {
			net.Isolate(parts[0].id, true)
		}
		timing := consensus.TimingContractSpec{
			HeartbeatInterval:  10 * time.Millisecond,
			ElectionTimeoutMin: 100 * time.Millisecond, ElectionTimeoutMax: 200 * time.Millisecond,
		}
		var nodes []*consensus.Node
		for _, p := range parts {
			cfg := consensus.Config{
				Cluster: cluster, Machine: &counter{}, Durability: consensus.DurabilityVolatile,
				Log: p.log, Snapshots: p.snaps, Transport: p.tr, InitialTiming: timing,
				MaxSnapshotBytes: 1 << 30, MaxLogBytes: 1 << 30, MinVoters: 3,
			}
			//: a node that cannot be bootstrapped builds no cluster.
			if err := consensus.Bootstrap(context.Background(), &cfg, members); err != nil {
				t.Fatal(err)
			}
			n, err := consensus.NewNode(cfg)
			//: a node that cannot be built builds no cluster.
			if err != nil {
				t.Fatal(err)
			}
			closeAtCleanup(t, n)
			nodes = append(nodes, n)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// The proposers are the nodes that can reach a majority: all three,
		// or the two others while node 0 is cut.
		proposers := nodes
		//: node 0 alone proposes nothing while it is cut.
		if c.isolated {
			proposers = nodes[1:]
		}
		var sum int
		var err error
		//: a proposal is retried until a leader is elected and takes it.
		for i := 0; ; i++ {
			_, sum, err = consensus.ProposeAs[int](ctx, proposers[i%len(proposers)], []byte("abc"))
			//: the write committed.
			if err == nil {
				break
			}
			//: no leader took it within the test's bound.
			if ctx.Err() != nil {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
		}
		//: the counter applied the one write.
		if sum != 3 {
			t.Fatalf("sum %d", sum)
		}
		//: node 0 joins the cluster again once the write is committed.
		if c.isolated {
			net.Isolate(parts[0].id, false)
		}
		// Barrier is the contract: its token is the applied index once it
		// returns. A node that does not know the leader yet refuses the read
		// with NOT_LEADER, safe to retry, as the proposals above are retried;
		// any other refusal fails the test. Status is the node's view after
		// its LAST TURN, published by the loop while entries are applied on
		// another goroutine, so it may trail the Barrier by one turn and is
		// polled rather than read once.
		var tok consensus.TokenValue
		//: a read is retried while node 0 does not know the leader.
		for {
			tok, err = nodes[0].Barrier(ctx)
			//: the read point is confirmed and applied.
			if err == nil {
				break
			}
			//: a refusal other than NOT_LEADER, or one past the bound, fails.
			if !errors.Is(err, consensus.NotLeader) || ctx.Err() != nil {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
		}
		//: the token covers the write committed before the Barrier.
		if tok.Index < 3 {
			t.Fatalf("barrier token %+v", tok)
		}
		//: Status is polled until it shows the leader and the write applied.
		for {
			st := nodes[0].Status()
			//: node 0's published view caught up with its Barrier.
			if st.Leader != 0 && st.Applied >= 3 {
				break
			}
			//: the view never caught up within the test's bound.
			if ctx.Err() != nil {
				t.Fatalf("status %+v", st)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	//: every case runs as its own subtest.
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// A consumer reads a health verdict by name through the facade: the flags
// are distinct bits, and Has answers for each.
func TestHealthFlagsReadByName(t *testing.T) {
	h := consensus.HealthReady | consensus.HealthHasLeader
	if !h.Has(consensus.HealthReady) || !h.Has(consensus.HealthHasLeader) || h.Has(consensus.HealthStopped) || h.Has(consensus.HealthQuorumReachable) {
		t.Fatalf("health flags: %b", h)
	}
	p := consensus.PeerVoter | consensus.PeerSnapshot
	if !p.Has(consensus.PeerVoter) || !p.Has(consensus.PeerSnapshot) || p.Has(consensus.PeerUnreachable) {
		t.Fatalf("peer flags: %b", p)
	}
	var v consensus.HealthValue
	v.Flags = consensus.HealthStopped
	if !v.Flags.Has(consensus.HealthStopped) {
		t.Fatal("a verdict's flags do not read through the facade")
	}
}
