package consensus

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// founderTiming is a valid InitialTiming whose election window grows with
// k: each passes Bootstrap's and NewNode's checks on its own, which is all
// Bootstrap checks, and entry 1 carries it (#698).
func founderTiming(k int) coreconsensus.TimingContractSpec {
	low := time.Duration(100+50*k) * time.Millisecond
	return coreconsensus.TimingContractSpec{
		Generation: 1, HeartbeatInterval: 10 * time.Millisecond,
		ElectionTimeoutMin: low, ElectionTimeoutMax: 2 * low, MaxClockDrift: 0.05,
	}
}

// founders are three founders of one member list, founder k's Warn lines
// kept in logs[k] and its series in meters[k].
type founders struct {
	*cluster
	logs   []*attrLogger
	meters []coremetrics.FullMeter
}

// foundersOf bootstraps and starts three founders, the identical member list
// on each, founder k with timing(k).
func foundersOf(t *testing.T, cid byte, timing func(k int) coreconsensus.TimingContractSpec) founders {
	t.Helper()
	f := founders{
		logs:   []*attrLogger{{}, {}, {}},
		meters: []coremetrics.FullMeter{svcmetrics.NewMeter(), svcmetrics.NewMeter(), svcmetrics.NewMeter()},
	}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{0x69, 0x8a, cid}, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		k := slices.Index(c.nodes, tn)
		cfg.InitialTiming, cfg.Logger, cfg.Meter = timing(k), f.logs[k], f.meters[k]
	}
	f.cluster = newClusterWith(t, c)
	return f
}

// refusalLine returns the Warn line of l naming a BOOTSTRAP_REFUSED, nil
// when there is none.
func refusalLine(l *attrLogger) map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line["error"], "BOOTSTRAP_REFUSED") {
			return line
		}
	}
	return nil
}

// keptLines returns a copy of the Warn lines l kept.
func keptLines(l *attrLogger) []map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.lines)
}

// leaderAmong returns the node of ns that leads now, nil for none.
func leaderAmong(ns []*testNode) *testNode {
	for _, tn := range ns {
		if tn.node != nil && tn.node.Status().Role == coreconsensus.RoleLeader {
			return tn
		}
	}
	return nil
}

// TestAFounderOfAnotherTimingIsRefusedAndSaysWhy is #698's scenario: three
// founders pass Bootstrap the identical list, founder 0 one timing and
// founders 1 and 2 another, so founder 0's committed entry 1 differs from
// theirs at (1, 1). Founder 0 refused every append of their leader for a
// broken chain, for as long as the cluster lived, and said nothing but
// chain refusals: the cluster ran with no failure tolerance, silently. It
// now refuses that leader for good, and says BOOTSTRAP_REFUSED in its log
// and its health, while founders 1 and 2 commit without it.
func TestAFounderOfAnotherTimingIsRefusedAndSaysWhy(t *testing.T) {
	c := foundersOf(t, 1, func(k int) coreconsensus.TimingContractSpec { return founderTiming(min(k, 1)) })
	logs, odd, rest := c.logs, c.nodes[0], c.nodes[1:]
	deadline := time.Now().Add(10 * time.Second)
	writes := 0
	for refusalLine(logs[0]) == nil || writes < 5 {
		if time.Now().After(deadline) {
			t.Fatalf("founder 0 said nothing in 10 s (log %v); founders 1 and 2 committed %d writes; status %+v",
				keptLines(logs[0]), writes, odd.node.Status())
		}
		if l := leaderAmong(rest); l != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			if _, _, err := l.node.Propose(ctx, fmt.Appendf(nil, "k%d=v", writes)); err == nil {
				writes++
			}
			cancel()
		}
		time.Sleep(10 * time.Millisecond)
	}
	line := refusalLine(logs[0])
	member := line["member"]
	if member != fmt.Sprint(uint64(rest[0].id)) && member != fmt.Sprint(uint64(rest[1].id)) {
		t.Errorf("the refusal names member %q, want founder 1 or 2: %v", member, line)
	}
	if line["index"] != "1" || line["term"] != "1" {
		t.Errorf("the refusal names (%s, %s), want the founding entry (1, 1): %v", line["index"], line["term"], line)
	}
	// Founder 0's log is its own history alone, its no-ops if it led once:
	// no entry of the other is spliced after its entry 1.
	first, last := odd.log.Bounds()
	chain := uint32(0)
	for _, e := range must(odd.log.Entries(first, last+1, 1<<20, nil))(t) {
		if wire.ChainSum(chain, &e) != e.ChainSum {
			t.Errorf("founder 0's entry %v does not chain from its log: two histories in one log", e.Position)
		}
		chain = e.ChainSum
	}
	h := odd.node.Health()
	if !h.Flags.Has(HealthHistoryRefused) || len(h.HistoryRefused) == 0 || h.Flags.Has(HealthReady) {
		t.Errorf("founder 0's health: flags %08b, refused %v: want HealthHistoryRefused, a member named, not ready", h.Flags, h.HistoryRefused)
	}
	for _, id := range h.HistoryRefused {
		if id != rest[0].id && id != rest[1].id {
			t.Errorf("founder 0 refuses %d, not a founder of the other history", id)
		}
	}
	if _, ok := c.meters[0].Collect().Sums[mHistoryRefusals]; !ok {
		t.Errorf("founder 0 never counted %s", mHistoryRefusals)
	}
	for _, tn := range rest {
		for _, id := range tn.node.Health().HistoryRefused {
			if id != odd.id {
				t.Errorf("founder %d refuses %d, a founder of its own history", tn.id, id)
			}
		}
	}
}

// TestFoundersOfOneTimingTruncateWithoutARefusal is the control: founders
// of one timing, and a leader cut off with an entry it appended alone. The
// majority elects another leader and commits past it; once back, the old
// leader truncates that entry, an ordinary repair of an entry of an older
// term, and no node refuses another.
func TestFoundersOfOneTimingTruncateWithoutARefusal(t *testing.T) {
	c := foundersOf(t, 2, func(int) coreconsensus.TimingContractSpec { return founderTiming(0) })
	old := c.settledLeader(5 * time.Second)
	_, before := old.log.Bounds()
	c.net.Isolate(old.id, true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _, _ = old.node.Propose(ctx, []byte("lost=1"))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for _, last := old.log.Bounds(); last == before; _, last = old.log.Bounds() {
		if time.Now().After(deadline) {
			t.Fatal("setup: the cut-off leader never appended its entry")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var others []*testNode
	for _, tn := range c.nodes {
		if tn != old {
			others = append(others, tn)
		}
	}
	var tok coreconsensus.TokenValue
	for {
		if time.Now().After(deadline) {
			t.Fatal("setup: the majority never committed without the cut-off leader")
		}
		if l := leaderAmong(others); l != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			var err error
			tok, _, err = l.node.Propose(ctx, []byte("won=1"))
			cancel()
			if err == nil {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.net.Isolate(old.id, false)
	<-done
	c.waitApplied(tok.Index, 5*time.Second)
	if v, _ := old.sm.get("lost"); v != "" {
		t.Fatalf("the entry the cut-off leader appended alone was applied: lost=%q", v)
	}
	for k, tn := range c.nodes {
		if line := refusalLine(c.logs[k]); line != nil {
			t.Errorf("founder %d refused a member of its own history: %v", k, line)
		}
		if h := tn.node.Health(); h.Flags.Has(HealthHistoryRefused) || h.HistoryRefused != nil {
			t.Errorf("founder %d's health refuses %v", k, h.HistoryRefused)
		}
	}
}
