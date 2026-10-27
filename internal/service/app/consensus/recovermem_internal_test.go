package consensus

import (
	"crypto/ed25519"
	"fmt"
	"path/filepath"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// logReads is what the Entries calls of a log handed back while a test
// counted them: how many calls, the most entries and payload bytes one
// call held, and the lowest index any call read.
type logReads struct {
	calls, most, mostBytes int
	lowest                 uint64
}

// countReads counts every batch l's Entries hands back from now on, until
// the test sets l.afterEntries back to nil.
func countReads(l *fileLog) *logReads {
	r := &logReads{lowest: ^uint64(0)}
	l.afterEntries = func(es []coreconsensus.EntryValue) {
		n := 0
		for i := range es {
			n += len(es[i].Data)
		}
		r.calls++
		r.most, r.mostBytes = max(r.most, len(es)), max(r.mostBytes, n)
		if len(es) > 0 {
			r.lowest = min(r.lowest, es[0].Position.Index)
		}
	}
	return r
}

// Recover read the survivor's whole retained log into memory, payloads
// included: the configuration in force at the index it keeps from the
// log's first entry, then the cluster version past the newest snapshot
// twice, each read with no byte bound. Its heap peaked at about three times
// the log, on the host where the same directory starts in about 1.4 since
// #630 and #663, and a survivor whose compaction had stalled was OOM-killed
// at every rerun: the cluster could not leave its quorum loss (#700).
// Every read Recover makes of the log now hands back one batch at most,
// offlineBatchEntries entries and offlineBatchBytes of payloads, whatever
// the log's size: the test counts what each Entries call holds over a log
// several times that, then checks the history Recover wrote.
func TestRecoverHoldsOneBatchOfTheLogAtATime(t *testing.T) {
	const logSize = 3 << 20
	for _, payload := range []int{64, 4096} {
		t.Run(fmt.Sprintf("payload %d B", payload), func(t *testing.T) {
			dir := t.TempDir()
			run := filepath.Join(t.TempDir(), "run")
			cid := coreconsensus.ClusterID{0x70, 0x0a}
			id, last, records := startTail(t, dir, run, cid, payload, logSize)
			d := tailOpen(t, dir, run)
			defer func() {
				if err := d.Close(); err != nil {
					t.Error(err)
				}
			}()
			st := must(d.Log().Stamp())(t)
			pub, _, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			next := coreconsensus.ClusterID{0x70, 0x0b}
			members := []coreconsensus.MemberValue{{ID: id, Incarnation: st.Incarnation, Slot: st.Slot, SPKI: MemorySPKI(id), Voter: true}}
			reads := countReads(d.d.log)
			err = Recover(d, next, members, &RecoverOptions{Confirm: next.String(), Operator: "op:recover", WitnessKeys: []ed25519.PublicKey{pub}})
			d.d.log.afterEntries = nil
			if err != nil {
				t.Fatalf("Recover on the only voter: %v", err)
			}
			t.Logf("payload %d B: %d entries, %.1f MiB of records; Recover read the log %d times, at most %d entries and %.2f MiB of payloads at once; a batch is %d entries and %.2f MiB",
				payload, last, mib(records), reads.calls, reads.most, mib(int64(reads.mostBytes)), offlineBatchEntries, mib(int64(offlineBatchBytes)))
			if uint64(reads.most) > offlineBatchEntries || reads.mostBytes > offlineBatchBytes {
				t.Errorf("payload %d B: one read of the log handed Recover %d entries and %d bytes of payloads, past a batch of %d entries and %d bytes: it holds the log, %d entries, in memory",
					payload, reads.most, reads.mostBytes, offlineBatchEntries, offlineBatchBytes, last)
			}
			// The history is the one the log holds: every entry kept, under
			// the new identity, then the forced configuration of this node.
			st = must(d.Log().Stamp())(t)
			if st.Log.Cluster != next || st.Log.Predecessor != cid || st.Log.KeptIndex != last || st.Log.AdoptedFrom != 0 {
				t.Fatalf("the new identity is %+v, want cluster %v after %v, kept %d, no adopted range (protocol 1)", st.Log, next, cid, last)
			}
			forced := must(d.Log().Entries(last+1, last+2, 1<<20, nil))(t)
			a, err := wire.ParseAdminAt(forced[0].Version, forced[0].Data)
			if err != nil || forced[0].Kind != coreconsensus.EntryMembership || forced[0].Version != 0 || a.Change != wire.ChangeForced ||
				len(a.Membership.Members) != 1 || a.Membership.Members[0].ID != id {
				t.Fatalf("the entry after the kept log is %v version %d, %+v (%v), want this node's forced configuration of version 0", forced[0].Kind, forced[0].Version, a, err)
			}
		})
	}
}

// historyEntry is an engine entry of the synthetic log of
// TestTheHistoryInForceIsReadPastTheSnapshot: its kind and its payload.
type historyEntry struct {
	kind  coreconsensus.EntryKind
	admin wire.AdminValue
}

// writeHistory appends entries 1 through n of term 1 to l, chained and
// committed, commands of 16 bytes but for the engine entries of at, and
// syncs them.
func writeHistory(t *testing.T, l *fileLog, n uint64, at map[uint64]historyEntry) {
	t.Helper()
	var prev uint32
	es := make([]coreconsensus.EntryValue, 0, n)
	for i := uint64(1); i <= n; i++ {
		e := coreconsensus.EntryValue{Position: coreconsensus.PositionValue{Term: 1, Index: i}, Kind: coreconsensus.EntryCommand, Data: fmt.Appendf(nil, "cmd-%012d", i)}
		if h, ok := at[i]; ok {
			e.Kind, e.Data = h.kind, wire.AppendAdminAt(nil, 0, &h.admin)
		}
		e.BodySum = wire.BodySum(e.Data)
		e.ChainSum = wire.ChainSum(prev, &e)
		prev = e.ChainSum
		es = append(es, e)
	}
	if err := l.Append(es, &coreconsensus.HardState{Term: 1, Commit: n}); err != nil {
		t.Fatal(err)
	}
	mustSync(t, l)
}

// publishMeta publishes in s a snapshot of one byte under meta: what is
// read of it here is its metadata alone.
func publishMeta(t *testing.T, s *fileSnapshots, meta coreconsensus.SnapshotMetaValue) {
	t.Helper()
	meta.ID, meta.EncodedSize = snapshotID(meta.Position.Index, meta.Position.Term), 1
	sink, _, err := s.Create(meta.ID, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Commit(meta); err != nil {
		t.Fatal(err)
	}
}

// historyBefore700 is what Recover read before #700, the log whole: the
// newest snapshot's configuration, then every configuration entry from the
// log's first through index; its cluster version, then every raise past it
// through index. The test holds inForceAt to it.
func historyBefore700(t *testing.T, l *fileLog, s *fileSnapshots, index uint64) (coreconsensus.MembershipValue, uint32) {
	t.Helper()
	var m coreconsensus.MembershipValue
	var ver uint32
	var gen, base uint64
	if metas := must(s.List())(t); len(metas) > 0 {
		m, ver, gen, base = metas[0].Membership, metas[0].ClusterVersion, metas[0].ClusterGeneration, metas[0].Position.Index
	}
	first, last := l.Bounds()
	for _, e := range must(l.Entries(first, min(last, index)+1, 1<<30, nil))(t) {
		a, err := wire.ParseAdminAt(e.Version, e.Data)
		switch {
		case err != nil:
		case e.Kind == coreconsensus.EntryMembership:
			m = a.Membership
		case e.Kind == coreconsensus.EntryClusterVersion && e.Position.Index > base && a.Pre.Generation == gen && a.ClusterVersion > ver:
			ver, gen = a.ClusterVersion, gen+1
		}
	}
	return m, ver
}

// confLine is a configuration as the test compares it: its members and
// their roles, then its retired IDs.
func confLine(m *coreconsensus.MembershipValue) string {
	return fmt.Sprintf("%s retired %v", memberLines(m.Members), m.Retired)
}

// The configuration and the cluster version in force where Recover keeps
// the log are those of the newest snapshot and of the entries after it,
// read a batch at a time: Recover read the configuration from the log's
// first entry, below the snapshot whose metadata already answers there,
// and the cluster version past it, each in one read of the whole range
// (#700). The bounded reader gives the answer the whole read gave at every
// index, below the snapshot as well, where the log is still read from its
// first entry; past the snapshot it reads nothing at or below it.
func TestTheHistoryInForceIsReadPastTheSnapshot(t *testing.T) {
	d := openDir(t, t.TempDir(), 1<<20)
	defer func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	}()
	member := func(id coreconsensus.NodeID, voter bool) coreconsensus.MemberValue {
		return coreconsensus.MemberValue{ID: id, SPKI: MemorySPKI(id), Voter: voter}
	}
	conf := func(retired []coreconsensus.NodeID, ms ...coreconsensus.MemberValue) historyEntry {
		return historyEntry{coreconsensus.EntryMembership, wire.AdminValue{Operator: "op:t", Membership: coreconsensus.MembershipValue{Members: ms, Retired: retired}}}
	}
	raise := func(gen uint64, v uint32) historyEntry {
		return historyEntry{coreconsensus.EntryClusterVersion, wire.AdminValue{Operator: "op:t", Pre: coreconsensus.PreconditionSpec{Generation: gen}, ClusterVersion: v}}
	}
	// A log of 3 batches of entries; a snapshot at base, its configuration
	// the one the log's entry k left; past base, a raise of the snapshot's
	// generation, one of a generation already used, then a configuration.
	const n, k, base = 3*offlineBatchEntries + 100, offlineBatchEntries / 2, 2*offlineBatchEntries + 7
	founding := conf(nil, member(1, true))
	learner := conf(nil, member(1, true), member(2, false))
	at := map[uint64]historyEntry{
		1: founding, k: learner,
		base + 5: raise(1, 3), base + 7: raise(1, 4),
		base + 10: conf([]coreconsensus.NodeID{2}, member(1, true), member(3, false)),
	}
	writeHistory(t, d.d.log, n, at)
	publishMeta(t, d.d.snaps, coreconsensus.SnapshotMetaValue{
		Position:   coreconsensus.PositionValue{Term: 1, Index: base},
		Membership: learner.admin.Membership, ClusterVersion: 2, ClusterGeneration: 1,
	})
	for _, index := range []uint64{k - 1, k, base - 1, base, base + 4, base + 5, base + 7, base + 9, base + 10, n} {
		wantConf, wantVer := historyBefore700(t, d.d.log, d.d.snaps, index)
		reads := countReads(d.d.log)
		m, ver, err := inForceAt(d.d.log, d.d.snaps, index)
		d.d.log.afterEntries = nil
		if err != nil {
			t.Fatalf("inForceAt(%d): %v", index, err)
		}
		if confLine(&m) != confLine(&wantConf) || ver != wantVer {
			t.Errorf("at %d: %s at cluster version %d, the whole log read gives %s at %d", index, confLine(&m), ver, confLine(&wantConf), wantVer)
		}
		if uint64(reads.most) > offlineBatchEntries || reads.mostBytes > offlineBatchBytes {
			t.Errorf("at %d: one read handed back %d entries and %d bytes of payloads, past a batch of %d and %d", index, reads.most, reads.mostBytes, offlineBatchEntries, offlineBatchBytes)
		}
		if index > base && reads.lowest <= base {
			t.Errorf("at %d: read the log from %d, at or below the snapshot at %d, whose metadata answers there", index, reads.lowest, base)
		}
	}
	// The answers themselves, beside their equality with the whole read.
	if m, ver := must2(inForceAt(d.d.log, d.d.snaps, n))(t); ver != 3 || len(m.Members) != 2 || m.Members[1].ID != 3 || !m.IsRetired(2) {
		t.Fatalf("at the log's end: %s at version %d, want the last configuration and the one raise of the snapshot's generation, 3", confLine(&m), ver)
	}
	if m, ver := must2(inForceAt(d.d.log, d.d.snaps, k-1))(t); ver != 2 || len(m.Members) != 1 {
		t.Fatalf("below the snapshot: %s at version %d, want the founding configuration and the snapshot's version", confLine(&m), ver)
	}
	// A newer snapshot the log does not continue, under another term at
	// its index: its configuration is not the one the log's entries left
	// there, and the log is read from its first entry, as it always was.
	other := base + 3
	publishMeta(t, d.d.snaps, coreconsensus.SnapshotMetaValue{
		Position:   coreconsensus.PositionValue{Term: 2, Index: other},
		Membership: founding.admin.Membership, ClusterVersion: 5,
	})
	for _, index := range []uint64{other - 1, other, base + 4, base + 9, n} {
		wantConf, wantVer := historyBefore700(t, d.d.log, d.d.snaps, index)
		reads := countReads(d.d.log)
		m, ver, err := inForceAt(d.d.log, d.d.snaps, index)
		d.d.log.afterEntries = nil
		if err != nil {
			t.Fatalf("inForceAt(%d) past a snapshot the log does not continue: %v", index, err)
		}
		if confLine(&m) != confLine(&wantConf) || ver != wantVer || reads.lowest != 1 {
			t.Errorf("at %d past a snapshot the log does not continue: %s at cluster version %d read from %d, the whole log read gives %s at %d from 1",
				index, confLine(&m), ver, reads.lowest, confLine(&wantConf), wantVer)
		}
	}
}
