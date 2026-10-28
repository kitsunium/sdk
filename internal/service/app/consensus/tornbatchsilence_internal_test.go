package consensus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// #745, what PR #743 left out of #723: the guard of TruncateAndStaySilent
// judged the bytes before the damage record by record, a configuration
// entry anywhere there passing it, while the reopen after the truncation
// drops the whole batch the damage tears (classifyTail, tornBatchStart),
// its records before the damage with it. The founding entry shares its
// batch with its hard state (bootstrap.go), and a joiner's first synced
// batch carries the log it replays from entry 1 (appendFor): a damage there
// kept the configuration entry in the guard's eyes, the reopen dropped it,
// and the node started silent with no configuration, a learner of nothing,
// for good. The guard now judges what the reopen keeps, and refuses. Every
// directory, the runtime one included, is in t.TempDir().

// damageAfterFoundingEntry flips a payload byte of the record that follows
// entry 1 in entry 1's own batch, in the first segment of dir's log: the
// founding batch's hard state for a founder, an entry of the log replayed
// from index 1 for a joiner. A log whose entry 1 ends its own batch has no
// such record, and the case skips. Every write stays under dir.
func damageAfterFoundingEntry(t *testing.T, dir string) {
	t.Helper()
	segs := must(filepath.Glob(filepath.Join(dir, "log", "seg-????????????????")))(t)
	if len(segs) == 0 {
		t.Fatal("setup: no published segment")
	}
	first := segs[0]
	b := must(os.ReadFile(first))(t)
	var founding *record
	for o := 0; o < len(b); {
		rec, ok := parseRecord(b[o:])
		if !ok {
			break
		}
		switch {
		case rec.kind == recEntry && rec.index == 1:
			if rec.end {
				t.Skip("entry 1 ends its own batch here: no record shares it")
			}
			founding = &rec
		case founding != nil && rec.batch == founding.batch && len(rec.data) > 0:
			t.Logf("damaging the record of kind %d, index %d, at offset %d: batch %d, entry 1's", rec.kind, rec.index, o, rec.batch)
			b[o+recHeader+recFixed] ^= 0xff
			if err := os.WriteFile(first, b, 0o600); err != nil {
				t.Fatal(err)
			}
			return
		}
		o += rec.length
	}
	t.Fatalf("setup: no record after entry 1 in its batch in %s", filepath.Base(first))
}

// TestATruncationThatTearsTheFoundingBatchIsRefused: the damage is at the
// hard state written in the founding entry's batch, no snapshot was cut.
// The founding entry lies before the damage, yet the reopen would drop it
// with the batch the damage tears: the exit is refused, the directory
// unchanged.
func TestATruncationThatTearsTheFoundingBatchIsRefused(t *testing.T) {
	x := &emptySilenceCase{writes: 8, damage: damageAfterFoundingEntry}
	emptySilenceQuarantined(t, x)
	silenceRefused(t, x.victim.dd, x.victim.dir)
}

// tornBatchJoiner founds three voters on disk, has a fourth, D, join them,
// replay the log from index 1 and be promoted, then crashes D. It returns
// D, its directory closed, the runtime directory and D's configuration.
func tornBatchJoiner(t *testing.T) (d *diskNode, run string, cfg func(*diskNode) Config) {
	t.Helper()
	net := NewMemoryNetwork()
	net.admission = true
	c := &cluster{t: t, net: net, cid: coreconsensus.ClusterID{5, 72, 10}, timing: testTimingDisk()}
	run = t.TempDir()
	cfg = func(dn *diskNode) Config {
		cfg := diskConfig(c, dn)
		cfg.JoinAuthorizer = allowJoin{}
		return cfg
	}
	start := func(dn *diskNode) {
		dn.sm = newKV()
		dn.node = must(NewNode(cfg(dn)))(t)
	}
	var founders []*diskNode
	var members []coreconsensus.MemberValue
	for i := range 3 {
		dn := &diskNode{dir: t.TempDir()}
		dn.dd = silenceOpen(t, dn.dir, run)
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
		if err := Bootstrap(context.Background(), new(cfg(dn)), members); err != nil {
			t.Fatal(err)
		}
		if err := dn.dd.Close(); err != nil {
			t.Fatal(err)
		}
		dn.dd = silenceOpen(t, dn.dir, run)
		start(dn)
	}
	for i := range 8 {
		proposeOnDisk(t, founders, fmt.Sprintf("k%d=v%d", i, i))
	}
	d = &diskNode{dir: t.TempDir()}
	d.dd = silenceOpen(t, d.dir, run)
	// Registered after D's TempDir, so it runs before the directory is
	// removed, which Windows refuses while its LOCK is held.
	t.Cleanup(func() {
		if d.node != nil {
			d.crash(t, c)
		} else if err := d.dd.Close(); err != nil {
			t.Errorf("close D's data directory: %v", err)
		}
	})
	id, _, err := Init(d.dd, c.cid, "slot-d")
	if err != nil {
		t.Fatal(err)
	}
	d.id = id
	start(d)
	st := must(d.dd.Log().Stamp())(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		if _, err := NewMemoryTransport(c.net, d.id).Join(ctx, coreconsensus.JoinConfig{Stamp: st, Address: "mem"}); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("setup: D never joined")
		}
		time.Sleep(50 * time.Millisecond)
	}
	all := append(slices.Clone(founders), d)
	retryAdmin(t, "promote D", func() error { return leaderOf(t, all).node.Promote(ctx, "op:test", d.id) })
	c.waitAppliedDisk(all, proposeOnDisk(t, founders, "before=crash").Index)
	if s := must(d.dd.Log().Stamp())(t); s.State != coreconsensus.StampMember {
		t.Fatalf("setup: D's stamp is %v, want member", s.State)
	}
	if d.node.Status().Role == coreconsensus.RoleLeader {
		retryAdmin(t, "move the leadership off D", func() error { return d.node.TransferLeadership(ctx, "op:test", founders[0].id) })
	}
	if snaps, _ := filepath.Glob(filepath.Join(d.dir, "snap", "*")); len(snaps) != 0 {
		t.Fatalf("setup: D holds snapshot files %v", snaps)
	}
	d.crash(t, c)
	return d, run, cfg
}

// TestATruncationThatTearsAJoinersFirstBatchIsRefused: D joined three
// founders with no snapshot cut, and replayed the log from index 1: its
// first synced batch carries entry 1, the founding configuration, and the
// entries after it. Damaged in that batch, past entry 1, D is quarantined;
// the reopen would drop the whole batch, and every configuration entry
// with it: the exit is refused, the directory unchanged.
func TestATruncationThatTearsAJoinersFirstBatchIsRefused(t *testing.T) {
	d, run, cfg := tornBatchJoiner(t)
	damageAfterFoundingEntry(t, d.dir)
	d.dd = silenceOpen(t, d.dir, run)
	d.sm = newKV()
	if _, err := NewNode(cfg(d)); !errors.Is(err, coreconsensus.LogQuarantined) {
		t.Fatalf("damaged log: %v", err)
	}
	silenceRefused(t, d.dd, d.dir)
}
