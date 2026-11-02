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
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// #758, the remainder #603 and #723 left: a founder V stays up while every
// other founder is replaced, D and E joining and promoted, A and B failing
// and removed. V acknowledged each change, so #603's guard (removalStrands,
// reachedBy) let every removal through. V's log is then found damaged at a
// record before D's admission, and TruncateAndStaySilent, which #723's guard
// let through since the founding configuration {A, B, V} survives, brought V
// back over that configuration: its question (#718) went to A and B, gone,
// its transport refused D and E, and it stayed silent for good, the cluster
// {V, D, E} one failure from two live voters of three with no leader. The
// exit is now refused while the operator is at the console. Every
// directory, the runtime one included, is in t.TempDir(); D, E and every
// later joiner run on MemoryLog, and where flock(2) is missing a data
// directory is refused and the tests skip (silenceOpen).

// replacedCase is V, a founder of {A, B, V} that saw D and E join and be
// promoted and A, and B with removeB, be removed, crashed, its log damaged
// and quarantined.
type replacedCase struct {
	c       *cluster
	run     string
	a, b, v *diskNode
	joined  []*testNode
	ctx     context.Context
}

func (x *replacedCase) cfg(dn *diskNode) Config {
	cfg := diskConfig(x.c, dn)
	cfg.JoinAuthorizer = allowJoin{}
	return cfg
}

func (x *replacedCase) start(t *testing.T, dn *diskNode) {
	t.Helper()
	dn.sm = newKV()
	n, err := NewNode(x.cfg(dn))
	if err != nil {
		t.Fatalf("NewNode %d: %v", dn.id, err)
	}
	dn.node = n
}

// live is every node running.
func (x *replacedCase) live() []*Node {
	var out []*Node
	for _, dn := range []*diskNode{x.a, x.b, x.v} {
		if dn.node != nil {
			out = append(out, dn.node)
		}
	}
	for _, tn := range x.joined {
		if tn.node != nil {
			out = append(out, tn.node)
		}
	}
	return out
}

func (x *replacedCase) leader(t *testing.T) *Node {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		for _, n := range x.live() {
			if n.Status().Role == coreconsensus.RoleLeader {
				return n
			}
		}
	}
	t.Fatal("setup: no leader")
	return nil
}

// joinVoter joins a node under slot and promotes it.
func (x *replacedCase) joinVoter(t *testing.T, slot string) *testNode {
	t.Helper()
	tn := x.c.join(t, slot)
	x.joined = append(x.joined, tn)
	t.Cleanup(func() {
		if tn.node != nil {
			_ = tn.node.HardClose()
		}
	})
	retryAdmin(t, "promote "+slot, func() error { return x.leader(t).Promote(x.ctx, "op:test", tn.id) })
	return tn
}

// propose commits kv on whichever live node leads.
func (x *replacedCase) propose(t *testing.T, kv string) coreconsensus.TokenValue {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		for _, n := range x.live() {
			if n.Status().Role != coreconsensus.RoleLeader {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			tok, _, err := n.Propose(ctx, []byte(kv))
			cancel()
			if err == nil {
				return tok
			}
		}
	}
	t.Fatalf("propose %q: no leader committed it", kv)
	return coreconsensus.TokenValue{}
}

func (x *replacedCase) silent() bool {
	_, err := os.Stat(filepath.Join(x.v.dir, fileSilent))
	return err == nil
}

// replacedQuarantined runs the history up to V's quarantine: the founders
// {A, B, V}, D and E joined and promoted, A, and B with removeB, crashed
// and removed, V crashed once it applied the removals, its log damaged by
// damage and its directory reopened quarantined.
func replacedQuarantined(t *testing.T, removeB bool, damage func(*testing.T, *diskNode)) *replacedCase {
	t.Helper()
	net := NewMemoryNetwork()
	net.admission = true
	x := &replacedCase{c: &cluster{t: t, net: net, cid: coreconsensus.ClusterID{5, 75, 8}, timing: testTimingDisk()}, run: t.TempDir()}
	// Every joiner keeps the founders' floor, the shipped default: the
	// removal of V from {V, D, E} is refused below it.
	x.c.configure = func(_ *testNode, cfg *Config) { cfg.MinVoters = 3 }
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	x.ctx = ctx
	var founders []*diskNode
	var members []coreconsensus.MemberValue
	for i := range 3 {
		dn := &diskNode{dir: t.TempDir()}
		dn.dd = silenceOpen(t, dn.dir, x.run)
		t.Cleanup(func() {
			if dn.node != nil {
				dn.crash(t, x.c)
			}
		})
		slot := fmt.Sprintf("f%d", i)
		id, inc, err := Init(dn.dd, x.c.cid, slot)
		if err != nil {
			t.Fatal(err)
		}
		dn.id = id
		founders = append(founders, dn)
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: MemorySPKI(id), Voter: true})
	}
	for _, dn := range founders {
		dn.sm = newKV()
		if err := Bootstrap(context.Background(), new(x.cfg(dn)), members); err != nil {
			t.Fatal(err)
		}
		if err := dn.dd.Close(); err != nil {
			t.Fatal(err)
		}
		dn.dd = silenceOpen(t, dn.dir, x.run)
		x.start(t, dn)
	}
	x.a, x.b, x.v = founders[0], founders[1], founders[2]
	for i := range 3 {
		proposeOnDisk(t, founders, fmt.Sprintf("k%d=v%d", i, i))
	}
	x.joinVoter(t, "slot-d")
	x.joinVoter(t, "slot-e")
	// A dead voter is always removable (#603), and V, which acknowledged
	// everything, holds the newest configuration.
	gone := []*diskNode{x.a}
	if removeB {
		gone = append(gone, x.b)
	}
	for _, g := range gone {
		g.crash(t, x.c)
		retryAdmin(t, fmt.Sprintf("remove %d", g.id), func() error { return x.leader(t).Remove(x.ctx, "op:test", g.id) })
	}
	tok := x.propose(t, "after=removals")
	for deadline := time.Now().Add(10 * time.Second); x.v.node.Status().Applied < tok.Index; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("setup: V did not apply the removals: %+v", x.v.node.Status())
		}
	}
	conf := x.v.node.Membership()
	for _, tn := range x.joined {
		if _, ok := conf.Member(tn.id); !ok {
			t.Fatalf("setup: V's configuration %+v does not list %d", conf, tn.id)
		}
	}
	x.v.crash(t, x.c)
	damage(t, x.v)
	x.v.dd = silenceOpen(t, x.v.dir, x.run)
	// Registered after V's TempDir, so it runs before the directory is
	// removed, which Windows refuses while its LOCK is held.
	t.Cleanup(func() {
		if x.v.node == nil {
			if err := x.v.dd.Close(); err != nil {
				t.Errorf("close V's data directory: %v", err)
			}
		}
	})
	x.v.sm = newKV()
	if _, err := NewNode(x.cfg(x.v)); !errors.Is(err, coreconsensus.LogQuarantined) {
		t.Fatalf("damaged log: %v", err)
	}
	return x
}

// rejoins clears V's quarantine with TruncateAndStaySilent, restarts it and
// waits for it to end its silence and apply a write of the leader.
func (x *replacedCase) rejoins(t *testing.T) {
	t.Helper()
	if err := ClearQuarantineWith(x.ctx, x.v.dd, TruncateAndStaySilent, "op:silent", askingFrom(x.c, x.v.id)); err != nil {
		t.Fatalf("TruncateAndStaySilent: %v (why %q)", err, fieldOf(err, "why"))
	}
	if !x.silent() {
		t.Fatal("setup: no silence condition persisted")
	}
	emptySilenceFresh(x.c, x.v.id)
	x.start(t, x.v)
	tok := x.propose(t, "under=new")
	wait := 4*testTimingDisk().ElectionTimeoutMax + 5*time.Second
	for deadline := time.Now().Add(wait); x.silent() || x.v.node.Status().Applied < tok.Index; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("V never rejoined in %v: silent %v, %+v, configuration %+v", wait, x.silent(), x.v.node.Status(), x.v.node.Membership())
		}
	}
}

// replacedDamageFirstCommand flips a payload byte of the first command
// entry of victim's lowest segment: synced, past the founding batch and
// before any later configuration entry, with valid records after it.
func replacedDamageFirstCommand(t *testing.T, victim *diskNode) {
	t.Helper()
	segs := must(filepath.Glob(filepath.Join(victim.dir, "log", "seg-????????????????")))(t)
	if len(segs) == 0 {
		t.Fatal("setup: the victim has no published segment")
	}
	slices.Sort(segs)
	b, err := os.ReadFile(segs[0])
	if err != nil {
		t.Fatal(err)
	}
	for o := 0; o < len(b); {
		rec, ok := parseRecord(b[o:])
		if !ok {
			break
		}
		if rec.kind == recEntry && rec.ekind == uint8(coreconsensus.EntryCommand) && len(rec.data) > 0 {
			b[o+recHeader+recFixed] ^= 0xff
			if err := os.WriteFile(segs[0], b, 0o600); err != nil {
				t.Fatal(err)
			}
			return
		}
		o += rec.length
	}
	t.Fatal("setup: no command entry in the lowest segment")
}

// TestATruncationBackToMembersAllRemovedSinceIsRefused is the issue's
// scenario: the truncation would keep {A, B, V} and drop {V, D, E}. It is
// refused RECOVER_REFUSED, the directory unchanged, naming WipeAndRejoin,
// the removal that frees V's slot and the promotion MinVoters asks for
// first, and that way out runs: V's old id is not removable from three
// voters, F joins and is promoted, V's old id is removed, and V's slot
// takes a JOIN again (R8-11).
func TestATruncationBackToMembersAllRemovedSinceIsRefused(t *testing.T) {
	x := replacedQuarantined(t, true, replacedDamageFirstCommand)
	silenceRefused(t, x.v.dd, x.v.dir, "Remove", "Promote")
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		err := x.leader(t).Remove(x.ctx, "op:test", x.v.id)
		if errors.Is(err, coreconsensus.MembershipInvalid) {
			break
		}
		if err == nil || time.Now().After(deadline) {
			t.Fatalf("removing V from three voters: %v, want MEMBERSHIP_INVALID under MinVoters", err)
		}
	}
	x.joinVoter(t, "slot-f")
	retryAdmin(t, "remove V", func() error { return x.leader(t).Remove(x.ctx, "op:test", x.v.id) })
	x.joined = append(x.joined, x.c.join(t, "f2"))
}

// TestATruncationThatKeepsTheNewestConfigurationRejoins is the control: the
// damage at the record before the last keeps {V, D, E}, and V rejoins.
func TestATruncationThatKeepsTheNewestConfigurationRejoins(t *testing.T) {
	x := replacedQuarantined(t, true, damageBeforeLastRecord)
	x.rejoins(t)
}

// TestATruncationBackToAMemberStillListedRejoins: the same damage as the
// issue's, but B was never removed. The truncation drops several
// configuration entries, the newest {B, V, D, E}, which still lists B: the
// exit is taken, V asks B for the leader's record and rejoins.
func TestATruncationBackToAMemberStillListedRejoins(t *testing.T) {
	x := replacedQuarantined(t, false, replacedDamageFirstCommand)
	x.rejoins(t)
}

// replacedRecord is the record of entry index at term 1: a configuration
// entry of conf, a command when conf is nil.
func replacedRecord(index uint64, conf *coreconsensus.MembershipValue) []byte {
	r := &record{kind: recEntry, batch: index, term: 1, index: index, end: true, ekind: uint8(coreconsensus.EntryCommand), data: []byte("x")}
	if conf != nil {
		r.ekind = uint8(coreconsensus.EntryMembership)
		r.data = wire.AppendAdmin(nil, &wire.AdminValue{Operator: "op:test", Change: wire.ChangeAddLearner, Membership: *conf})
	}
	return appendRecord(nil, r)
}

// TestTheStrandingTruncationIsJudgedOnTheLogsNewestConfiguration holds the
// judgment's edges on records written in place, V the node: a lone node, a
// kept configuration that lists V alone, learners, a joining member, a
// withdrawn key, a kept configuration that does not list V, a snapshot that
// carries the configuration, several configurations dropped, the newest
// one a later truncation or entry took out, damage before it, and one that
// does not parse.
func TestTheStrandingTruncationIsJudgedOnTheLogsNewestConfiguration(t *testing.T) {
	const v, a, b, d, e, j = 1, 2, 3, 4, 5, 6
	member := func(id coreconsensus.NodeID, voter bool) coreconsensus.MemberValue {
		return coreconsensus.MemberValue{ID: id, SPKI: MemorySPKI(id), Voter: voter}
	}
	conf := func(ms ...coreconsensus.MemberValue) *coreconsensus.MembershipValue {
		return &coreconsensus.MembershipValue{Members: ms}
	}
	voters := func(ids ...coreconsensus.NodeID) *coreconsensus.MembershipValue {
		c := conf()
		for _, id := range ids {
			c.Members = append(c.Members, member(id, true))
		}
		return c
	}
	withdrawn := voters(v, d)
	withdrawn.Members = append(withdrawn.Members, coreconsensus.MemberValue{ID: a, Voter: true})
	joining := member(j, false)
	joining.Joining = true
	damaged := replacedRecord(2, nil)
	damaged[recHeader+recFixed] ^= 0xff
	unparsed := appendRecord(nil, &record{kind: recEntry, batch: 2, term: 1, index: 2, end: true, ekind: uint8(coreconsensus.EntryMembership), data: []byte{0xff}})
	truncate := appendRecord(nil, &record{kind: recTruncate, batch: 9, index: 4, end: true})
	cat := func(bs ...[]byte) []byte { return slices.Concat(bs...) }
	for _, tc := range []struct {
		name          string
		snap          *coreconsensus.MembershipValue
		snapAt        uint64
		kept, dropped []byte
		want          bool
	}{
		{name: "nothing readable is dropped", kept: replacedRecord(1, voters(a, b, v)), dropped: replacedRecord(2, nil)},
		{name: "every other member removed", kept: replacedRecord(1, voters(a, b, v)), dropped: replacedRecord(2, voters(v, d, e)), want: true},
		{name: "one member still listed", kept: replacedRecord(1, voters(a, b, v)), dropped: replacedRecord(2, voters(b, v, d, e))},
		{name: "a lone node stays alone", kept: replacedRecord(1, voters(v)), dropped: replacedRecord(2, voters(v))},
		{name: "a kept configuration of V alone that others joined since", kept: replacedRecord(1, voters(v)), dropped: replacedRecord(2, voters(v, d, e)), want: true},
		{
			name: "a learner still listed", kept: replacedRecord(1, conf(member(a, true), member(b, true), member(v, true), member(d, false))),
			dropped: replacedRecord(2, conf(member(v, true), member(e, true), member(j, true), member(d, false))),
		},
		{
			name: "V a learner whose members were all removed", kept: replacedRecord(1, conf(member(a, true), member(b, true), member(v, false))),
			dropped: replacedRecord(2, conf(member(v, false), member(d, true), member(e, true))), want: true,
		},
		{
			name: "a joining member still listed, promoted since", kept: replacedRecord(1, conf(member(a, true), member(b, true), member(v, true), joining)),
			dropped: replacedRecord(2, voters(v, d, j)),
		},
		{name: "the one member left had its key withdrawn", kept: replacedRecord(1, voters(a, v)), dropped: replacedRecord(2, withdrawn), want: true},
		{name: "a kept configuration from before V's admission", kept: replacedRecord(1, voters(a, b)), dropped: replacedRecord(2, conf(member(a, true), member(b, true), member(v, false)))},
		{name: "a kept configuration from before V's admission, all removed", kept: replacedRecord(1, voters(a, b)), dropped: replacedRecord(2, voters(v, d, e)), want: true},
		{
			name: "several dropped, the newest decides", kept: replacedRecord(1, voters(a, b, v)),
			dropped: cat(replacedRecord(2, voters(a, b, v, d)), replacedRecord(3, voters(b, v, d)), replacedRecord(4, voters(v, d))), want: true,
		},
		{
			name: "the newest taken out by a later truncation", kept: replacedRecord(1, voters(a, b, v)),
			dropped: cat(replacedRecord(2, voters(a, b, v, d)), replacedRecord(3, voters(b, v, d)), replacedRecord(4, voters(v, d)), truncate),
		},
		{
			name: "the newest overwritten by a later entry", kept: replacedRecord(1, voters(a, b, v)),
			dropped: cat(replacedRecord(2, voters(b, v, d)), replacedRecord(3, voters(v, d)), replacedRecord(3, nil)),
		},
		{name: "a snapshot past what is dropped", snap: voters(v, d, e), snapAt: 5, kept: replacedRecord(1, voters(a, b, v)), dropped: replacedRecord(2, voters(v, d))},
		{name: "a snapshot that lists a member still listed", snap: voters(b, v), snapAt: 1, dropped: replacedRecord(2, voters(b, v, d))},
		{name: "a snapshot older than what is dropped", snap: voters(a, b, v), snapAt: 1, dropped: replacedRecord(2, voters(v, d)), want: true},
		{name: "damage before the newest", kept: replacedRecord(1, voters(a, b, v)), dropped: cat(damaged, replacedRecord(3, voters(v, d))), want: true},
		{name: "a dropped configuration that does not parse", kept: replacedRecord(1, voters(a, b, v)), dropped: unparsed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var metas []coreconsensus.SnapshotMetaValue
			if tc.snap != nil {
				metas = append(metas, coreconsensus.SnapshotMetaValue{Position: coreconsensus.PositionValue{Term: 1, Index: tc.snapAt}, Membership: *tc.snap})
			}
			seen := newTruncationConfs(v, metas)
			seen.read(tc.kept)
			seen.cut()
			seen.read(tc.dropped)
			if got := seen.strands(); got != tc.want {
				t.Fatalf("strands = %v, want %v (kept %+v, logged %+v)", got, tc.want, seen.kept, seen.logged)
			}
		})
	}
}
