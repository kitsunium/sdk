package raft

import (
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// fenceMsg is a forward of node from's run epoch, counter, at term.
func fenceMsg(from core.NodeID, epoch, counter uint32, term uint64) core.MessageValue {
	m := msg(core.MsgForward, from, 1, term)
	m.Context = uint64(epoch)<<32 | uint64(counter)
	return m
}

// The fence keeps a mark per run. Past soft marks it forgets those of a
// term below the current one, the oldest term first and, within a term,
// the run handled least recently, whatever its node; each mark forgotten
// raises its node's fence to its term, so that run's copies stay refused
// while a run of a later term is admitted. A mark of the current term is
// never forgotten below hard marks. The cases run in order, on one fence,
// soft 3 and hard 8.
func TestTheFenceForgetsOlderTermsFirstAndTheLeastRecent(t *testing.T) {
	const soft, hard = 3, 8
	var f fwdFence
	f.start = 1
	type tc struct {
		name    string
		note    bool
		m       core.MessageValue
		current uint64
		admit   bool
	}
	runCase := func(t *testing.T, x tc) {
		t.Helper()
		if got := f.admits(&x.m); got != x.admit {
			t.Fatalf("%s: admitted %v, want %v (%d marks, node fences %v)", x.name, got, x.admit, len(f.marks), f.nodes)
		}
		if x.note {
			f.note(&x.m, soft, hard, x.current)
		}
	}
	for _, x := range []tc{
		{"node 9's run, term 2", true, fenceMsg(9, 1, 1, 2), 2, true},
		{"node 2's run, term 2", true, fenceMsg(2, 1, 1, 2), 2, true},
		{"node 3's run, term 3", true, fenceMsg(3, 1, 1, 3), 3, true},
		{"node 2's run handled again, term 2 still", true, fenceMsg(2, 1, 2, 2), 3, true},
		{"node 4's run, term 3: soft reached, node 9's term-2 mark, the least recent, goes", true, fenceMsg(4, 1, 1, 3), 3, true},
		{"node 9's run's copy, its mark gone", false, fenceMsg(9, 1, 1, 2), 3, false},
		{"node 9's run's next forward, of term 2, refused", false, fenceMsg(9, 1, 2, 2), 3, false},
		{"node 9's run, of term 3: admitted", false, fenceMsg(9, 1, 2, 3), 3, true},
		{"node 2's run, kept, though its node is the lowest", false, fenceMsg(2, 1, 3, 2), 3, true},
		{"a node never evicted, of a term at the start: refused", false, fenceMsg(5, 1, 1, 1), 3, false},
		{"a node never evicted, of term 2: admitted", false, fenceMsg(5, 1, 1, 2), 3, true},
	} {
		runCase(t, x)
	}
	if k := f.nodes[9]; k != 2 || f.nodes[2] != 0 || len(f.marks) != soft {
		t.Fatalf("node fences %v and %d marks, want node 9 at 2, node 2 untouched, %d marks", f.nodes, len(f.marks), soft)
	}
}

// A mark of the current term is kept past soft marks, so that no node's
// fence rises to the current term (#1482, the safety review of its fix C,
// D1). Past hard marks, the last resort, the run handled least recently of
// the node holding the most marks goes, and only that node's next runs wait
// for the next term: a live run of another node is served throughout, and
// so are its new runs.
func TestTheFenceLastResortFencesOnlyTheCrowdedNode(t *testing.T) {
	const soft, hard = 4, 8
	var f fwdFence
	f.start = 1
	live := fenceMsg(2, 0xA2, 1, 2)
	f.note(&live, soft, hard, 2)
	for e := uint32(1); e <= 7; e++ {
		m := fenceMsg(3, e, 1, 2)
		f.note(&m, soft, hard, 2)
	}
	if len(f.marks) != 8 || len(f.nodes) != 0 {
		t.Fatalf("past soft, every mark of the current term kept: %d marks, node fences %v, want 8 and none", len(f.marks), f.nodes)
	}
	next := fenceMsg(3, 8, 1, 2)
	f.note(&next, soft, hard, 2)
	type tc struct {
		name  string
		m     core.MessageValue
		admit bool
	}
	runCase := func(t *testing.T, x tc) {
		t.Helper()
		if got := f.admits(&x.m); got != x.admit {
			t.Fatalf("%s: admitted %v, want %v (%d marks, node fences %v)", x.name, got, x.admit, len(f.marks), f.nodes)
		}
	}
	for _, x := range []tc{
		{"node 3's oldest run, gone, its copy", fenceMsg(3, 1, 1, 2), false},
		{"node 3's newest run, kept", fenceMsg(3, 8, 2, 2), true},
		{"node 3's run handled last before the eviction, kept", fenceMsg(3, 7, 2, 2), true},
		{"a new run of node 3, of the current term", fenceMsg(3, 9, 1, 2), false},
		{"a new run of node 3, of the next term", fenceMsg(3, 9, 1, 3), true},
		{"node 2's live run", fenceMsg(2, 0xA2, 2, 2), true},
		{"a new run of node 2, of the current term", fenceMsg(2, 0xB2, 1, 2), true},
	} {
		runCase(t, x)
	}
	if f.nodes[3] != 2 || f.nodes[2] != 0 || len(f.marks) != 8 {
		t.Fatalf("node fences %v and %d marks, want node 3 at 2 alone and 8 marks", f.nodes, len(f.marks))
	}
}

// A rebuild of the table's map gives the room back a step at a time and
// keeps every answer the window still covers, through the rebuild and
// after it; the old map goes once its last answer is moved. On the first
// fix a rebuild ran in one turn (23.5 ms for 75 000 answers, review O4), and
// a mutation of it that copied the wrong region lost answers no test saw
// (review M1). The answers past the window go a step at a time too,
// fwdExpireStep a call at most.
func TestARebuildKeepsEveryLiveAnswerAStepAtATime(t *testing.T) {
	type tc struct {
		name       string
		old, young int
		rebuilds   bool
	}
	const window = 10 * time.Second
	runCase := func(t *testing.T, x tc) {
		t.Helper()
		tab := new(fwdTable)
		now := t0
		tab.advance(now, fwdClockStep)
		for i := range x.old {
			tab.remember(fwdKey{from: 2, ctx: uint64(i)}, fwdAnswer{index: uint64(i)})
		}
		for range 6 {
			now = now.Add(time.Second)
			tab.advance(now, fwdClockStep)
		}
		for i := x.old; i < x.old+x.young; i++ {
			tab.remember(fwdKey{from: 2, ctx: uint64(i)}, fwdAnswer{index: uint64(i)})
		}
		for range 4 {
			now = now.Add(time.Second)
			tab.advance(now, fwdClockStep)
		}
		keepsYoung := func(when string) {
			for i := x.old; i < x.old+x.young; i++ {
				if a, ok := tab.lookup(fwdKey{from: 2, ctx: uint64(i)}); !ok || a.index != uint64(i) {
					t.Fatalf("%s: answer %d, 4 s old, lost %s (%+v, %v)", x.name, i, when, a, ok)
				}
			}
		}
		// The first calls forget the old answers, fwdExpireStep each; the
		// driver's Tick ages at most a second's worth (fwdClockStep). The
		// calls after them only move the map.
		expiredLeft := func() bool {
			return tab.size > 0 && tab.clock-tab.blocks[0][tab.head].at >= window
		}
		var first, worst time.Duration
		steps, rebuilt := 0, false
		for {
			before, kept := len(tab.next), tab.kept()
			start := time.Now()
			tab.expire(window)
			if gone := kept - tab.kept(); gone > fwdExpireStep {
				t.Fatalf("%s: one call forgot %d answers, more than a step of %d", x.name, gone, fwdExpireStep)
			}
			if d := time.Since(start); steps == 0 {
				first = d
			} else {
				worst = max(worst, d)
			}
			if moved := len(tab.next) - before; moved > fwdRebuildStep {
				t.Fatalf("%s: one call moved %d answers, more than a step of %d", x.name, moved, fwdRebuildStep)
			}
			keepsYoung(fmt.Sprintf("after call %d", steps+1))
			rebuilt = rebuilt || tab.next != nil
			if tab.next == nil && !expiredLeft() {
				break
			}
			steps++
			if steps > x.young/fwdRebuildStep+2+x.old/fwdExpireStep {
				t.Fatalf("%s: the rebuild still runs after %d calls", x.name, steps)
			}
		}
		if rebuilt != x.rebuilds || tab.kept() != x.young || len(tab.byKey) != x.young {
			t.Fatalf("%s: %d calls, a rebuild %v, %d kept, %d in the map; want a rebuild %v and %d", x.name, steps, rebuilt, tab.kept(), len(tab.byKey), x.rebuilds, x.young)
		}
		t.Logf("%s: the expiry and the rebuild took %d calls after the first (%v), the longest %v", x.name, steps, first, worst)
	}
	for _, x := range []tc{
		{"the review's table, too small to rebuild", 1000, 200, false},
		{"a quarter left of a large table", 4 * minFwdRebuild, 3000, true},
		{"the review's drain, 75 000 left of 2^18", 1<<18 - 75000, 75000, false},
		{"a drain to a quarter of 2^18", 3 << 16, 1 << 16, true},
	} {
		runCase(t, x)
	}
}
