package consensus

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// hangLog is a memory log whose Sync can be held: a frozen volume whose
// fdatasync neither returns nor fails.
type hangLog struct {
	*MemoryLog
	mu   sync.Mutex
	gate chan struct{}
}

func (l *hangLog) Sync() error {
	l.mu.Lock()
	g := l.gate
	l.mu.Unlock()
	if g != nil {
		<-g
	}
	return l.MemoryLog.Sync()
}

// hold makes every Sync from now on wait until the returned release runs.
func (l *hangLog) hold() (release func()) {
	g := make(chan struct{})
	l.mu.Lock()
	l.gate = g
	l.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.gate = nil
			l.mu.Unlock()
			close(g)
		})
	}
}

// #554: a leader whose storage or machine makes no progress hands its
// leadership to a voter that does, within a few election timeouts, and
// writes entering elsewhere succeed. Its event loop kept heartbeating, so
// CheckQuorum held and the followers never campaigned, while every write
// and read entering at it waited for a local sync or apply that never
// came, until an operator restarted it.
func TestALeaderThatMakesNoProgressStepsAside(t *testing.T) {
	cases := []struct {
		name string
		hang func(tn *testNode, l *hangLog) (release func())
	}{
		{"fdatasync hangs", func(_ *testNode, l *hangLog) func() { return l.hold() }},
		{"apply hangs", func(tn *testNode, _ *hangLog) func() {
			tn.sm.mu.Lock()
			var once sync.Once
			return func() { once.Do(tn.sm.mu.Unlock) }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := map[*testNode]*hangLog{}
			var mu sync.Mutex
			c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 3}, size: 3}
			c.configure = func(tn *testNode, cfg *Config) {
				mu.Lock()
				defer mu.Unlock()
				hl := logs[tn]
				if hl == nil {
					hl = &hangLog{MemoryLog: tn.log}
					logs[tn] = hl
				}
				cfg.Log = hl
			}
			newClusterWith(t, c)
			l := c.settledLeader(5 * time.Second)
			propose(t, l.node, "a=1")
			mu.Lock()
			hl := logs[l]
			mu.Unlock()
			release := tc.hang(l, hl)
			defer release()
			// A write entering at the stuck leader puts something to make
			// progress on.
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_, _, _ = l.node.Propose(ctx, []byte("stuck=1"))
			}()
			within := 4*testTimingFast().ElectionTimeoutMax + time.Second
			deadline := time.Now().Add(within)
			var other *testNode
			for time.Now().Before(deadline) && other == nil {
				for _, tn := range c.nodes {
					if tn != l && tn.node.Status().Role == coreconsensus.RoleLeader {
						other = tn
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if other == nil {
				t.Fatalf("the stuck leader is still the only leader after %v: %+v", within, l.node.Status())
			}
			for i := range 5 {
				propose(t, other.node, fmt.Sprintf("b%d=2", i))
			}
		})
	}
}
