package consensus

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// silenceRecordIn reads dir's SILENT: its term and index, ok false when
// there is none or it is not 16 bytes.
func silenceRecordIn(dir string) (term, index uint64, ok bool) {
	b, err := os.ReadFile(filepath.Join(dir, fileSilent))
	if err != nil || len(b) != 16 {
		return 0, 0, false
	}
	return binary.LittleEndian.Uint64(b), binary.LittleEndian.Uint64(b[8:]), true
}

// #793: the lowered point is written in SILENT's one form, silenceRecord,
// the term kept, so a node of any release reads it back. It only lowers:
// never a higher index, never a condition the directory does not hold (one
// already ended stays ended), never one unreadable (the safe side silence
// reports) or not filled in yet (the next start fills it in from the log).
func TestALoweredSilenceKeepsItsRecordsForm(t *testing.T) {
	dir := t.TempDir()
	dd := silenceOpen(t, dir, t.TempDir())
	defer func() {
		if err := dd.Close(); err != nil {
			t.Error(err)
		}
	}()
	l := dd.d.log
	path := filepath.Join(dir, fileSilent)
	write := func(b []byte) {
		t.Helper()
		if err := writeFileSync(path, b); err != nil {
			t.Fatal(err)
		}
	}
	lower := func(index uint64) {
		t.Helper()
		if err := l.lowerSilence(index); err != nil {
			t.Fatalf("lowerSilence(%d): %v", index, err)
		}
	}
	read := func() []byte {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	write(silenceRecord(5, 9))
	lower(4)
	if got, want := read(), silenceRecord(5, 4); !bytes.Equal(got, want) {
		t.Fatalf("SILENT after lowering 9 to 4 is %x, want %x", got, want)
	}
	if term, index, unreadable, err := l.silence(); err != nil || unreadable != nil || term != 5 || index != 4 {
		t.Fatalf("silence() reads %d/%d (unreadable %v, err %v), want 5/4", term, index, unreadable, err)
	}
	lower(6)
	if got, want := read(), silenceRecord(5, 4); !bytes.Equal(got, want) {
		t.Fatalf("SILENT after a lowering to 6 over 4 is %x, want it unchanged %x", got, want)
	}
	for _, kept := range [][]byte{silenceRecord(5, 0), silenceRecord(5, 9)[:15]} {
		write(kept)
		lower(3)
		if got := read(); !bytes.Equal(got, kept) {
			t.Fatalf("SILENT %x became %x: a condition not filled in, or unreadable, is never rewritten", kept, got)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	lower(3)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a lowering over an ended silence wrote SILENT back: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a lowering left its temporary file: %v", err)
	}
}

// pendingTruncates reports whether the batch l is about to sync truncates
// the log from index from. It runs inside duringSync, on the storage
// actor's goroutine, which owns the buffer.
func pendingTruncates(l *fileLog, from uint64) bool {
	for off := 0; off < len(l.pending); {
		r, ok := parseRecord(l.pending[off:])
		if !ok || r.length <= 0 {
			return false
		}
		if r.kind == recTruncate && r.index == from {
			return true
		}
		off += r.length
	}
	return false
}

// durableLast is the last index l made durable.
func durableLast(l *fileLog) uint64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.dur.prevIndex
}

// #793, on disk. The leader V of term T is cut off and appends four writes
// no other node sees, c+1..c+4; A and B elect a leader of a later term,
// whose no-op is committed at c+1. V's record of c+3 rots, and
// TruncateAndStaySilent leaves it SILENT(T, c+3) over the tail c+1..c+2
// that never counted. Back, V accepts the leader's append at c+1, which
// conflicts with that tail: its point falls to c+1, and the truncation
// leaves in the same Ready. The driver persisted the end of a silence and
// never a lowering: a crash once that batch was on disk, before the commit
// reached V, restarted it on SILENT(T, c+3) with a log that ended at c+1,
// and in a cluster that wrote nothing the leader's one no-op, at c+2, never
// reached the old point. The lowered point is now on disk before the
// storage actor writes the truncation, and V, restarted from the state that
// batch's sync leaves, ends its silence in a quiet cluster.
func TestALoweredSilencePointIsOnDiskBeforeItsTruncation(t *testing.T) {
	run := t.TempDir()
	// Made before the cleanup that crashes the nodes is registered, so that
	// cleanup runs first: Windows removes no directory whose LOCK is held.
	crashRoot := t.TempDir()
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{0x79, 0x03}}
	var nodes []*diskNode
	var members []coreconsensus.MemberValue
	for i := range 3 {
		dn := &diskNode{dir: t.TempDir()}
		dn.dd = silenceOpen(t, dn.dir, run)
		slot := fmt.Sprintf("p%d", i)
		id, inc, err := Init(dn.dd, c.cid, slot)
		if err != nil {
			t.Fatal(err)
		}
		dn.id = id
		nodes = append(nodes, dn)
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: MemorySPKI(id), Voter: true})
	}
	start := func(dn *diskNode) {
		t.Helper()
		dn.sm = newKV()
		dn.node = must(NewNode(diskConfig(c, dn)))(t)
	}
	for _, dn := range nodes {
		dn.sm = newKV()
		if err := Bootstrap(context.Background(), new(diskConfig(c, dn)), members); err != nil {
			t.Fatal(err)
		}
		start(dn)
	}
	var restarted *diskNode
	t.Cleanup(func() {
		for _, dn := range append(nodes, restarted) {
			if dn != nil && dn.node != nil {
				dn.crash(t, c)
			}
		}
	})

	v := leaderOf(t, nodes)
	var others []*diskNode
	for _, dn := range nodes {
		if dn != v {
			others = append(others, dn)
		}
	}
	tok := propose(t, v.node, "warm=up")
	c.waitAppliedDisk(nodes, tok.Index)
	base, term := tok.Index, v.node.Status().Term
	c.net.Isolate(v.id, true)
	// One batch each: the reopen drops the whole batch a damaged record
	// tears (#745), and the damage at c+3 must leave c+1..c+2.
	for i := uint64(1); i <= 4; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, _, _ = v.node.Propose(ctx, fmt.Appendf(nil, "lost%d=x", i))
		}()
		for deadline := time.Now().Add(time.Second); durableLast(v.dd.d.log) < base+i; {
			if time.Now().After(deadline) {
				t.Fatalf("setup: the cut-off leader made %d durable, want its write at %d", durableLast(v.dd.d.log), base+i)
			}
			time.Sleep(time.Millisecond)
		}
	}
	var lead *diskNode
	for deadline := time.Now().Add(10 * time.Second); lead == nil; {
		for _, dn := range others {
			if st := dn.node.Status(); st.Role == coreconsensus.RoleLeader && st.Term > term && st.Commit > base {
				lead = dn
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("setup: A and B elected no leader past term %d", term)
		}
		time.Sleep(5 * time.Millisecond)
	}
	v.crash(t, c)

	// V's record of c+3 rots: quarantine, then the TruncateAndStaySilent exit.
	dd := silenceOpen(t, v.dir, run)
	l := dd.d.log
	l.mu.RLock()
	s := l.slots[base+3-l.first]
	l.mu.RUnlock()
	if err := dd.Close(); err != nil {
		t.Fatal(err)
	}
	segPath := filepath.Join(v.dir, "log", segName(s.seg))
	b := must(os.ReadFile(segPath))(t)
	b[s.off+int64(recHeader+recFixed)] ^= 0xff
	if err := os.WriteFile(segPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	v.dd = silenceOpen(t, v.dir, run)
	v.sm = newKV()
	if _, err := NewNode(diskConfig(c, v)); !errors.Is(err, coreconsensus.LogQuarantined) {
		t.Fatalf("setup: damaged log: %v", err)
	}
	if err := ClearQuarantine(v.dd, TruncateAndStaySilent, "op:silent"); err != nil {
		t.Fatal(err)
	}
	silentTerm, point, ok := silenceRecordIn(v.dir)
	if _, last := v.dd.Log().Bounds(); !ok || point != base+3 || last != base+2 {
		t.Fatalf("setup: after TruncateAndStaySilent SILENT is %d/%d (present %v) and the log ends at %d; want the point at %d over the tail %d..%d",
			silentTerm, point, ok, last, base+3, base+1, base+2)
	}

	// The batch that truncates from c+1: what SILENT holds when it is
	// written, and the directory each write of it leaves, a crash there.
	var hits atomic.Int32
	var atTruncation atomic.Uint64
	var cut atomic.Pointer[string]
	copyErr := make(chan error, 4)
	v.dd.d.log.duringSync = func() {
		if !pendingTruncates(v.dd.d.log, base+1) {
			return
		}
		k := hits.Add(1)
		if k == 1 {
			_, index, _ := silenceRecordIn(v.dir)
			atTruncation.Store(index)
		}
		dst := filepath.Join(crashRoot, fmt.Sprintf("cut%d", k))
		if err := copyTree(v.dir, dst); err != nil {
			copyErr <- err
			return
		}
		cut.Store(&dst)
	}
	start(v)
	c.net.Isolate(v.id, false)
	for deadline := time.Now().Add(10 * time.Second); ; {
		if _, _, silent := silenceRecordIn(v.dir); !silent && cut.Load() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("V never ended its silence over the leader's no-op (truncation seen %d times, status %+v)", hits.Load(), v.node.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case err := <-copyErr:
		t.Fatal(err)
	default:
	}
	if got := atTruncation.Load(); got != base+1 {
		t.Errorf("SILENT's point was %d when V's storage actor wrote the truncation from %d: the truncation reached the disk before the point it lowers (#793)",
			got, base+1)
	}

	// V crashes; it restarts from what the truncating batch left on disk.
	v.crash(t, c)
	dir := *cut.Load()
	restarted = &diskNode{dir: dir, id: v.id}
	restarted.dd = silenceOpen(t, dir, run)
	_, crashPoint, _ := silenceRecordIn(dir)
	_, crashLast := restarted.dd.Log().Bounds()
	t.Logf("crash state: log last %d, SILENT point %d; the leader's log ends at %d", crashLast, crashPoint, lead.node.Status().Commit)
	start(restarted)
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, _, silent := silenceRecordIn(dir); !silent {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restarted from the crash state (log last %d, SILENT point %d), V is still silent after 5 s of a quiet cluster: leader commit %d, V commit %d",
				crashLast, crashPoint, lead.node.Status().Commit, restarted.node.Status().Commit)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// #792, on the install's crash window: an install publishes its snapshot,
// restores it, and only then moves raft onto it, whose Ready lowers a
// silence's point over a log that held the snapshot's index under another
// term. A crash in between, during a restore of seconds, restarted the
// node on the old point, and finishInstall dropped that log at the start:
// silent again over a log too short for its point, as in #793. The start
// now lowers the point past the snapshot before it drops the log, and only
// over that log: one that holds the index at its term keeps its suffix,
// and one that ends before the index proves nothing.
func TestAnInterruptedInstallLowersTheSilencePointBeforeTheLog(t *testing.T) {
	cases := []struct {
		name             string
		held             uint64
		snapAt, snapTerm uint64
		silent, want     uint64
	}{
		{name: "the index held under another term", held: 6, snapAt: 4, snapTerm: 2, silent: 7, want: 5},
		{name: "the index held at its term", held: 6, snapAt: 4, snapTerm: 1, silent: 7, want: 7},
		{name: "a log that ends before the index", held: 3, snapAt: 5, snapTerm: 2, silent: 9, want: 9},
		{name: "no silence", held: 6, snapAt: 4, snapTerm: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			dd := silenceOpen(t, dir, t.TempDir())
			defer func() {
				if err := dd.Close(); err != nil {
					t.Error(err)
				}
			}()
			if err := dd.Log().Append(ents(1, tc.held, 1), nil); err != nil {
				t.Fatal(err)
			}
			mustSync(t, dd.Log())
			if tc.silent != 0 {
				if err := writeFileSync(filepath.Join(dir, fileSilent), silenceRecord(2, tc.silent)); err != nil {
					t.Fatal(err)
				}
			}
			// The point SILENT holds at each write the start syncs.
			var seen []uint64
			dd.d.log.duringSync = func() {
				_, index, _ := silenceRecordIn(dir)
				seen = append(seen, index)
			}
			n := &Node{cfg: Config{Log: dd.Log()}}
			first, last := dd.Log().Bounds()
			base := coreconsensus.SnapshotMetaValue{Position: coreconsensus.PositionValue{Term: tc.snapTerm, Index: tc.snapAt}}
			if _, _, err := n.finishInstall(&base, first, last); err != nil {
				t.Fatal(err)
			}
			term, index, ok := silenceRecordIn(dir)
			if ok != (tc.silent != 0) || index != tc.want || (ok && term != 2) {
				t.Fatalf("after the start SILENT is %d/%d (present %v), want 2/%d", term, index, ok, tc.want)
			}
			if tc.want != tc.silent && len(seen) == 0 {
				t.Fatal("setup: the start synced no write of its log")
			}
			for _, at := range seen {
				if at != tc.want {
					t.Fatalf("SILENT held the point %d when the start synced its log (%v), want %d before the log changes", at, seen, tc.want)
				}
			}
		})
	}
}
