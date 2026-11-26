package consensus

import (
	"testing"

	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// The forwards the step function neither proposed nor answered are
// published by reason, the runs its fence marks as a gauge, and the answers
// its table forgot at its bound as a counter (#1482, the safety review of
// fix C, O4 and O2): a node whose fence refuses a forwarder is seen, the
// reason evicted naming the fence's last resort.
func TestTheForwardDropsArePublishedByReason(t *testing.T) {
	meter := svcmetrics.NewMeter()
	n := &Node{obs: newObs(&Config{Meter: meter, Group: 1})}
	type tc struct {
		name      string
		drops     raft.ForwardDropsValue
		runs      int
		forgotten uint64
	}
	runCase := func(t *testing.T, x tc) {
		t.Helper()
		n.observeForwards(&raft.SummaryValue{ForwardDrops: x.drops, ForwardRuns: x.runs, ForwardForgotten: x.forgotten})
		snap := meter.Collect()
		want := map[string]int64{"copy": int64(x.drops.Copies), "restarted": int64(x.drops.Restarted), "evicted": int64(x.drops.Evicted)}
		got := map[string]int64{}
		for _, p := range snap.Sums[mForwardsDropped].Points {
			for reason := range want {
				if hasAttr(p.Attrs, "reason", reason) && hasAttr(p.Attrs, "group", "1") {
					got[reason] = p.Value
				}
			}
		}
		for reason, v := range want {
			if got[reason] != v {
				t.Fatalf("%s: %s{reason=%q} is %d, want %d (%+v)", x.name, mForwardsDropped, reason, got[reason], v, snap.Sums[mForwardsDropped])
			}
		}
		if f := snap.Sums[mForwardForgotten]; len(f.Points) != 1 || f.Points[0].Value != int64(x.forgotten) || !hasAttr(f.Points[0].Attrs, "group", "1") {
			t.Fatalf("%s: %s is %+v, want %d", x.name, mForwardForgotten, f, x.forgotten)
		}
		g := snap.Gauges[mForwardRuns]
		if len(g.Points) != 1 || g.Points[0].Value != float64(x.runs) {
			t.Fatalf("%s: %s is %+v, want %d", x.name, mForwardRuns, g, x.runs)
		}
	}
	for _, x := range []tc{
		{"none yet", raft.ForwardDropsValue{}, 0, 0},
		{"a copy and a restart", raft.ForwardDropsValue{Copies: 1, Restarted: 2}, 3, 0},
		{"then an eviction and answers forgotten at the bound", raft.ForwardDropsValue{Copies: 1, Restarted: 2, Evicted: 4}, 4096, 5},
	} {
		runCase(t, x)
	}
}
