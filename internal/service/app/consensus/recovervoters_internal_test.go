package consensus

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// freshDisk is an empty data directory Init prepared for cluster cid under
// slot, closed at the end of the test when no node holds it.
func freshDisk(t *testing.T, c *cluster, cid coreconsensus.ClusterID, slot string) (*diskNode, coreconsensus.MemberValue) {
	t.Helper()
	dn := &diskNode{dir: t.TempDir()}
	dn.dd = openDir(t, dn.dir, 16<<10)
	t.Cleanup(func() {
		if dn.node != nil {
			dn.crash(t, c)
			return
		}
		if err := dn.dd.Close(); err != nil {
			t.Errorf("close data directory %d: %v", dn.id, err)
		}
	})
	id, inc, err := Init(dn.dd, cid, slot)
	if err != nil {
		t.Fatal(err)
	}
	dn.id = id
	return dn, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: MemorySPKI(id), Voter: true}
}

// startOnDisk starts dn under c's cluster, a voter alone enough for a
// majority.
func startOnDisk(t *testing.T, c *cluster, dn *diskNode) {
	t.Helper()
	dn.sm = newKV()
	cfg := diskConfig(c, dn)
	cfg.MinVoters = 1
	dn.node = must(NewNode(cfg))(t)
}

// ADR 0152 R3-02 and R4-04: the nodes a Recover names besides the survivor
// "enter as LEARNERS, and are promoted through the normal Promote catch-up,
// never as empty voters". Recover accepted as a voter a member Init only
// prepared: its node cannot vote before a leader tells it of its own
// admission, the survivor needs its vote for a majority, no leader was
// ever elected, and nothing online repaired it (#642). It is refused
// RECOVER_REFUSED before anything is written; named a learner, the same
// member joins a cluster the survivor leads alone.
func TestRecoverRefusesAMemberInitPreparedAsAVoter(t *testing.T) {
	c, nodes := threeOnDisk(t, 20)
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	keep := nodes[0]
	keep.dd = openDir(t, keep.dir, 16<<10)
	next := coreconsensus.ClusterID{23}
	joiner, j := freshDisk(t, c, next, "joiner")
	members, opt := recoverArgs(t, c, nodes, keep, next)
	if err := Recover(keep.dd, next, append(slices.Clone(members), j), opt); !errors.Is(err, coreconsensus.RecoverRefused) {
		t.Fatalf("Recover naming the Init'ed joiner a voter: %v, want RECOVER_REFUSED", err)
	}
	if st := must(keep.dd.Log().Stamp())(t); st.Log.Cluster != c.cid {
		t.Fatalf("the refused Recover moved the stamp to %v", st.Log.Cluster)
	}
	j.Voter = false
	if err := Recover(keep.dd, next, append(members, j), opt); err != nil {
		t.Fatalf("Recover naming the joiner a learner: %v", err)
	}
	c.cid = next
	startOnDisk(t, c, keep)
	startOnDisk(t, c, joiner)
	waitFor(t, "the survivor leads the recovered cluster", func() bool {
		return keep.node.Status().Role == coreconsensus.RoleLeader
	})
}

// Two survivors that each name the other a voter of their Recover each ran
// it into the same cluster id: neither needed a fence for the other, a
// member, and two histories carried one identity, each having committed its
// own forced entry alone, different entries at one (index, term) (#642).
// Both Recovers are refused now.
func TestTwoSurvivorsNamingEachOtherAVoterAreRefused(t *testing.T) {
	c, nodes := threeOnDisk(t, 20)
	n0, n1, n2 := nodes[0], nodes[1], nodes[2]
	n1.crash(t, c)
	var tok coreconsensus.TokenValue
	for i := range 15 {
		tok = proposeOnDisk(t, []*diskNode{n0, n2}, fmt.Sprintf("late%d=v%d", i, i))
	}
	c.waitAppliedDisk([]*diskNode{n0, n2}, tok.Index)
	n0.crash(t, c)
	n2.crash(t, c)
	next := coreconsensus.ClusterID{24}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, dn := range []*diskNode{n0, n1} {
		dn.dd = openDir(t, dn.dir, 16<<10)
		t.Cleanup(func() {
			if err := dn.dd.Close(); err != nil {
				t.Errorf("close data directory %d: %v", dn.id, err)
			}
		})
	}
	member := func(dn *diskNode) coreconsensus.MemberValue {
		st := must(dn.dd.Log().Stamp())(t)
		return coreconsensus.MemberValue{ID: dn.id, Incarnation: st.Incarnation, Slot: st.Slot, SPKI: MemorySPKI(dn.id), Voter: true}
	}
	members := []coreconsensus.MemberValue{member(n0), member(n1)}
	for _, keep := range []*diskNode{n0, n1} {
		opt := RecoverOptions{Confirm: next.String(), Operator: "op:recover", WitnessKeys: []ed25519.PublicKey{pub}}
		opt.Fenced = []coreconsensus.FencedAttestationValue{{
			Voter: n2.id, Method: "powered-off", Receipts: [][]byte{ed25519.Sign(priv, ReceiptMessage(c.cid, next, n2.id))},
		}}
		if err := Recover(keep.dd, next, members, &opt); !errors.Is(err, coreconsensus.RecoverRefused) {
			t.Fatalf("Recover on %d naming the other survivor a voter: %v, want RECOVER_REFUSED", keep.id, err)
		}
		if st := must(keep.dd.Log().Stamp())(t); st.Log.Cluster != c.cid {
			t.Fatalf("the refused Recover on %d moved the stamp to %v", keep.id, st.Log.Cluster)
		}
	}
}

// RestoreSnapshot is run on EVERY node of the new cluster, with the same
// snapshot, cluster id and member list (ADR 0152 R2-26, R3-15): each voter
// it names holds the snapshot, none is empty, and the restored nodes elect
// a leader and serve the restored state, then start again cold (R4-15).
// The guard #642 adds to Recover does not apply to it: a restore names
// every voter.
func TestARestoreOnEveryNodeWithTheSameMembersElects(t *testing.T) {
	c, nodes := threeOnDisk(t, 20)
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	meta, err := nodes[0].node.SaveSnapshot(ctx, "op:save", &buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	next := coreconsensus.ClusterID{25}
	var fresh []*diskNode
	var members []coreconsensus.MemberValue
	for i := range 3 {
		dn, m := freshDisk(t, c, next, fmt.Sprintf("n%d", i))
		fresh, members = append(fresh, dn), append(members, m)
	}
	for _, dn := range fresh {
		opt := RestoreOptions{Expected: meta, Confirm: next.String(), Operator: "op:restore", MaxEncoded: 1 << 30, MaxDecoded: 1 << 30}
		if _, err := RestoreSnapshot(dn.dd, bytes.NewReader(buf.Bytes()), next, members, &opt); err != nil {
			t.Fatalf("restore on %d: %v", dn.id, err)
		}
	}
	c.cid = next
	for round := range 2 {
		for _, dn := range fresh {
			if round > 0 {
				dn.dd = openDir(t, dn.dir, 16<<10)
			}
			dn.sm = newKV()
			dn.node = must(NewNode(diskConfig(c, dn)))(t)
		}
		tok := proposeOnDisk(t, fresh, fmt.Sprintf("after=r%d", round))
		c.waitAppliedDisk(fresh, tok.Index)
		for _, dn := range fresh {
			if v, _ := dn.sm.get("k19"); v != "v19" {
				t.Fatalf("round %d: node %d holds k19=%q", round, dn.id, v)
			}
			dn.crash(t, c)
		}
	}
}
