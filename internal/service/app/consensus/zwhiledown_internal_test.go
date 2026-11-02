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
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// #763, what #764 left: the leader judges a voter on the configuration it
// last heard it at for one vote window after it stops answering, and #758's
// offline refusal reads only the node's own log. A founder V of {A, B, V}
// saw D and E join and be promoted, D lead and A be removed: its log ended
// on {B, V, D, E}. V crashed, its log damaged before D's admission, and B
// was removed a few milliseconds later, inside that window, then stopped
// for good. V's TruncateAndStaySilent kept {A, B, V}, and its newest
// readable configuration, {B, V, D, E}, still listed B, so the exit was
// taken: V restarted silent over {A, B, V}, asked A and B, both gone,
// refused D and E, and never ended its silence; once E failed, D and V, two
// live voters of three, committed nothing. The truncation now reads the
// cluster's configuration from a live member, D or E, which lists neither
// A nor B, and is refused before anything is written. The control damages
// the record before the last: V keeps {B, V, D, E}, whose D and E the live
// configuration still lists, and rejoins. Every directory, the runtime one
// included, is in t.TempDir(); D and E run on MemoryLog.

// TestATruncationBackToMembersRemovedWhileTheNodeWasDownIsRefused: with the
// damage before D's admission the exit is refused RECOVER_REFUSED and the
// directory stays quarantined; with the damage at the tail it is taken and
// V rejoins.
func TestATruncationBackToMembersRemovedWhileTheNodeWasDownIsRefused(t *testing.T) {
	for _, late := range []bool{false, true} {
		name := "damage-before-the-replacements"
		if late {
			name = "control-damage-at-the-tail"
		}
		t.Run(name, func(t *testing.T) { removedWhileDownTruncation(t, late) })
	}
}

func removedWhileDownTruncation(t *testing.T, late bool) {
	x := whileDownCase(t, coreconsensus.ClusterID{5, 76, 9})
	d := x.joined[0]
	// V crashes, its log damaged; B is removed at once, inside the vote
	// window D still judges V in, then decommissioned.
	x.v.crash(t, x.c)
	if late {
		damageBeforeLastRecord(t, x.v)
	} else {
		replacedDamageFirstCommand(t, x.v)
	}
	if st := d.node.Status(); st.Role != coreconsensus.RoleLeader {
		t.Fatalf("setup: D no longer leads before B's removal: %+v", st)
	}
	retryAdmin(t, "remove B while V is down", func() error { return d.node.Remove(x.ctx, "op:test", x.b.id) })
	x.b.crash(t, x.c)
	x.propose(t, "after=b")
	if _, ok := d.node.Membership().Member(x.b.id); ok {
		t.Fatalf("setup: the cluster's configuration %+v still lists B", d.node.Membership())
	}
	whileDownReopen(t, x)
	err := clearSilentAsking(x)
	if late {
		if err != nil {
			t.Fatalf("control: TruncateAndStaySilent: %v (why %q)", err, fieldOf(err, "why"))
		}
		x.restartsAndRejoins(t)
		return
	}
	if !errors.Is(err, coreconsensus.RecoverRefused) {
		t.Fatalf("TruncateAndStaySilent back to {A, B, V}, B removed while V was down and A before: %v, want RECOVER_REFUSED", err)
	}
	t.Logf("refused: %q", fieldOf(err, "why"))
	if x.silent() {
		t.Fatal("the refused truncation persisted a silence condition")
	}
	if _, err := os.Stat(filepath.Join(x.v.dir, markerQuarantine)); err != nil {
		t.Fatalf("the refused truncation cleared the quarantine: %v", err)
	}
}

// whileDownCase runs the history up to V's crash: the founders {A, B, V}, D
// and E joined and promoted, the leadership moved to D, and A removed while
// V runs, so that V's log ends on {B, V, D, E}.
func whileDownCase(t *testing.T, cid coreconsensus.ClusterID) *replacedCase {
	t.Helper()
	net := NewMemoryNetwork()
	net.admission = true
	x := &replacedCase{c: &cluster{t: t, net: net, cid: cid, timing: testTimingDisk()}, run: t.TempDir()}
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
	d := x.joinVoter(t, "slot-d")
	x.joinVoter(t, "slot-e")
	retryAdmin(t, "transfer to D", func() error { return x.leader(t).TransferLeadership(x.ctx, "op:test", d.id) })
	for deadline := time.Now().Add(10 * time.Second); d.node.Status().Role != coreconsensus.RoleLeader; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("setup: D never led: %+v", d.node.Status())
		}
	}
	x.a.crash(t, x.c)
	retryAdmin(t, "remove A", func() error { return d.node.Remove(x.ctx, "op:test", x.a.id) })
	tok := x.propose(t, "after=a")
	for deadline := time.Now().Add(10 * time.Second); x.v.node.Status().Applied < tok.Index; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("setup: V did not apply A's removal: %+v", x.v.node.Status())
		}
	}
	conf := x.v.node.Membership()
	if _, ok := conf.Member(x.a.id); ok {
		t.Fatalf("setup: V's configuration %+v still lists A", conf)
	}
	if _, ok := conf.Member(x.b.id); !ok {
		t.Fatalf("setup: V's configuration %+v does not list B", conf)
	}
	return x
}

// whileDownReopen reopens V's directory, which a start finds quarantined.
func whileDownReopen(t *testing.T, x *replacedCase) {
	t.Helper()
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
}

// clearSilentAsking clears V's quarantine with TruncateAndStaySilent, asking
// the live members through V's end of the network.
func clearSilentAsking(x *replacedCase) error {
	return ClearQuarantineWith(x.ctx, x.v.dd, TruncateAndStaySilent, "op:silent", askingFrom(x.c, x.v.id))
}

// askingFrom is the means to ask a live member that node id's
// TruncateAndStaySilent takes on c's memory network: id's own end of it.
func askingFrom(c *cluster, id coreconsensus.NodeID) *QuarantineOptions {
	return &QuarantineOptions{Ask: NewMemoryTransport(c.net, id).AskMembership}
}

// askingAnswer stands for live members that run with conf: every member
// conf lists answers it, under the key conf lists it with, and no other
// answers. A test whose subject is not the asking takes it while the
// victim's links are cut, or once its peers are down.
func askingAnswer(conf coreconsensus.MembershipValue) *QuarantineOptions {
	return &QuarantineOptions{Ask: func(_ context.Context, m coreconsensus.MemberValue) (coreconsensus.MembershipValue, error) {
		if !listsUnder(&conf, &m) {
			return coreconsensus.MembershipValue{}, origin(coreconsensus.PeerRejected, errs.String("why", "unanswered"))
		}
		return conf.Clone(), nil
	}}
}

// restartsAndRejoins restarts V, silent, and waits for it to end its silence
// and apply a write of the leader.
func (x *replacedCase) restartsAndRejoins(t *testing.T) {
	t.Helper()
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

// TestATruncationNoLiveMemberAnswersIsRefused: a founder damaged past its
// founding entry keeps {A, B, V}, which A and B, live, still run with. The
// exit is refused before anything is written while no live member can be
// asked: without the means to ask one (ClearQuarantine), with V's links
// cut, with A and B both built before the read of the configuration (a
// mixed cluster), and over a configuration that no longer lists V. Once B,
// upgraded, answers, it is taken and V rejoins.
func TestATruncationNoLiveMemberAnswersIsRefused(t *testing.T) {
	x := &emptySilenceCase{writes: 8, damaged: 4}
	emptySilenceQuarantined(t, x)
	ctx := context.Background()
	refused := func(what, want string, err error) {
		t.Helper()
		if !errors.Is(err, coreconsensus.RecoverRefused) || fieldOf(err, "why") != want {
			t.Fatalf("%s: %v (why %q), want RECOVER_REFUSED %q", what, err, fieldOf(err, "why"), want)
		}
		if x.silent() {
			t.Fatalf("%s: the refused exit persisted a silence condition", what)
		}
		if _, err := os.Stat(filepath.Join(x.victim.dir, markerQuarantine)); err != nil {
			t.Fatalf("%s: the refused exit cleared the quarantine: %v", what, err)
		}
	}
	refused("no means to ask", whyNoAsk, ClearQuarantine(x.victim.dd, TruncateAndStaySilent, "op:silent"))
	x.c.net.Isolate(x.victim.id, true)
	err := ClearQuarantineWith(ctx, x.victim.dd, TruncateAndStaySilent, "op:silent", askingFrom(x.c, x.victim.id))
	refused("V cut off", whyNoLive, err)
	if asked := fieldOf(err, "asked"); asked != "2" {
		t.Fatalf("V cut off: asked %q members, want 2", asked)
	}
	x.c.net.Isolate(x.victim.id, false)
	predates := func(dn *diskNode, old bool) {
		tr := NewMemoryTransport(x.c.net, dn.id)
		tr.mu.Lock()
		tr.predatesMembership = old
		tr.mu.Unlock()
	}
	predates(x.a, true)
	predates(x.b, true)
	refused("A and B built before the read", whyNoLive,
		ClearQuarantineWith(ctx, x.victim.dd, TruncateAndStaySilent, "op:silent", askingFrom(x.c, x.victim.id)))
	conf := x.a.node.Membership()
	gone := conf.Clone()
	gone.Members = slices.DeleteFunc(gone.Members, func(m coreconsensus.MemberValue) bool { return m.ID == x.victim.id })
	refused("V no longer listed", whyNotListed,
		ClearQuarantineWith(ctx, x.victim.dd, TruncateAndStaySilent, "op:silent", askingAnswer(gone)))
	predates(x.b, false)
	if err := ClearQuarantineWith(ctx, x.victim.dd, TruncateAndStaySilent, "op:silent", askingFrom(x.c, x.victim.id)); err != nil {
		t.Fatalf("B answers: %v (why %q)", err, fieldOf(err, "why"))
	}
	x.rejoins(t)
}

// TestTheNewestLiveConfigurationIsJudged: of the members asked, the one
// that answers the configuration of the highest index rules; an answer
// that does not list the member asked under its key is set aside, as is
// every member once the context is done. The members asked are every
// other member the configurations read name, under its newest record, one
// whose newest record carries the zero key left out.
func TestTheNewestLiveConfigurationIsJudged(t *testing.T) {
	const v, a, b, d = coreconsensus.NodeID(3), coreconsensus.NodeID(1), coreconsensus.NodeID(2), coreconsensus.NodeID(4)
	member := func(id coreconsensus.NodeID, addr string) coreconsensus.MemberValue {
		return coreconsensus.MemberValue{ID: id, SPKI: MemorySPKI(id), Address: addr, Voter: true}
	}
	kept := coreconsensus.MembershipValue{Index: 1, Members: []coreconsensus.MemberValue{member(a, "a0"), member(b, "b0"), member(v, "v0")}}
	newer := coreconsensus.MembershipValue{Index: 7, Members: []coreconsensus.MemberValue{member(b, "b1"), member(v, "v0"), member(d, "d0")}}
	denied := member(a, "a1")
	denied.SPKI = [32]byte{}
	newest := coreconsensus.MembershipValue{Index: 9, Members: []coreconsensus.MemberValue{denied, member(v, "v0"), member(d, "d1")}}
	seen := &truncationConfs{self: v, kept: kept, keeps: true, logged: []loggedConf{{m: kept, index: 1}, {m: newer, index: 7}, {m: newest, index: 9}}}
	peers := seen.peers()
	if len(peers) != 2 || peers[0].ID != d || peers[0].Address != "d1" || peers[1].ID != b || peers[1].Address != "b1" {
		t.Fatalf("peers %+v, want D at d1 then B at b1, A, under the zero key in the newest record, not asked", peers)
	}
	answers := map[coreconsensus.NodeID]coreconsensus.MembershipValue{}
	ask := func(_ context.Context, m coreconsensus.MemberValue) (coreconsensus.MembershipValue, error) {
		c, ok := answers[m.ID]
		if !ok {
			return coreconsensus.MembershipValue{}, origin(coreconsensus.PeerRejected, errs.String("why", "unanswered"))
		}
		return c, nil
	}
	opt := &QuarantineOptions{Ask: ask}
	ctx := context.Background()
	// B lags on {B, V, D}, which lists B; D runs with {V, D}: D rules.
	answers[b] = newer
	answers[d] = coreconsensus.MembershipValue{Index: 12, Members: []coreconsensus.MemberValue{member(v, "v0"), member(d, "d1")}}
	if err := liveRefusal(ctx, seen, opt); !errors.Is(err, coreconsensus.RecoverRefused) {
		t.Fatalf("D's newer configuration lists neither A nor B: %v", err)
	}
	// D's answer does not list D: it is not D's, and B's rules.
	answers[d] = coreconsensus.MembershipValue{Index: 12, Members: []coreconsensus.MemberValue{member(v, "v0")}}
	if err := liveRefusal(ctx, seen, opt); err != nil {
		t.Fatalf("B's configuration still lists B: %v (why %q)", err, fieldOf(err, "why"))
	}
	// B answers under another key than the one V's log pins.
	rekeyed := newer.Clone()
	rekeyed.Members[0].SPKI = [32]byte{9}
	answers[b] = rekeyed
	if err := liveRefusal(ctx, seen, opt); fieldOf(err, "why") != whyNoLive {
		t.Fatalf("no member answered under its key: %v (why %q)", err, fieldOf(err, "why"))
	}
	answers[b] = newer
	done, cancel := context.WithCancel(ctx)
	cancel()
	if err := liveRefusal(done, seen, opt); fieldOf(err, "why") != whyNoLive {
		t.Fatalf("asked once the context was done: %v (why %q)", err, fieldOf(err, "why"))
	}
	if err := liveRefusal(ctx, nil, nil); err != nil {
		t.Fatalf("a directory that is not a member's: %v", err)
	}
}

// TestAMembersConfigurationIsReadOverTCPByAMemberOnly: V, down, asks
// through a transport built with its own certificate. A member answers the
// configuration it runs with, which lists it under its key; the read is
// refused under another pin than the member's, to a node of this cluster
// the membership does not list, by a member built before the lane (a mixed
// cluster, where the next member answers) and by a member whose node
// stopped.
func TestAMembersConfigurationIsReadOverTCPByAMemberOnly(t *testing.T) {
	ca, cid, ns := newTCPCluster(t)
	tcpLeader(t, ns)
	v := ns[2]
	v.stop(t)
	v.tr = nil
	asker := must(NewTCPTransport(&TransportConfig{Certificate: v.cert, Roots: ca.pool, ListenAddress: "127.0.0.1:0"}))(t)
	t.Cleanup(func() {
		if err := asker.Close(); err != nil {
			t.Errorf("close the asker: %v", err)
		}
	})
	record := func(tn *tcpNode) coreconsensus.MemberValue {
		return coreconsensus.MemberValue{ID: tn.id, Incarnation: tn.inc, Slot: tn.slot, SPKI: tn.spki, Address: tn.addr, Voter: true}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conf, err := asker.AskMembership(ctx, record(ns[0]))
	if err != nil {
		t.Fatalf("a member's read: %v", err)
	}
	for _, tn := range ns {
		if x, ok := conf.Member(tn.id); !ok || x.SPKI != tn.spki {
			t.Fatalf("the configuration read %+v does not list %d under its key", conf, tn.id)
		}
	}
	wrong := record(ns[0])
	wrong.SPKI = ns[1].spki
	if _, err := asker.AskMembership(ctx, wrong); err == nil {
		t.Fatal("read under another pin than the member's")
	}
	stranger := &tcpNode{slot: "site-9"}
	stranger.cert, _ = ca.issue(t, SAN(cid, stranger.slot), nil)
	stranger.listen(t, ca)
	defer stranger.tr.Close()
	if _, err := stranger.tr.AskMembership(ctx, record(ns[0])); err == nil {
		t.Fatal("a node the membership does not list read the configuration")
	}
	ns[0].tr.predatesMembership.Store(true)
	if _, err := asker.AskMembership(ctx, record(ns[0])); err == nil {
		t.Fatal("a member built before the lane answered")
	}
	got, ok := askNewest(ctx, asker.AskMembership, []coreconsensus.MemberValue{record(ns[0]), record(ns[1])})
	if !ok || !listsUnder(&got, new(record(ns[1]))) {
		t.Fatalf("mixed cluster: %+v %v, want the configuration of the member that answers", got, ok)
	}
	if err := ns[1].node.HardClose(); err != nil {
		t.Fatal(err)
	}
	ns[1].node = nil
	if _, err := asker.AskMembership(ctx, record(ns[1])); err == nil {
		t.Fatal("a member whose node stopped answered")
	}
}
