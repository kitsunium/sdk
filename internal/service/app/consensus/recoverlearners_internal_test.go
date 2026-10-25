package consensus

import (
	"crypto/ed25519"
	"errors"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// recoverFixture is three stopped nodes on disk whose first two are open
// for a Recover to next, and one witness key.
type recoverFixture struct {
	c          *cluster
	nodes      []*diskNode
	next       coreconsensus.ClusterID
	pub        ed25519.PublicKey
	priv       ed25519.PrivateKey
	stampsWere map[coreconsensus.NodeID]coreconsensus.StampValue
}

func newRecoverFixture(t *testing.T, next coreconsensus.ClusterID) *recoverFixture {
	t.Helper()
	c, nodes := threeOnDisk(t, 20)
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &recoverFixture{c: c, nodes: nodes, next: next, pub: pub, priv: priv, stampsWere: map[coreconsensus.NodeID]coreconsensus.StampValue{}}
	for _, dn := range nodes[:2] {
		dn.dd = openDir(t, dn.dir, 16<<10)
		t.Cleanup(func() {
			if err := dn.dd.Close(); err != nil {
				t.Errorf("close data directory %d: %v", dn.id, err)
			}
		})
		f.stampsWere[dn.id] = must(dn.dd.Log().Stamp())(t)
	}
	return f
}

// member is dn's member line, read from its stamp.
func (f *recoverFixture) member(t *testing.T, dn *diskNode, voter bool) coreconsensus.MemberValue {
	t.Helper()
	st := f.stampsWere[dn.id]
	if st.ID == 0 {
		st = must(dn.dd.Log().Stamp())(t)
	}
	return coreconsensus.MemberValue{ID: dn.id, Incarnation: st.Incarnation, Slot: st.Slot, SPKI: MemorySPKI(dn.id), Voter: voter}
}

// options are Recover's options with a receipt for each of fenced.
func (f *recoverFixture) options(fenced ...*diskNode) *RecoverOptions {
	opt := &RecoverOptions{Confirm: f.next.String(), Operator: "op:recover", WitnessKeys: []ed25519.PublicKey{f.pub}}
	for _, dn := range fenced {
		opt.Fenced = append(opt.Fenced, coreconsensus.FencedAttestationValue{
			Voter: dn.id, Method: "powered-off", Receipts: [][]byte{ed25519.Sign(f.priv, ReceiptMessage(f.c.cid, f.next, dn.id))},
		})
	}
	return opt
}

// refused asserts that Recover on keep was refused RECOVER_REFUSED before
// anything was written: its stamp is the one it had.
func (f *recoverFixture) refused(t *testing.T, keep *diskNode, err error, what string) {
	t.Helper()
	if !errors.Is(err, coreconsensus.RecoverRefused) {
		st := must(keep.dd.Log().Stamp())(t)
		t.Fatalf("Recover on %d %s: %v, want RECOVER_REFUSED (stamp now cluster %v epoch %d kept %d)", keep.id, what, err, st.Log.Cluster, st.Log.Epoch, st.Log.KeptIndex)
	}
	if st := must(keep.dd.Log().Stamp())(t); st != f.stampsWere[keep.id] {
		t.Fatalf("the refused Recover on %d moved the stamp to cluster %v", keep.id, st.Log.Cluster)
	}
}

// #642's fix refused another survivor named a voter, but checkFenced still
// exempted from its receipt every old voter the new members listed,
// whatever its role: two survivors that each named the other a LEARNER
// passed, neither needing the other's fence, and the two Recovers wrote two
// histories under one identity, each with its own single voter. Each
// elected itself and acknowledged writes alone; healed, they ran two
// leaders at one term, or one overwrote the other's acknowledged write
// (#691). Both Recovers are refused now, before anything is written.
func TestTwoSurvivorsNamingEachOtherALearnerAreRefused(t *testing.T) {
	f := newRecoverFixture(t, coreconsensus.ClusterID{26})
	n0, n1, n2 := f.nodes[0], f.nodes[1], f.nodes[2]
	for _, p := range [][2]*diskNode{{n0, n1}, {n1, n0}} {
		keep, other := p[0], p[1]
		members := []coreconsensus.MemberValue{f.member(t, keep, true), f.member(t, other, false)}
		// Only the third old voter is attested fenced: the other survivor
		// is a member, and used to need no receipt.
		f.refused(t, keep, Recover(keep.dd, f.next, members, f.options(n2)), "naming the other survivor a learner")
	}
}

// The same hole let a Recover run with no receipt at all: every other old
// voter listed as a learner, while the old majority may still be serving.
func TestRecoverNamingEveryOldVoterALearnerWithoutAFenceIsRefused(t *testing.T) {
	f := newRecoverFixture(t, coreconsensus.ClusterID{27})
	n0, n1, n2 := f.nodes[0], f.nodes[1], f.nodes[2]
	members := []coreconsensus.MemberValue{f.member(t, n0, true), f.member(t, n1, false), f.member(t, n2, false)}
	f.refused(t, n0, Recover(n0.dd, f.next, members, f.options()), "naming every old voter a learner, no receipt")
}

// An old member's ID is never one Init drew for the new cluster: the nodes
// a Recover names beside the survivor are joiners Init prepared under the
// new cluster id, each with a new NodeID (ADR 0152 R1-17, R3-02). Even
// attested fenced, an old voter named a learner is refused, and so is an
// old voter named by its ID only; the survivor with every other old voter
// fenced is accepted, as before.
func TestRecoverRefusesAnOldMemberNamedALearnerEvenFenced(t *testing.T) {
	f := newRecoverFixture(t, coreconsensus.ClusterID{28})
	n0, n1, n2 := f.nodes[0], f.nodes[1], f.nodes[2]
	members := []coreconsensus.MemberValue{f.member(t, n0, true), f.member(t, n1, false)}
	err := Recover(n0.dd, f.next, members, f.options(n1, n2))
	f.refused(t, n0, err, "naming a fenced old voter a learner")
	if why := whyOf(err); why != whyOldMember {
		t.Fatalf("refused for %q, want %q", why, whyOldMember)
	}
	if err := Recover(n0.dd, f.next, members[:1], f.options(n1, n2)); err != nil {
		t.Fatalf("Recover on the survivor alone, every other old voter fenced: %v", err)
	}
}

// whyOf is the why field of a refusal, "" when it has none.
func whyOf(err error) string {
	for _, fl := range errs.FieldsOf(err) {
		if fl.Key() == "why" {
			return fl.StringValue()
		}
	}
	return ""
}

// checkOldMembers judges the new members against the configuration in
// force at the last entry kept: any ID it lists, learner or voter, and any
// ID it retired, is refused beside this node; a new ID is accepted.
func TestCheckOldMembersRefusesAnyOldOrRetiredID(t *testing.T) {
	const self, voter, learner, retired, fresh coreconsensus.NodeID = 1, 2, 3, 4, 5
	old := coreconsensus.MembershipValue{
		Members: []coreconsensus.MemberValue{{ID: self, Voter: true}, {ID: voter, Voter: true}, {ID: learner}},
		Retired: []coreconsensus.NodeID{retired},
	}
	for _, tc := range []struct {
		name string
		id   coreconsensus.NodeID
		ok   bool
	}{
		{"an old voter", voter, false},
		{"an old learner", learner, false},
		{"a retired id", retired, false},
		{"a new id", fresh, true},
	} {
		members := []coreconsensus.MemberValue{{ID: self, Voter: true}, {ID: tc.id}}
		err := checkOldMembers(&old, members, self)
		if tc.ok != (err == nil) || err != nil && !errors.Is(err, coreconsensus.RecoverRefused) {
			t.Errorf("%s named a learner: %v, want accepted %v", tc.name, err, tc.ok)
		}
	}
}
