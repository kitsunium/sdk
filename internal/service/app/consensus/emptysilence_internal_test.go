package consensus

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// #723, the variant #718 named and PR #721 left out: a founder whose log is
// damaged at its founding entry, before any snapshot was cut, took the
// TruncateAndStaySilent exit. The truncation dropped every entry, and the
// node started silent with no configuration at all: a learner of nothing,
// whose question of #639 and #718 went to no member and whose admission set
// was empty, so it refused the leader that still lists it as a voter at
// every handshake. It never ended its silence nor voted again, and the
// cluster ran one failure from having no leader, for good. The exit is now
// refused while the operator is at the console, the directory unchanged,
// and the refusal names WipeAndRejoin. Every directory, the runtime one
// included, is in t.TempDir(); where flock(2) is missing a data directory is
// refused, and the tests skip (silenceOpen).

// emptySilenceFresh models the transport of a restarted process: a TCP
// transport starts with no admitted member (tcp.go), and the memory double,
// reused across restarts, would keep the admission of the crashed process.
func emptySilenceFresh(c *cluster, id coreconsensus.NodeID) {
	tr := NewMemoryTransport(c.net, id)
	tr.mu.Lock()
	tr.groups, tr.members = nil, nil
	tr.mu.Unlock()
}

// emptySilenceDamage flips a payload byte of the record of entry index in
// the victim's first segment: synced, complete, with valid records after it.
func emptySilenceDamage(t *testing.T, victim *diskNode, index uint64) {
	t.Helper()
	segs := must(filepath.Glob(filepath.Join(victim.dir, "log", "seg-????????????????")))(t)
	if len(segs) == 0 {
		t.Fatal("setup: the victim has no published segment")
	}
	first := segs[0]
	b, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	for o := 0; o < len(b); {
		rec, ok := parseRecord(b[o:])
		if !ok {
			break
		}
		if rec.kind == recEntry && rec.index == index && len(rec.data) > 0 {
			b[o+recHeader+recFixed] ^= 0xff
			if err := os.WriteFile(first, b, 0o600); err != nil {
				t.Fatal(err)
			}
			return
		}
		o += rec.length
	}
	t.Fatalf("setup: no entry %d with a payload in %s", index, filepath.Base(first))
}

// emptySilenceCase is a founder of three crashed after writes commands,
// its log damaged at entry damaged and quarantined, with what restarts it.
type emptySilenceCase struct {
	c         *cluster
	a, b      *diskNode
	victim    *diskNode
	run       string
	writes    int
	damaged   uint64
	snapshots bool
}

// emptySilenceQuarantined runs three founders, writes x.writes commands,
// crashes the victim, damages its entry x.damaged and reopens its directory
// quarantined. With x.snapshots the victim must hold a snapshot by then.
func emptySilenceQuarantined(t *testing.T, x *emptySilenceCase) {
	t.Helper()
	net := NewMemoryNetwork()
	net.admission = true
	c := &cluster{t: t, net: net, cid: coreconsensus.ClusterID{5, 72, 3}, timing: testTimingDisk()}
	x.c, x.run = c, t.TempDir()
	var founders []*diskNode
	var members []coreconsensus.MemberValue
	for i := range 3 {
		dn := &diskNode{dir: t.TempDir()}
		dn.dd = silenceOpen(t, dn.dir, x.run)
		t.Cleanup(func() {
			if dn.node != nil {
				dn.crash(t, c)
			}
		})
		slot := fmt.Sprintf("f%d", i)
		id, inc, err := Init(dn.dd, c.cid, slot)
		if err != nil {
			t.Fatal(err)
		}
		dn.id = id
		founders = append(founders, dn)
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: MemorySPKI(id), Voter: true})
	}
	for _, dn := range founders {
		dn.sm = newKV()
		if err := Bootstrap(context.Background(), new(diskConfig(c, dn)), members); err != nil {
			t.Fatal(err)
		}
		if err := dn.dd.Close(); err != nil {
			t.Fatal(err)
		}
		dn.dd = silenceOpen(t, dn.dir, x.run)
		x.start(t, dn)
	}
	x.a, x.b, x.victim = founders[0], founders[1], founders[2]
	for i := range x.writes {
		proposeOnDisk(t, founders, fmt.Sprintf("k%d=v%d", i, i))
	}
	if x.victim.node.Status().Role == coreconsensus.RoleLeader {
		retryAdmin(t, "move the leadership off the victim", func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return x.victim.node.TransferLeadership(ctx, "op:test", x.a.id)
		})
	}
	c.waitAppliedDisk(founders, proposeOnDisk(t, founders, "before=crash").Index)
	snaps, _ := filepath.Glob(filepath.Join(x.victim.dir, "snap", "*"))
	if (len(snaps) != 0) != x.snapshots {
		t.Fatalf("setup: the victim holds snapshot files %v, want some: %v", snaps, x.snapshots)
	}
	x.victim.crash(t, c)
	emptySilenceDamage(t, x.victim, x.damaged)
	x.victim.dd = silenceOpen(t, x.victim.dir, x.run)
	// Registered after the victim's TempDir, so it runs before the
	// directory is removed, which Windows refuses while its LOCK is held: a
	// victim never started again keeps its directory open, and one started
	// is crashed by the cleanup above, which closes it.
	t.Cleanup(func() {
		if x.victim.node == nil {
			if err := x.victim.dd.Close(); err != nil {
				t.Errorf("close the victim's data directory: %v", err)
			}
		}
	})
	x.victim.sm = newKV()
	if _, err := NewNode(diskConfig(c, x.victim)); !errors.Is(err, coreconsensus.LogQuarantined) {
		t.Fatalf("damaged log: %v", err)
	}
}

func (x *emptySilenceCase) start(t *testing.T, dn *diskNode) {
	t.Helper()
	dn.sm = newKV()
	n, err := NewNode(diskConfig(x.c, dn))
	if err != nil {
		t.Fatalf("NewNode %d: %v", dn.id, err)
	}
	dn.node = n
}

func (x *emptySilenceCase) silent() bool {
	_, err := os.Stat(filepath.Join(x.victim.dir, fileSilent))
	return err == nil
}

// rejoins restarts the silent victim on a fresh transport and waits for it
// to end its silence and apply a write of the leader.
func (x *emptySilenceCase) rejoins(t *testing.T) {
	t.Helper()
	if !x.silent() {
		t.Fatal("setup: no silence condition persisted")
	}
	emptySilenceFresh(x.c, x.victim.id)
	x.start(t, x.victim)
	l := leaderOf(t, []*diskNode{x.a, x.b})
	tok := propose(t, l.node, "under=leader")
	wait := 20 * testTimingDisk().ElectionTimeoutMax
	for deadline := time.Now().Add(wait); x.silent() || x.victim.node.Status().Applied < tok.Index; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the silent founder never rejoined in %v: silent %v, %+v, configuration %+v", wait, x.silent(), x.victim.node.Status(), x.victim.node.Membership())
		}
	}
}

// segmentDigests is the digest of every file of dir's log directory.
func segmentDigests(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	for _, p := range must(filepath.Glob(filepath.Join(dir, "log", "*")))(t) {
		out[filepath.Base(p)] = sha256.Sum256(must(os.ReadFile(p))(t))
	}
	return out
}

// TestTheTruncationThatLeavesNoConfigurationIsRefused: the damage is at
// the founding entry and no snapshot was cut. TruncateAndStaySilent is
// refused RECOVER_REFUSED naming WipeAndRejoin, with the log, the quarantine
// marker and the absent silence condition as they were; WipeAndRejoin, the
// exit the refusal names, still runs.
func TestTheTruncationThatLeavesNoConfigurationIsRefused(t *testing.T) {
	x := &emptySilenceCase{writes: 8, damaged: 1}
	emptySilenceQuarantined(t, x)
	before := segmentDigests(t, x.victim.dir)
	err := ClearQuarantine(x.victim.dd, TruncateAndStaySilent, "op:silent")
	if !errors.Is(err, coreconsensus.RecoverRefused) || !strings.Contains(fieldOf(err, "why"), "WipeAndRejoin") {
		t.Fatalf("TruncateAndStaySilent over a truncation that leaves no configuration: %v (why %q), want RECOVER_REFUSED naming WipeAndRejoin", err, fieldOf(err, "why"))
	}
	if x.silent() {
		t.Fatal("the refused exit persisted a silence condition")
	}
	if _, err := os.Stat(filepath.Join(x.victim.dir, markerQuarantine)); err != nil {
		t.Fatalf("the refused exit removed the quarantine marker: %v", err)
	}
	after := segmentDigests(t, x.victim.dir)
	if len(after) != len(before) {
		t.Fatalf("the refused exit changed the log's files: %d, was %d", len(after), len(before))
	}
	for name, d := range before {
		if after[name] != d {
			t.Fatalf("the refused exit changed %s", name)
		}
	}
	if err := ClearQuarantine(x.victim.dd, WipeAndRejoin, "op:wipe"); err != nil {
		t.Fatalf("WipeAndRejoin after the refusal: %v", err)
	}
}

// TestTheSilentFounderWithItsConfigurationRejoins: the same with the damage
// past the founding entry. The truncation keeps the founding
// configuration, which lists the leader, and the founder rejoins.
func TestTheSilentFounderWithItsConfigurationRejoins(t *testing.T) {
	x := &emptySilenceCase{writes: 8, damaged: 4}
	emptySilenceQuarantined(t, x)
	if err := ClearQuarantine(x.victim.dd, TruncateAndStaySilent, "op:silent"); err != nil {
		t.Fatal(err)
	}
	x.rejoins(t)
}

// TestTheTruncationOverASnapshotKeepsItsConfiguration: the damage is at the
// founding entry, but a snapshot was cut. The truncation keeps the
// snapshot, which names the members, and is taken; the founder rejoins.
func TestTheTruncationOverASnapshotKeepsItsConfiguration(t *testing.T) {
	x := &emptySilenceCase{writes: 60, damaged: 1, snapshots: true}
	emptySilenceQuarantined(t, x)
	if err := ClearQuarantine(x.victim.dd, TruncateAndStaySilent, "op:silent"); err != nil {
		t.Fatalf("TruncateAndStaySilent over a snapshot: %v (why %q)", err, fieldOf(err, "why"))
	}
	x.rejoins(t)
}
