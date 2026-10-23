package consensus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// slowCutKV is kv whose snapshot takes cutTime to write: a large state cut
// by a machine that pauses apply to cut.
type slowCutKV struct {
	*kv
	cutTime time.Duration
}

type slowSnap struct {
	coreconsensus.Snapshot
	d time.Duration
}

func (s slowSnap) WriteTo(w io.Writer) (int64, error) {
	time.Sleep(s.d)
	return s.Snapshot.WriteTo(w)
}

func (s *slowCutKV) Snapshot() (coreconsensus.Snapshot, error) {
	snap, err := s.kv.Snapshot()
	return slowSnap{Snapshot: snap, d: s.cutTime}, err
}

func runPausedCuts(t *testing.T, pauses bool) (changes int64, refused int64) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{7, 7}, every: 30, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		cfg.Machine = &slowCutKV{kv: tn.sm, cutTime: 500 * time.Millisecond}
		cfg.SnapshotPausesApply = pauses
	}
	newClusterWith(t, c)
	c.settledLeader(5 * time.Second)
	var leaders atomic.Int64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var last coreconsensus.NodeID
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			for _, tn := range c.nodes {
				if st := tn.node.Status(); st.Role == coreconsensus.RoleLeader && st.Leader != last {
					if last != 0 {
						leaders.Add(1)
					}
					last = st.Leader
				}
			}
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		for _, tn := range c.nodes {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, _, err := tn.node.Propose(ctx, []byte(fmt.Sprintf("k%d=v", i)))
			cancel()
			if err == nil {
				break
			}
			if errorsIsNotLeader(err) {
				refused++
			}
		}
	}
	close(stop)
	<-done
	return leaders.Load(), refused
}

func errorsIsNotLeader(err error) bool {
	return err != nil && errors.Is(err, coreconsensus.NotLeader)
}

// A cut that pauses apply longer than the maximum election timeout was
// taken for a stuck apply: the leader handed over, its successor cut at the
// same threshold and handed over in turn, one leader change per cut for as
// long as writes came (#593). The time the apply goroutine spends cutting
// is no longer counted.
func TestACutThatPausesApplyDoesNotHandLeadershipOver(t *testing.T) {
	base, _ := runPausedCuts(t, false)
	paused, refused := runPausedCuts(t, true)
	t.Logf("leader changes in 2 s of writes: cuts off the apply goroutine %d, cuts pausing apply (500 ms > ElectionTimeoutMax 200 ms) %d, NOT_LEADER answers %d", base, paused, refused)
	if paused > base {
		t.Errorf("every cut that pauses apply longer than ElectionTimeoutMax hands leadership over: %d changes against %d", paused, base)
	}
}
