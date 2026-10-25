package raft

import (
	"bytes"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// frameOverhead is what Config holds MaxFrameBytes above MaxBatchBytes by,
// the driver's batchFrameOverhead (review M2): an append frame of a batch
// within MaxBatchBytes is within the bound every member reads under.
const frameOverhead = 1 << 10

// evictedLeader is node 1 leading members 1, 2, 3 at term 2 over n commands
// of term 1 with payload bytes each, committed and applied, all loaded
// without their payloads as a start past RecentEntriesBytes loads them
// (#663): what feeds a follower behind it by fetches (#664). It returns the
// leader and the entries as the store holds them.
func evictedLeader(t *testing.T, n, payload, maxBatch int) (*Raft, []core.EntryValue) {
	t.Helper()
	store := make([]core.EntryValue, n)
	var prev uint32
	for i := range store {
		d := bytes.Repeat([]byte{byte('a' + i%26)}, payload)
		e := core.EntryValue{Position: core.PositionValue{Term: 1, Index: uint64(i + 1)}, Kind: core.EntryCommand, BodySum: wire.BodySum(d), Data: d}
		e.ChainSum = wire.ChainSum(prev, &e)
		prev = e.ChainSum
		store[i] = e
	}
	loaded := make([]core.EntryValue, n)
	for i := range store {
		loaded[i] = store[i]
		loaded[i].Data = nil
	}
	r := NewRaft(&Config{
		ID: 1, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: maxBatch, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3,
		RecentEntriesBytes: 1 << 20, MaxApplyLagBytes: 1 << 20,
	}, &StartState{
		HardState: core.HardState{Term: 1, Commit: uint64(n)}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries: loaded, Evicted: uint64(n), Applied: uint64(n), Now: t0,
	})
	r.now = t0.Add(5 * time.Second)
	r.term, r.vote = 2, 1
	r.becomeLeader()
	rd := r.Ready()
	r.StorageDone(rd.Seq, r.log.lastIndex(), 2)
	r.Ready()
	if r.log.evicted != uint64(n) || r.role != core.RoleLeader {
		t.Fatalf("setup: role %v, payloads evicted through %d of %d", r.role, r.log.evicted, n)
	}
	return r, store
}

// storeRead reads [lo, hi) as both shipped stores do (MemoryLog, the file
// log): at least one entry, then as long as the payloads sum to maxBytes.
func storeRead(store []core.EntryValue, lo, hi uint64, maxBytes int) []core.EntryValue {
	var out []core.EntryValue
	size := 0
	for i := lo; i < hi; i++ {
		e := store[i-1]
		size += len(e.Data)
		if i > lo && size > maxBytes {
			break
		}
		e.Data = bytes.Clone(e.Data)
		out = append(out, e)
	}
	return out
}

// appendTo returns the append rd sends to, if any.
func appendTo(rd *ReadyValue, to core.NodeID) (core.MessageValue, bool) {
	for _, m := range rd.Messages {
		if m.Kind == core.MsgApp && m.To == to && len(m.Entries) != 0 {
			return m, true
		}
	}
	return core.MessageValue{}, false
}

// A follower behind the payloads its leader evicted is fed by fetches
// (#664). A store bounds its read by the payloads alone, and the batch it
// returned went out whole: 33 bytes an entry and the message's 112 over
// MaxBatchBytes, past the 1 KiB Config holds MaxFrameBytes above it from
// some 28 entries on. The follower's reader refused the frame, the leader
// fetched the same batch at the next probe, and the follower never caught
// up (#683). Each append now carries a batch within MaxBatchBytes of its
// encoding, as full as that bound lets it, and the next fetch resumes past
// it; a fetch asks for no more entries than one append carries, so a run
// of small entries is not read whole for its head alone.
func TestAFetchedBatchIsCutByItsEncoding(t *testing.T) {
	type tc struct {
		name               string
		n, payload, budget int
	}
	runCase := func(t *testing.T, c tc) {
		r, store := evictedLeader(t, c.n, c.payload, c.budget)
		// The encoding EntryLen states is the one AppendMessage writes.
		one := core.MessageValue{Entries: store[:1]}
		if got, want := wire.EntryLen(&store[0]), len(wire.AppendMessage(nil, &one))-len(wire.AppendMessage(nil, &core.MessageValue{})); got != want {
			t.Fatalf("EntryLen says %d, an entry adds %d to its message", got, want)
		}
		span := uint64(max(1, c.budget/wire.EntryLen(&core.EntryValue{})))
		// Peer 2 refuses the leader's first append: it holds nothing.
		m := msg(core.MsgAppResp, 2, 1, 2)
		m.Reject, m.Index = true, uint64(c.n)
		r.Step(r.now, m)
		appends, fetches := 0, 0
		for range 4 * c.n {
			if r.prs[2].match == r.log.lastIndex() {
				break
			}
			rd := r.Ready()
			for _, f := range rd.Fetch {
				if f.To != 2 {
					continue
				}
				fetches++
				if f.Hi-f.Lo > span {
					t.Fatalf("a fetch asked for [%d, %d), %d entries, one append carries %d at most under %d bytes", f.Lo, f.Hi, f.Hi-f.Lo, span, c.budget)
				}
				r.Fetched(f.To, f.Lo, storeRead(store, f.Lo, f.Hi, c.budget), false)
				rd = r.Ready()
			}
			app, ok := appendTo(&rd, 2)
			if !ok {
				t.Fatalf("peer 2 at match %d of %d got neither a fetch nor an append", r.prs[2].match, r.log.lastIndex())
			}
			appends++
			size := 0
			for i := range app.Entries {
				size += wire.EntryLen(&app.Entries[i])
			}
			frame := len(wire.AppendMessage(nil, &app))
			if frame > c.budget+frameOverhead {
				t.Fatalf("an append of %d entries from %d is a frame of %d bytes, %d of entries, over the %d a member reads under (MaxBatchBytes %d + 1 KiB)",
					len(app.Entries), app.Index+1, frame, size, c.budget+frameOverhead, c.budget)
			}
			if len(app.Entries) > 1 && size > c.budget {
				t.Fatalf("an append of %d entries carries %d bytes of them, over MaxBatchBytes %d", len(app.Entries), size, c.budget)
			}
			last := app.Entries[len(app.Entries)-1].Position.Index
			if last < uint64(c.n) && size+wire.EntryLen(&store[last]) <= c.budget {
				t.Fatalf("an append through %d carries %d bytes of entries, and the next, %d more, fits MaxBatchBytes %d: the cut is not the bound's", last, size, wire.EntryLen(&store[last]), c.budget)
			}
			ack := msg(core.MsgAppResp, 2, 1, 2)
			ack.Index = last
			r.Step(r.now, ack)
		}
		if r.prs[2].match != r.log.lastIndex() {
			t.Fatalf("peer 2 stayed at match %d of %d", r.prs[2].match, r.log.lastIndex())
		}
		t.Logf("%d entries of %d bytes under MaxBatchBytes %d: %d fetches, %d appends", c.n, c.payload, c.budget, fetches, appends)
	}
	cases := []tc{
		{"500-byte entries under 64 KiB, the issue's", 300, 500, 64 << 10},
		{"empty entries under 4 KiB", 2000, 0, 4 << 10},
		{"2-byte entries under 4 KiB", 2000, 2, 4 << 10},
		{"4 KiB entries under 16 KiB", 40, 4 << 10, 16 << 10},
		{"entries of MaxBatchBytes, one an append", 6, 4 << 10, 4 << 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
