package raft

import (
	"testing"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// A restart starts the step function at the newest snapshot that restored,
// while the store still holds the log back to the older one less the
// trailing window (R6-02): the first cut after it compacts through a point
// at or below the base. Compact returned on it without a word, the store was
// never compacted, and a node restarted between every two of its cuts grew
// its log to the no-space alarm (#741). Such a point forgets nothing in the
// step function, and the Ready still asks the store for it; past the
// applied index, or at 0, nothing is asked.
func TestACompactionAtOrBelowTheBaseStillReachesTheStore(t *testing.T) {
	ents := chainEntries(0, 11, 1, 1, 1, 1, 1, 1)
	r := NewRaft(&Config{
		ID: 1, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3,
	}, &StartState{
		HardState: core.HardState{Term: 1, Commit: 15},
		Snapshot:  core.SnapshotMetaValue{Position: core.PositionValue{Term: 1, Index: 10}, Membership: testConf(1)},
		Entries:   ents, Applied: 15, Now: t0,
	})
	r.Ready()
	if r.log.applied != 15 {
		t.Fatalf("setup: applied %d, want 15", r.log.applied)
	}
	type tc struct {
		name          string
		index, want   uint64
		base, entries uint64
	}
	// The cases run in order, on the one step function they compact.
	runCase := func(t *testing.T, x tc) {
		t.Helper()
		r.Compact(x.index)
		rd := r.Ready()
		if rd.Compact != x.want {
			t.Fatalf("Compact(%d) %s: the Ready compacts the store through %d, want %d", x.index, x.name, rd.Compact, x.want)
		}
		if got := r.SnapshotIndex(); got != x.base || uint64(len(r.log.ents)) != x.entries {
			t.Fatalf("Compact(%d) %s: base %d holding %d entries, want %d holding %d", x.index, x.name, got, len(r.log.ents), x.base, x.entries)
		}
	}
	for _, x := range []tc{
		{"below the base", 6, 6, 10, 6},
		{"at the base", 10, 10, 10, 6},
		{"at 0", 0, 0, 10, 6},
		{"past the applied index", 16, 0, 10, 6},
		{"past the base", 12, 12, 12, 4},
	} {
		runCase(t, x)
	}
}
