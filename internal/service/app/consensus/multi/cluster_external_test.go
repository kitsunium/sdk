package multi_test

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/multi"
)

// kv is a register per key: a command "k=v" sets k and answers its previous
// value. It records every command in the order it applied them.
type kv struct {
	mu    sync.Mutex
	m     map[string]string
	order []string
}

func newKV() *kv { return &kv{m: map[string]string{}} }

func (s *kv) Apply(e coreconsensus.EntryValue) any {
	r := make([]any, 1)
	s.ApplyBatch([]coreconsensus.EntryValue{e}, r)
	return r[0]
}

func (s *kv) ApplyBatch(es []coreconsensus.EntryValue, results []any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range es {
		k, v, _ := bytes.Cut(e.Data, []byte("="))
		key := string(k)
		results[i] = s.m[key]
		s.m[key] = string(v)
		s.order = append(s.order, string(e.Data))
	}
}

func (s *kv) get(k string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[k]
}

func (s *kv) applied() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.order)
}

type kvSnap struct{}

func (kvSnap) WriteTo(io.Writer) (int64, error) { return 0, nil }
func (kvSnap) Release()                         {}

func (*kv) Snapshot() (coreconsensus.Snapshot, error)                { return kvSnap{}, nil }
func (*kv) Restore(io.Reader, coreconsensus.SnapshotMetaValue) error { return nil }

// memCluster is three voters over the memory network, each node's machine
// a Machine over its own kv when packed, the kv itself otherwise.
type memCluster struct {
	nodes []*consensus.Node
	kvs   []*kv
}

func newMemCluster(tb testing.TB, packed bool) *memCluster {
	tb.Helper()
	net := consensus.NewMemoryNetwork()
	cid := coreconsensus.ClusterID{9, 2, 6}
	timing := coreconsensus.TimingContractSpec{
		Generation: 1, HeartbeatInterval: 10 * time.Millisecond,
		ElectionTimeoutMin: 100 * time.Millisecond, ElectionTimeoutMax: 200 * time.Millisecond, MaxClockDrift: 0.05,
	}
	c := &memCluster{}
	var cfgs []consensus.Config
	var members []coreconsensus.MemberValue
	for i := range 3 {
		log := consensus.NewMemoryLog()
		slot := fmt.Sprintf("slot-%d", i)
		id, inc, err := consensus.InitLog(log, cid, slot)
		if err != nil {
			tb.Fatal(err)
		}
		k := newKV()
		var m coreconsensus.StateMachine = k
		if packed {
			m = multi.NewMachine(k)
		}
		c.kvs = append(c.kvs, k)
		cfgs = append(cfgs, consensus.Config{
			Cluster: cid, Machine: m, Durability: consensus.DurabilityVolatile, Log: log,
			Snapshots: consensus.NewMemorySnapshots(), Transport: consensus.NewMemoryTransport(net, id),
			InitialTiming: timing, MaxSnapshotBytes: 1 << 30, SnapshotEveryBytes: 1 << 30,
			MaxLogBytes: 1 << 34, LogReserveBytes: 1 << 20, MinVoters: 1,
		})
		members = append(members, coreconsensus.MemberValue{
			ID: id, Incarnation: inc, Slot: slot, SPKI: consensus.MemorySPKI(id), Voter: true,
		})
	}
	for i := range cfgs {
		if err := consensus.Bootstrap(context.Background(), &cfgs[i], members); err != nil {
			tb.Fatal(err)
		}
		n, err := consensus.NewNode(cfgs[i])
		if err != nil {
			tb.Fatal(err)
		}
		c.nodes = append(c.nodes, n)
	}
	tb.Cleanup(func() {
		for _, n := range c.nodes {
			_ = n.HardClose()
		}
	})
	return c
}

// leader waits for a leader and returns its index.
func (c *memCluster) leader(tb testing.TB) int {
	tb.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for i, n := range c.nodes {
			if n.Status().Role == coreconsensus.RoleLeader {
				return i
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	tb.Fatal("no leader")
	return -1
}

func TestAPackedEntryAppliesOnEveryNodeWithItsResultsInCallerOrder(t *testing.T) {
	c := newMemCluster(t, true)
	l := c.leader(t)
	p := multi.NewPacker(c.nodes[l], multi.Config{})
	defer closePacker(t, p)

	const callers, each = 40, 10
	type answer struct {
		cmd string
		res multi.ResultValue
	}
	var mu sync.Mutex
	var answers []answer
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			for j := range each {
				cmd := fmt.Sprintf("k%d=%d", i, j)
				res, err := p.Propose(context.Background(), []byte(cmd))
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				answers = append(answers, answer{cmd, res})
				mu.Unlock()
				// Each caller writes its own key: the result is its own
				// previous write.
				if want := map[bool]string{true: "", false: fmt.Sprint(j - 1)}[j == 0]; res.Value != want {
					t.Errorf("%s answered %v, want %q", cmd, res.Value, want)
				}
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	// The order each node applied is the order of the tokens and
	// sub-indexes the callers were answered with.
	slices.SortFunc(answers, func(a, b answer) int {
		return cmp.Or(cmp.Compare(a.res.Token.Index, b.res.Token.Index), cmp.Compare(a.res.Sub, b.res.Sub))
	})
	want := make([]string, len(answers))
	entries := map[uint64]bool{}
	for i, a := range answers {
		want[i] = a.cmd
		entries[a.res.Token.Index] = true
	}
	if _, err := c.nodes[l].Barrier(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i, k := range c.kvs {
		deadline := time.Now().Add(10 * time.Second)
		for len(k.applied()) < len(want) && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if got := k.applied(); !slices.Equal(got, want) {
			t.Fatalf("node %d applied %d commands, not in the answers' order", i, len(got))
		}
	}
	if len(entries) >= len(answers) {
		t.Fatalf("%d commands took %d entries: nothing was packed", len(answers), len(entries))
	}
	t.Logf("%d commands in %d entries", len(answers), len(entries))
}

func TestTheLinearizabilityClientOverAMultiMachinePasses(t *testing.T) {
	c := newMemCluster(t, true)
	l := c.leader(t)
	p := multi.NewPacker(c.nodes[l], multi.Config{})
	defer closePacker(t, p)

	start := time.Now()
	now := func() int64 { return int64(time.Since(start)) }
	const clients, each = 8, 40
	keys := []string{"a", "b", "c"}
	var mu sync.Mutex
	var ops []kvOp
	var wg sync.WaitGroup
	for i := range clients {
		wg.Go(func() {
			for j := range each {
				key := keys[(i+j)%len(keys)]
				if (i+j)%3 == 0 {
					call := now()
					if _, err := c.nodes[l].Barrier(context.Background()); err != nil {
						t.Error(err)
						return
					}
					v := c.kvs[l].get(key)
					mu.Lock()
					ops = append(ops, kvOp{key: key, val: v, call: call, ret: now()})
					mu.Unlock()
					continue
				}
				val := fmt.Sprintf("%d.%d", i, j)
				call := now()
				if _, err := p.Propose(context.Background(), []byte(key+"="+val)); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				ops = append(ops, kvOp{key: key, write: true, val: val, call: call, ret: now()})
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	if ok, key := linearizable(ops); !ok {
		t.Fatalf("the history of %d operations has no linearization on key %q", len(ops), key)
	}
}

// ---- a register checker, as raft's lincheck: Wing & Gong with Lowe's memo ----

// kvOp is a write of val, or a read that returned val, between call and ret.
type kvOp struct {
	key   string
	write bool
	val   string
	call  int64
	ret   int64
}

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

func linearizableKey(ops []kvOp) bool {
	slices.SortFunc(ops, func(a, b kvOp) int { return cmp.Compare(a.call, b.call) })
	done := make([]bool, len(ops))
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
		minRet := int64(1<<63 - 1)
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
			ok := search(next, left-1)
			done[i] = false
			if ok {
				memo[k] = true
				return true
			}
		}
		memo[k] = false
		return false
	}
	return search("", len(ops))
}

// A checker never seen failing is not one: it must refuse a stale read.
func TestTheRegisterCheckerRefusesAStaleRead(t *testing.T) {
	type tc struct {
		name string
		ops  []kvOp
		want bool
	}
	cases := []tc{
		{"write then read", []kvOp{{key: "x", write: true, val: "1", call: 0, ret: 1}, {key: "x", val: "1", call: 2, ret: 3}}, true},
		{"stale read", []kvOp{
			{key: "x", write: true, val: "1", call: 0, ret: 1},
			{key: "x", write: true, val: "2", call: 2, ret: 3},
			{key: "x", val: "1", call: 4, ret: 5},
		}, false},
		{"concurrent read sees either", []kvOp{
			{key: "x", write: true, val: "1", call: 0, ret: 1},
			{key: "x", write: true, val: "2", call: 2, ret: 6},
			{key: "x", val: "1", call: 3, ret: 4},
		}, true},
		{"value never written", []kvOp{{key: "x", val: "9", call: 0, ret: 1}}, false},
	}
	runCase := func(t *testing.T, c tc) {
		if got, _ := linearizable(c.ops); got != c.want {
			t.Fatalf("linearizable %v, want %v", got, c.want)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
