package machine_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	svcconsensus "github.com/kitsunium/sdk/internal/service/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/machine"
)

// The linearizability checker of ADR 0152 D15, the one the raft simulator
// runs (raft's lincheck_internal_test.go), here over the KV reference
// machine on the memory transport: Wing & Gong's search with Lowe's
// memoization, partitioned per key, over a register model. A test file
// cannot be imported, so the checker is repeated here, and its negative
// control with it: a checker never seen failing is not one.

// kvOp is one client operation: a write of val, or a read that returned
// val. ret is math.MaxInt64 for an operation whose outcome is unknown.
type kvOp struct {
	key   string
	write bool
	val   string
	call  int64
	ret   int64
}

// linearizable reports whether the history has a linearization; on
// failure it names the key whose history has none.
func linearizable(ops []kvOp) (bool, string) {
	byKey := map[string][]kvOp{}
	for _, o := range ops {
		byKey[o.key] = append(byKey[o.key], o)
	}
	for _, k := range slices.Sorted(maps.Keys(byKey)) {
		if !linearizableKey(byKey[k]) {
			return false, k
		}
	}
	return true, ""
}

// linearizableKey searches one key's history. Unknown-outcome writes may
// be placed anywhere after their call, or left out.
func linearizableKey(ops []kvOp) bool {
	slices.SortFunc(ops, func(a, b kvOp) int { return int(min(max(a.call-b.call, -1), 1)) })
	done := make([]bool, len(ops))
	required := 0
	for _, o := range ops {
		if o.ret != math.MaxInt64 {
			required++
		}
	}
	memo := map[string]bool{}
	var search func(state string, left int) bool
	search = func(state string, left int) bool {
		if left == 0 {
			return true
		}
		var key bytes.Buffer
		for _, d := range done {
			key.WriteByte(map[bool]byte{true: '1', false: '0'}[d])
		}
		key.WriteString("|" + state)
		k := key.String()
		if v, ok := memo[k]; ok {
			return v
		}
		minRet := int64(math.MaxInt64)
		for i, o := range ops {
			if !done[i] && o.ret < minRet {
				minRet = o.ret
			}
		}
		for i, o := range ops {
			if done[i] || o.call > minRet {
				continue
			}
			next := state
			if o.write {
				next = o.val
			} else if o.val != state {
				continue
			}
			done[i] = true
			l := left
			if o.ret != math.MaxInt64 {
				l--
			}
			ok := search(next, l)
			done[i] = false
			if ok {
				memo[k] = true
				return true
			}
		}
		memo[k] = false
		return false
	}
	return search("", required)
}

func TestLinearizabilityCheckerOnKnownHistories(t *testing.T) {
	const unknown int64 = math.MaxInt64
	cases := []struct {
		name string
		ops  []kvOp
		want bool
	}{
		{"sequential write then read", []kvOp{{key: "x", write: true, val: "1", call: 0, ret: 1}, {key: "x", val: "1", call: 2, ret: 3}}, true},
		{"stale read after a completed write", []kvOp{
			{key: "x", write: true, val: "1", call: 0, ret: 1},
			{key: "x", write: true, val: "2", call: 2, ret: 3},
			{key: "x", val: "1", call: 4, ret: 5},
		}, false},
		{"read concurrent with a write sees either", []kvOp{
			{key: "x", write: true, val: "1", call: 0, ret: 1},
			{key: "x", write: true, val: "2", call: 2, ret: 6},
			{key: "x", val: "1", call: 3, ret: 4},
		}, true},
		{"read of a value never written", []kvOp{{key: "x", val: "9", call: 0, ret: 1}}, false},
		{"lost update: two reads disagree on the order", []kvOp{
			{key: "x", write: true, val: "1", call: 0, ret: 10},
			{key: "x", write: true, val: "2", call: 0, ret: 10},
			{key: "x", val: "1", call: 11, ret: 12},
			{key: "x", val: "2", call: 13, ret: 14},
		}, false},
		{"unknown write may take effect", []kvOp{
			{key: "x", write: true, val: "1", call: 0, ret: unknown},
			{key: "x", val: "1", call: 5, ret: 6},
		}, true},
		{"unknown write cannot flip back", []kvOp{
			{key: "x", write: true, val: "1", call: 0, ret: unknown},
			{key: "x", val: "1", call: 5, ret: 6},
			{key: "x", val: "", call: 7, ret: 8},
		}, false},
	}
	for _, c := range cases {
		if got, _ := linearizable(c.ops); got != c.want {
			t.Errorf("%s: linearizable %v, want %v", c.name, got, c.want)
		}
	}
}

// history records operations against one logical clock: an operation's
// call and return are ticks of it, so real-time order is the ticks' order.
type history struct {
	clock atomic.Int64
	mu    sync.Mutex
	ops   []kvOp
}

func (h *history) add(o kvOp) {
	h.mu.Lock()
	h.ops = append(h.ops, o)
	h.mu.Unlock()
}

// kvClient runs n operations on its own keys' share: a put of a value no
// one else writes, then a read through the log, through whichever node
// leads. A NOT_LEADER was never appended and is left out; any other
// failure of a write may have committed, and is recorded as unknown.
func kvClient(c *testCluster3, h *history, client, n int) {
	for k := range n {
		key := []string{"a", "b"}[k%2]
		val := fmt.Sprintf("c%d-%d", client, k)
		for _, write := range []bool{true, false} {
			cmd := machine.KVGet(key)
			if write {
				cmd = machine.KVPut(key, []byte(val))
			}
			call := h.clock.Add(1)
			_, n := c.leader()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, r, err := n.Propose(ctx, cmd)
			cancel()
			ret := h.clock.Add(1)
			switch {
			case err == nil && write:
				h.add(kvOp{key: key, write: true, val: val, call: call, ret: ret})
			case err == nil:
				h.add(kvOp{key: key, val: string(r.(machine.KVResult).Value), call: call, ret: ret})
			case write && !errors.Is(err, coreconsensus.NotLeader):
				h.add(kvOp{key: key, write: true, val: val, call: call, ret: math.MaxInt64})
			}
		}
	}
}

func TestTheKVMachineIsLinearizableOverTheMemoryTransport(t *testing.T) {
	c := newTestCluster(t, svcconsensus.DurabilityVolatile, func(int) coreconsensus.StateMachine { return machine.NewKV() })
	h := &history{}
	var wg sync.WaitGroup
	for client := range 4 {
		wg.Go(func() { kvClient(c, h, client, 12) })
	}
	// The leader crashes while the clients run, and comes back on its log.
	time.Sleep(150 * time.Millisecond)
	li, _ := c.leader()
	c.kill(li)
	time.Sleep(300 * time.Millisecond)
	c.start(li)
	wg.Wait()
	ops := slices.Clone(h.ops)
	reads := 0
	for _, o := range ops {
		if !o.write {
			reads++
		}
	}
	if reads < 20 {
		t.Fatalf("only %d reads completed: the history checks nothing", reads)
	}
	if ok, key := linearizable(ops); !ok {
		t.Fatalf("the history of %d operations has no linearization on key %q", len(ops), key)
	}
	// The negative control on this very history: a read that returns the
	// initial value after a write to its key completed has none.
	for i, o := range ops {
		if o.write || !slices.ContainsFunc(ops, func(w kvOp) bool { return w.write && w.key == o.key && w.ret < o.call }) {
			continue
		}
		stale := slices.Clone(ops)
		stale[i].val = ""
		if ok, _ := linearizable(stale); ok {
			t.Fatalf("the checker accepted a read of the initial value on %q after a write to it completed", o.key)
		}
		return
	}
	t.Fatal("no read followed a completed write: the negative control found nothing to mutate")
}
