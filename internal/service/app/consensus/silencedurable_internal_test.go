package consensus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/fs/flock"
)

// silenceOpen opens the data directory under dir on 16 KiB segments, with
// its boot-scoped marker directory run inside the test's own temporary
// directory as well: nothing is written outside t.TempDir().
func silenceOpen(t *testing.T, dir, run string) DataDir {
	t.Helper()
	d, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: 16 << 10, RuntimeDir: run})
	//: no flock(2), no data directory (illumos, Solaris): the refusal is
	//: asserted, then the case skips, as openDir's do.
	if !flock.Native {
		if !errors.Is(err, coreproc.UnsupportedPlatform) {
			t.Fatalf("NewDataDirWith on %s = %v, want UNSUPPORTED_PLATFORM", runtime.GOOS, err)
		}
		t.Skipf("%s has no flock(2), so a data directory is refused there (ADR 0144)", runtime.GOOS)
	}
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return d
}

// pendingRange is the first and the last entry index of the batch the file
// log is about to sync, read from its pending buffer. It runs on the
// storage actor's goroutine, inside duringSync: the actor holds the write
// lock, and the buffer is its own.
func pendingRange(l *fileLog) (lo, hi uint64) {
	for off := 0; off < len(l.pending); {
		r, ok := parseRecord(l.pending[off:])
		if !ok || r.length <= 0 {
			break
		}
		if r.kind == recEntry {
			if lo == 0 {
				lo = r.index
			}
			hi = r.index
		}
		off += r.length
	}
	return lo, hi
}

// #684: three voters on disk. The lagger G gets heartbeats and no append, so
// the leader L commits 30 acknowledged writes with the victim V alone. A
// synced record of V rots, V is quarantined and exits by
// TruncateAndStaySilent, and restarts silent under L, which sends it the
// missing entries in one append: it commits L's commit and meets #636's
// condition. The driver removed SILENT and synced the directory on the loop
// as soon as the step function said so, before the storage actor had synced
// the same Ready's entries. The state on disk is copied at the first
// fdatasync of V's log once SILENT is gone: a power cut at that instant.
// V restarted from it was a voter again with a log that stopped at the
// truncation point; L failed, and V and G elected V, which lacked the writes
// it had acknowledged. The end of the silence now waits for those entries
// to be durable, so a crash state without SILENT holds them all.
func TestTheEndOfASilenceIsDurableOnlyAfterTheEntriesThatEndIt(t *testing.T) {
	run := t.TempDir()
	// Made before the cleanup that crashes the nodes is registered, so that
	// cleanup runs first: Windows removes no directory whose LOCK is held.
	crashDir := filepath.Join(t.TempDir(), "crash")
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{0x5d}}
	var nodes []*diskNode
	var members []coreconsensus.MemberValue
	trs := map[coreconsensus.NodeID]*kindFilter{}
	for i := range 3 {
		dn := &diskNode{dir: t.TempDir()}
		dn.dd = silenceOpen(t, dn.dir, run)
		slot := fmt.Sprintf("s%d", i)
		id, inc, err := Init(dn.dd, c.cid, slot)
		if err != nil {
			t.Fatal(err)
		}
		dn.id = id
		trs[id] = &kindFilter{MemoryTransport: NewMemoryTransport(c.net, id)}
		nodes = append(nodes, dn)
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: MemorySPKI(id), Voter: true})
	}
	config := func(dn *diskNode) Config {
		cfg := diskConfig(c, dn)
		cfg.Transport = trs[dn.id]
		cfg.SnapshotEvery, cfg.SnapshotEveryBytes = 1<<30, 1<<29
		return cfg
	}
	start := func(dn *diskNode) {
		t.Helper()
		dn.sm = newKV()
		dn.node = must(NewNode(config(dn)))(t)
	}
	for _, dn := range nodes {
		dn.sm = newKV()
		if err := Bootstrap(context.Background(), new(config(dn)), members); err != nil {
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
	lead := leaderOf(t, nodes)
	var victim, lagger *diskNode
	for _, dn := range nodes {
		switch {
		case dn == lead:
		case victim == nil:
			victim = dn
		default:
			lagger = dn
		}
	}
	tok := propose(t, lead.node, "warm=up")
	c.waitAppliedDisk(nodes, tok.Index)
	term := lead.node.Status().Term

	trs[lagger.id].setDrop(func(m *coreconsensus.MessageValue) bool { return m.Kind == coreconsensus.MsgApp })
	_, laggerLast := lagger.dd.Log().Bounds()
	acked := map[string]uint64{}
	for i := range 30 {
		key := fmt.Sprintf("ack%02d", i)
		tok = propose(t, lead.node, key+"="+strings.Repeat("v", 1024))
		acked[key] = tok.Index
	}
	waitAppliedOnly(t, victim, tok.Index)
	if st := lead.node.Status(); st.Role != coreconsensus.RoleLeader || st.Term != term {
		t.Skipf("setup: leadership changed during the writes (%+v)", st)
	}
	_, oldLast := lead.dd.Log().Bounds()

	// V goes down; the synced record of ack03 rots; quarantine, then the
	// TruncateAndStaySilent exit.
	c.net.Isolate(victim.id, true)
	victim.crash(t, c)
	dd := silenceOpen(t, victim.dir, run)
	l := dd.d.log
	l.mu.RLock()
	s := l.slots[acked["ack03"]-l.first]
	l.mu.RUnlock()
	if err := dd.Close(); err != nil {
		t.Fatal(err)
	}
	segPath := filepath.Join(victim.dir, "log", segName(s.seg))
	b := must(os.ReadFile(segPath))(t)
	b[s.off+int64(recHeader+recFixed)] ^= 0xff
	if err := os.WriteFile(segPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	victim.dd = silenceOpen(t, victim.dir, run)
	victim.sm = newKV()
	if _, err := NewNode(config(victim)); !errors.Is(err, coreconsensus.LogQuarantined) {
		t.Fatalf("damaged log: %v", err)
	}
	if err := ClearQuarantine(victim.dd, TruncateAndStaySilent, "op:silent"); err != nil {
		t.Fatal(err)
	}
	_, hint := victim.dd.Log().Bounds()
	silentFile := filepath.Join(victim.dir, fileSilent)
	if _, err := os.Stat(silentFile); err != nil {
		t.Fatalf("setup: no silence condition after TruncateAndStaySilent: %v", err)
	}
	t.Logf("leader %d term %d last %d; victim truncated to %d; lagger stays at %d", lead.id, term, oldLast, hint, laggerLast)

	// The crash point: the first fdatasync of V's log once SILENT is gone,
	// between the write of a batch and its sync. What the directory holds
	// then is copied: a power cut there leaves no more than that.
	var copied atomic.Bool
	var pendingLo, pendingHi atomic.Uint64
	copyDone := make(chan error, 1)
	victim.dd.d.log.duringSync = func() {
		if copied.Load() {
			return
		}
		if _, err := os.Stat(silentFile); !errors.Is(err, os.ErrNotExist) {
			return
		}
		copied.Store(true)
		lo, hi := pendingRange(victim.dd.d.log)
		pendingLo.Store(lo)
		pendingHi.Store(hi)
		copyDone <- copyTree(victim.dir, crashDir)
	}
	start(victim)
	c.net.Isolate(victim.id, false)
	go func() { _, _, _ = lead.node.Propose(context.Background(), []byte("trigger=1")) }()
	// Once the silence ended durably, V's log may have nothing more to
	// sync: one more write of the leader gives the crash point its sync.
	kicked := false
	for deadline := time.Now().Add(20 * time.Second); !copied.Load(); {
		if _, err := os.Stat(silentFile); errors.Is(err, os.ErrNotExist) && !kicked {
			kicked = true
			go func() { _, _, _ = lead.node.Propose(context.Background(), []byte("after=end")) }()
		}
		if time.Now().After(deadline) {
			t.Fatalf("the silence never ended, or no sync of the victim's log followed (status %+v)", victim.node.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := <-copyDone; err != nil {
		t.Fatal(err)
	}
	t.Logf("SILENT was gone when V's storage actor began to sync the batch of entries %d..%d", pendingLo.Load(), pendingHi.Load())
	// The live victim goes on and writes the batch: it is not the one that
	// is wrong, the state a crash leaves is.
	for deadline := time.Now().Add(10 * time.Second); ; {
		if _, last := victim.dd.Log().Bounds(); last >= oldLast {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the live victim never got the entries back")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// L fails; V crashes; G's link is whole again.
	c.net.Isolate(lead.id, true)
	lead.crash(t, c)
	victim.crash(t, c)
	trs[lagger.id].setDrop(nil)

	// V restarts from the crash state.
	_, silentAfter := os.Stat(filepath.Join(crashDir, fileSilent))
	restarted = &diskNode{dir: crashDir, id: victim.id}
	restarted.dd = silenceOpen(t, crashDir, run)
	_, crashLast := restarted.dd.Log().Bounds()
	t.Logf("crash state: log last %d (acknowledged through %d before the damage), SILENT present: %v", crashLast, oldLast, silentAfter == nil)
	if silentAfter == nil || crashLast >= oldLast {
		t.Logf("the crash state is safe: silent, or holding every acknowledged write")
		if err := restarted.dd.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Logf("the silence ended on disk before the entries: the crash state has no SILENT and stops at %d < %d", crashLast, oldLast)
	start(restarted)
	pair := []*diskNode{restarted, lagger}
	var nl *diskNode
	for deadline := time.Now().Add(8 * testTimingDisk().ElectionTimeoutMax); nl == nil && time.Now().Before(deadline); {
		for _, dn := range pair {
			if dn.node.Status().Role == coreconsensus.RoleLeader {
				nl = dn
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if nl == nil {
		t.Logf("no leader among the restarted victim and the lagger while the old leader is down: safe")
		return
	}
	tok = propose(t, nl.node, "after=loss")
	waitAppliedOnly(t, restarted, tok.Index)
	waitAppliedOnly(t, lagger, tok.Index)
	var lost []string
	for i := range 30 {
		key := fmt.Sprintf("ack%02d", i)
		if _, ok := nl.sm.get(key); !ok {
			lost = append(lost, key)
		}
	}
	if len(lost) > 0 {
		t.Fatalf("acknowledged writes lost: node %d (restarted victim %v) leads term %d and commits index %d; %d of 30 acknowledged writes are missing, first %s (index %d)",
			nl.id, nl == restarted, nl.node.Status().Term, tok.Index, len(lost), lost[0], acked[lost[0]])
	}
}
