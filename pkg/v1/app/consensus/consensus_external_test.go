package consensus_test

import (
	"context"
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

func (c *counter) Apply(e consensus.Entry) any {
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

func (c *counter) Restore(r io.Reader, _ consensus.SnapshotMeta) error {
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
func TestFacadeThreeNodesInMemory(t *testing.T) {
	net := consensus.NewMemoryNetwork()
	cluster := consensus.ClusterID{9}
	type nodeParts struct {
		log   *consensus.MemoryLog
		snaps *consensus.MemorySnapshots
		id    consensus.NodeID
	}
	var parts []nodeParts
	var members []consensus.Member
	for i := range 3 {
		p := nodeParts{log: consensus.NewMemoryLog(), snaps: consensus.NewMemorySnapshots()}
		slot := fmt.Sprintf("s%d", i)
		id, inc, err := consensus.InitLog(p.log, cluster, slot)
		if err != nil {
			t.Fatal(err)
		}
		p.id = id
		parts = append(parts, p)
		members = append(members, consensus.Member{ID: id, Incarnation: inc, Slot: slot, SPKI: consensus.MemorySPKI(id), Voter: true})
	}
	timing := consensus.TimingContract{
		HeartbeatInterval:  10 * time.Millisecond,
		ElectionTimeoutMin: 100 * time.Millisecond, ElectionTimeoutMax: 200 * time.Millisecond,
	}
	var nodes []*consensus.Node
	for _, p := range parts {
		cfg := consensus.Config{
			Cluster: cluster, Machine: &counter{}, Durability: consensus.DurabilityVolatile,
			Log: p.log, Snapshots: p.snaps, Transport: net.Transport(p.id), InitialTiming: timing,
			MaxSnapshotBytes: 1 << 30, MaxLogBytes: 1 << 30, MinVoters: 3,
		}
		if err := consensus.Bootstrap(context.Background(), cfg, members); err != nil {
			t.Fatal(err)
		}
		n, err := consensus.NewRaft(cfg)
		if err != nil {
			t.Fatal(err)
		}
		closeAtCleanup(t, n)
		nodes = append(nodes, n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var sum int
	var err error
	for i := 0; ; i++ {
		_, sum, err = consensus.ProposeAs[int](ctx, nodes[i%3], []byte("abc"))
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sum != 3 {
		t.Fatalf("sum %d", sum)
	}
	// Barrier is the contract: its token is the applied index once it
	// returns. Status is the node's view after its LAST TURN, published by
	// the loop while entries are applied on another goroutine, so it may
	// trail the Barrier by one turn and is polled rather than read once.
	tok, err := nodes[0].Barrier(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Index < 3 {
		t.Fatalf("barrier token %+v", tok)
	}
	for {
		st := nodes[0].Status()
		if st.Leader != 0 && st.Applied >= 3 {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("status %+v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
