package consensus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// #718, end to end on the shipped layout: a founder down while D was
// admitted, promoted and handed the leadership, whose log is then found
// damaged, takes the TruncateAndStaySilent exit and comes back silent over
// a configuration that does not list D. Its transport refuses D (the memory
// transport's admission model, the TCP transport's rule), and a silent
// voter never campaigns (#524): it never sent the PreVote #542's record
// answers, #639's question was a learner's only, and it stayed silent for
// as long as D led, the cluster one failure from having no leader. It now
// asks, as a learner does. Every directory, the runtime directory
// included, is in t.TempDir(); where flock(2) is missing a data directory
// is refused, and the test skips (silenceOpen).
func TestTheSilentFounderRejoinsALeaderAdmittedWhileItWasDown(t *testing.T) {
	net := NewMemoryNetwork()
	net.admission = true
	c := &cluster{t: t, net: net, cid: coreconsensus.ClusterID{5, 71, 8}, timing: testTimingDisk()}
	run := t.TempDir()
	cfg := func(dn *diskNode) Config {
		cfg := diskConfig(c, dn)
		cfg.JoinAuthorizer = allowJoin{}
		return cfg
	}
	start := func(dn *diskNode) {
		dn.sm = newKV()
		n, err := NewNode(cfg(dn))
		if err != nil {
			t.Fatalf("NewNode %d: %v", dn.id, err)
		}
		dn.node = n
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
	a, b, victim := founders[0], founders[1], founders[2]
	for i := range 8 {
		proposeOnDisk(t, founders, fmt.Sprintf("k%d=v%d", i, i))
	}
	if victim.node.Status().Role == coreconsensus.RoleLeader {
		retryAdmin(t, "move the leadership off the victim", func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return victim.node.TransferLeadership(ctx, "op:test", a.id)
		})
	}
	c.waitAppliedDisk(founders, proposeOnDisk(t, founders, "before=crash").Index)
	victim.crash(t, c)

	// D joins while the victim is down, is promoted and takes over.
	d := c.join(t, "slot-d")
	t.Cleanup(func() {
		if d.node != nil {
			_ = d.node.HardClose()
		}
	})
	leader := func() *Node {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			for _, n := range []*Node{a.node, b.node, d.node} {
				if n.Status().Role == coreconsensus.RoleLeader {
					return n
				}
			}
		}
		t.Fatal("setup: no leader")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	retryAdmin(t, "promote D", func() error { return leader().Promote(ctx, "op:test", d.id) })
	retryAdmin(t, "transfer to D", func() error { return leader().TransferLeadership(ctx, "op:test", d.id) })
	// The transfer returns once its caller sees D lead. D sends its first
	// appends before it publishes its own role at the end of its turn, and
	// a loaded runner read D a candidate still (#751): D's own view decides.
	for deadline := time.Now().Add(10 * time.Second); d.node.Status().Role != coreconsensus.RoleLeader; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("setup: D does not lead: %+v", d.node.Status())
		}
	}

	// The victim's log is found damaged at its restart: the record before
	// the last of its last segment, past the founding configuration and
	// before D's admission, which it never received.
	damageBeforeLastRecord(t, victim)
	victim.dd = silenceOpen(t, victim.dir, run)
	victim.sm = newKV()
	if _, err := NewNode(cfg(victim)); !errors.Is(err, coreconsensus.LogQuarantined) {
		t.Fatalf("damaged log: %v", err)
	}
	if err := ClearQuarantineWith(context.Background(), victim.dd, TruncateAndStaySilent, "op:silent", askingAnswer(d.node.Membership())); err != nil {
		t.Fatal(err)
	}
	silent := func() bool {
		_, err := os.Stat(filepath.Join(victim.dir, fileSilent))
		return err == nil
	}
	if !silent() {
		t.Fatal("setup: no silence condition persisted")
	}
	start(victim)

	tok := propose(t, d.node, "under=d")
	wait := 4*testTimingDisk().ElectionTimeoutMax + 5*time.Second
	for deadline := time.Now().Add(wait); silent() || victim.node.Status().Applied < tok.Index; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the silent founder never rejoined D in %v: silent %v, %+v", wait, silent(), victim.node.Status())
		}
	}
	// One more failure: B, the victim and D are 3 of the 4 voters, and D
	// still commits.
	a.crash(t, c)
	propose(t, d.node, "after=a")
}

// damageBeforeLastRecord flips a payload byte of the record before the last
// of victim's last segment: synced data, a complete batch, a valid record
// after it.
func damageBeforeLastRecord(t *testing.T, victim *diskNode) {
	t.Helper()
	segs := must(filepath.Glob(filepath.Join(victim.dir, "log", "seg-????????????????")))(t)
	if len(segs) == 0 {
		t.Fatal("setup: the victim has no published segment")
	}
	last := segs[len(segs)-1]
	b, err := os.ReadFile(last)
	if err != nil {
		t.Fatal(err)
	}
	var offs []int
	for o := 0; o < len(b); {
		rec, ok := parseRecord(b[o:])
		if !ok {
			break
		}
		offs = append(offs, o)
		o += rec.length
	}
	if len(offs) < 3 {
		t.Fatalf("setup: %d records in the last segment", len(offs))
	}
	b[offs[len(offs)-2]+recHeader+recFixed] ^= 0xff
	if err := os.WriteFile(last, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
