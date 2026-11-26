package raft

import (
	"cmp"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

// The linearizability checker's bounded scans (#1413) against the whole
// scans they replaced: on random histories of one key, the two searches
// give the same answer, linearizable or not. The file sorts after every
// seed suite (raft's NOTES.md, Tests), so the shards keep their tests.

// randomHistory is a history of n operations on one key drawn from r: a
// register run whose operations take effect at an instant between their
// call and their return, writes of distinct values, some of unknown
// outcome and taking effect or not, then, one history in two, a read
// changed to another value, which leaves it linearizable or not. disorder
// lets some operations return before their call, a history no run records
// and no scan linearizes.
func randomHistory(r *rand.Rand, n int, unknown float64, disorder bool) []kvOp {
	type timed struct {
		op      kvOp
		idx     int
		at      int64
		applied bool
	}
	all := make([]timed, n)
	//: n operations, called within a window short enough to overlap.
	for i := range all {
		call := r.Int64N(int64(4 * n))
		dur := r.Int64N(12)
		o := timed{op: kvOp{key: "k", call: call, ret: call + dur}, idx: i, at: call + r.Int64N(dur+1), applied: true}
		//: one operation in two a write of its own value, the rest reads.
		if r.IntN(2) == 0 {
			o.op.write, o.op.val = true, fmt.Sprint("w", i)
			//: a write of unknown outcome, which took effect or did not.
			if r.Float64() < unknown {
				o.op.ret, o.applied = math.MaxInt64, r.IntN(2) == 0
			}
		}
		all[i] = o
	}
	order := slices.Clone(all)
	slices.SortStableFunc(order, func(a, b timed) int { return cmp.Compare(a.at, b.at) })
	reg, read := "", map[int]string{}
	//: the register run, in the order the operations took effect.
	for _, o := range order {
		//: a write taking effect sets the register; a read takes it.
		switch {
		case o.op.write && o.applied:
			reg = o.op.val
		case !o.op.write:
			read[o.idx] = reg
		}
	}
	ops := make([]kvOp, 0, n)
	var written []string
	//: each operation as the run answered it.
	for _, o := range all {
		//: a read returns what the register held when it took effect.
		if !o.op.write {
			o.op.val = read[o.idx]
		} else {
			written = append(written, o.op.val)
		}
		//: a disordered history returns some known operations before their call.
		if disorder && o.op.ret != math.MaxInt64 && r.IntN(4) == 0 {
			o.op.ret = o.op.call - 1 - r.Int64N(int64(4*n))
		}
		ops = append(ops, o.op)
	}
	//: one history in two has a read changed to another value written, or the initial one.
	if r.IntN(2) == 0 {
		i := r.IntN(len(ops))
		//: the change lands on a read.
		if !ops[i].write {
			ops[i].val = ""
			//: the value is a written one, unless none is.
			if k := r.IntN(len(written) + 1); k < len(written) {
				ops[i].val = written[k]
			}
		}
	}
	return ops
}

// TestTheBoundedScansAnswerAsTheWholeScans holds linearizableKey's bounded
// scans to the search that read every operation at each step: the same
// answer on every history, and both answers met, so the comparison is put
// to the test on each side.
func TestTheBoundedScansAnswerAsTheWholeScans(t *testing.T) {
	type tc struct {
		name      string
		histories uint64
		size      int
		unknown   float64
		disorder  bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		var yes, no int
		//: the case's histories, one seed each.
		for seed := range c.histories {
			ops := randomHistory(rand.New(rand.NewPCG(seed, uint64(c.size))), c.size, c.unknown, c.disorder)
			bounded := linearizableKeyScan(slices.Clone(ops), true)
			whole := linearizableKeyScan(slices.Clone(ops), false)
			//: the bounded scans and the whole ones disagree: the bound dropped or added a linearization.
			if bounded != whole {
				t.Fatalf("seed %d: bounded scans say %v, whole scans %v, on %+v", seed, bounded, whole, ops)
			}
			//: count each answer.
			if whole {
				yes++
			} else {
				no++
			}
		}
		t.Logf("linearizable %d, not %d", yes, no)
		//: one answer never given: the comparison was not put to the test on that side.
		if yes == 0 || no == 0 {
			t.Fatalf("linearizable %d, not %d: both answers must be met", yes, no)
		}
	}
	cases := []tc{
		{name: "small, every outcome known", histories: 2000, size: 6},
		{name: "small, unknown writes", histories: 2000, size: 8, unknown: 0.3},
		{name: "past a word of operations", histories: 200, size: 66, unknown: 0.05},
		{name: "returns before calls", histories: 2000, size: 8, unknown: 0.2, disorder: true},
	}
	//: each shape of history.
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
