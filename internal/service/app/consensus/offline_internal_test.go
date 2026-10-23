package consensus

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// threeOnDisk founds three nodes on data directories, writes n keys, and
// stops them all.
func threeOnDisk(t *testing.T, n int) (*cluster, []*diskNode) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5}}
	var nodes []*diskNode
	var members []coreconsensus.MemberValue
	for i := range 3 {
		dn := &diskNode{dir: t.TempDir()}
		dn.dd = openDir(t, dn.dir, 16<<10)
		slot := fmt.Sprintf("r%d", i)
		id, inc, err := Init(dn.dd, c.cid, slot)
		if err != nil {
			t.Fatal(err)
		}
		dn.id = id
		nodes = append(nodes, dn)
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: MemorySPKI(id), Voter: true})
	}
	for _, dn := range nodes {
		dn.sm = newKV()
		if err := Bootstrap(context.Background(), new(diskConfig(c, dn)), members); err != nil {
			t.Fatal(err)
		}
		if err := dn.dd.Close(); err != nil {
			t.Fatal(err)
		}
		dn.open(t, c)
	}
	// A node the test has not crashed when it fails is crashed here, before
	// its directory is removed: Windows refuses to remove a held LOCK.
	t.Cleanup(func() {
		for _, dn := range nodes {
			if dn.node != nil {
				dn.crash(t, c)
			}
		}
	})
	// Each key is proposed to whichever node leads: a leader whose own
	// fdatasync stalls past the maximum election timeout hands its
	// leadership over (#554), and a proposal it held then answers
	// OUTCOME_UNKNOWN. A loaded Windows runner had one sync take that long,
	// and the setup, bound to the first leader, failed (#587).
	var tok coreconsensus.TokenValue
	for i := range n {
		tok = proposeOnDisk(t, nodes, fmt.Sprintf("k%d=v%d", i, i))
	}
	c.waitAppliedDisk(nodes, tok.Index)
	return c, nodes
}

// waitAppliedDisk waits until every node applied index AND its log records
// a commit at or past it, or fails the test naming who is behind.
//
// Both halves are what the callers go on to rely on. Applied is what the
// state machine holds; the recorded commit is what an offline operation
// reads after the crash — Recover keeps a node's log up to its HardState
// commit, then only the suffix whose chain verifies — and a follower can
// apply an entry before the batch carrying its commit reaches the log. The
// index is the last proposal's token, never the largest Status().Commit at
// the time of the call: Status is a node's view after its last turn, and the
// proposer can be answered before the turn that commits publishes it. A deadline that ran
// out used to return in silence, so a lagging node was crashed and the
// failure surfaced later as a key the recovered history did not hold.
func (c *cluster) waitAppliedDisk(nodes []*diskNode, index uint64) {
	c.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for _, dn := range nodes {
		for {
			hs, err := dn.dd.Log().HardState()
			if err != nil {
				c.t.Fatalf("node %d hard state: %v", dn.id, err)
			}
			st := dn.node.Status()
			if st.Applied >= index && hs.Commit >= index {
				break
			}
			if time.Now().After(deadline) {
				c.t.Fatalf("node %d: applied %d, recorded commit %d, want both >= %d", dn.id, st.Applied, hs.Commit, index)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestRecoverStartsANewHistoryFromOneNode(t *testing.T) {
	c, nodes := threeOnDisk(t, 30)
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	keep := nodes[0]
	keep.dd = openDir(t, keep.dir, 16<<10)
	st, err := keep.dd.Log().Stamp()
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	next := coreconsensus.ClusterID{6}
	self := coreconsensus.MemberValue{ID: keep.id, Incarnation: st.Incarnation, Slot: st.Slot, SPKI: MemorySPKI(keep.id), Voter: true}
	opt := RecoverOptions{Confirm: next.String(), Operator: "op:recover", WitnessKeys: []ed25519.PublicKey{pub}}
	// Without a fence attestation for the two old voters, it fails closed.
	if err := Recover(keep.dd, next, []coreconsensus.MemberValue{self}, &opt); !errors.Is(err, coreconsensus.RecoverRefused) {
		t.Fatalf("unattested recover: %v", err)
	}
	for _, dn := range nodes[1:] {
		opt.Fenced = append(opt.Fenced, coreconsensus.FencedAttestationValue{
			Voter: dn.id, Method: "powered-off",
			Receipts: [][]byte{ed25519.Sign(priv, ReceiptMessage(c.cid, next, dn.id))},
		})
	}
	// Two members under one key: the transport could not tell them apart
	// (review M5).
	twin := coreconsensus.MemberValue{ID: 99, Slot: "twin", SPKI: self.SPKI}
	if err := Recover(keep.dd, next, []coreconsensus.MemberValue{self, twin}, &opt); !errors.Is(err, coreconsensus.RecoverRefused) {
		t.Fatalf("recover with a shared key: %v", err)
	}
	if err := Recover(keep.dd, next, []coreconsensus.MemberValue{self}, &opt); err != nil {
		t.Fatal(err)
	}
	c.cid = next
	keep.sm = newKV()
	cfg := diskConfig(c, keep)
	cfg.MinVoters = 1
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	keep.node = n
	defer keep.crash(t, c)
	tok := propose(t, n, "after=recover")
	if tok.Cluster != next {
		t.Fatalf("new history token %v", tok)
	}
	if v, _ := keep.sm.get("k29"); v != "v29" {
		t.Fatalf("recovered state lost k29: %q", v)
	}
	// The old cluster's directory is refused under the new one.
	old := nodes[1]
	old.dd = openDir(t, old.dir, 16<<10)
	defer old.dd.Close()
	old.sm = newKV()
	if _, err := NewNode(diskConfig(c, old)); !errors.Is(err, coreconsensus.DataDirMismatch) {
		t.Fatalf("old pair restarted under the new cluster: %v", err)
	}
}

// appendPastCommit appends to a stopped node's log one command entry per kv,
// past its persisted commit, which it leaves as it is: what a follower holds
// when it synced and acknowledged entries its leader then committed, before
// it learned that commit. The entry at broken, when not negative, carries a
// body sum its data does not have; the entries after it chain on from the
// sum it records, so only that one link is broken. It returns the last
// index appended and the persisted commit.
func appendPastCommit(t *testing.T, dd DataDir, kvs []string, broken int) (last, commit uint64) {
	t.Helper()
	l := dd.Log()
	hs := must(l.HardState())(t)
	_, last = l.Bounds()
	prev := must(l.Entries(last, last+1, 1, nil))(t)[0]
	var es []coreconsensus.EntryValue
	for i, s := range kvs {
		e := coreconsensus.EntryValue{
			Position: coreconsensus.PositionValue{Term: prev.Position.Term, Index: prev.Position.Index + 1},
			Kind:     coreconsensus.EntryCommand, Data: []byte(s), BodySum: wire.BodySum([]byte(s)),
		}
		if i == broken {
			e.BodySum++
		}
		e.ChainSum = wire.ChainSum(prev.ChainSum, &e)
		es = append(es, e)
		prev = e
	}
	if err := l.Append(es, nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := must(l.HardState())(t); got.Commit != hs.Commit {
		t.Fatalf("setup: the persisted commit moved from %d to %d", hs.Commit, got.Commit)
	}
	return prev.Position.Index, hs.Commit
}

// recoverAlone runs Recover on keep, every other node attested fenced, and
// restarts keep alone under the new cluster.
func recoverAlone(t *testing.T, c *cluster, nodes []*diskNode, keep *diskNode, next coreconsensus.ClusterID) {
	t.Helper()
	st := must(keep.dd.Log().Stamp())(t)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	self := coreconsensus.MemberValue{ID: keep.id, Incarnation: st.Incarnation, Slot: st.Slot, SPKI: MemorySPKI(keep.id), Voter: true}
	opt := RecoverOptions{Confirm: next.String(), Operator: "op:recover", WitnessKeys: []ed25519.PublicKey{pub}}
	for _, dn := range nodes {
		if dn != keep {
			opt.Fenced = append(opt.Fenced, coreconsensus.FencedAttestationValue{
				Voter: dn.id, Method: "powered-off",
				Receipts: [][]byte{ed25519.Sign(priv, ReceiptMessage(c.cid, next, dn.id))},
			})
		}
	}
	if err := Recover(keep.dd, next, []coreconsensus.MemberValue{self}, &opt); err != nil {
		t.Fatal(err)
	}
	c.cid = next
	keep.sm = newKV()
	cfg := diskConfig(c, keep)
	cfg.MinVoters = 1
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	keep.node = n
	propose(t, n, "after=recover")
}

// recoverReport is the last line Recover wrote to keep's audit file.
func recoverReport(t *testing.T, keep *diskNode) string {
	t.Helper()
	b := must(os.ReadFile(filepath.Join(keep.dir, fileAudit)))(t)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}

// A survivor that synced and acknowledged entries its leader committed,
// before it learned that commit, holds them past its persisted commit.
// Recover adopts every one of them whose chain verifies: cutting them back
// to the persisted commit loses writes the old cluster acknowledged
// (issue #399). The report says how many it adopted.
func TestRecoverAdoptsTheVerifiedSuffixPastThePersistedCommit(t *testing.T) {
	c, nodes := threeOnDisk(t, 30)
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	keep := nodes[0]
	keep.dd = openDir(t, keep.dir, 16<<10)
	last, commit := appendPastCommit(t, keep.dd, []string{"acked0=a", "acked1=b", "acked2=c"}, -1)
	next := coreconsensus.ClusterID{9}
	recoverAlone(t, c, nodes, keep, next)
	defer keep.crash(t, c)
	for k, want := range map[string]string{"k29": "v29", "acked0": "a", "acked1": "b", "acked2": "c", "after": "recover"} {
		if v, _ := keep.sm.get(k); v != want {
			t.Fatalf("after Recover, %s = %q, want %q", k, v, want)
		}
	}
	want := fmt.Sprintf("op:recover recover %s kept %d commit %d adopted %d", next, last, commit, last-commit)
	if got := recoverReport(t, keep); got != want {
		t.Fatalf("Recover reported %q, want %q", got, want)
	}
	if st := must(keep.dd.Log().Stamp())(t); st.Log.KeptIndex != last {
		t.Fatalf("KeptIndex %d, want the last adopted index %d", st.Log.KeptIndex, last)
	}
}

// A suffix whose chain breaks is cut at the first bad link: the entries
// before it are adopted, it and every entry after it are not, even when
// those chain on from it.
func TestRecoverCutsTheSuffixAtTheFirstBrokenLink(t *testing.T) {
	c, nodes := threeOnDisk(t, 30)
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	keep := nodes[0]
	keep.dd = openDir(t, keep.dir, 16<<10)
	last, commit := appendPastCommit(t, keep.dd, []string{"good=a", "bad=b", "after-bad=c"}, 1)
	kept := last - 2
	next := coreconsensus.ClusterID{10}
	recoverAlone(t, c, nodes, keep, next)
	defer keep.crash(t, c)
	if v, _ := keep.sm.get("good"); v != "a" {
		t.Fatalf("the entry before the broken link was cut: good = %q", v)
	}
	for _, k := range []string{"bad", "after-bad"} {
		if v, ok := keep.sm.get(k); ok {
			t.Fatalf("an entry at or past the broken link was adopted: %s = %q", k, v)
		}
	}
	want := fmt.Sprintf("op:recover recover %s kept %d commit %d adopted %d", next, kept, commit, kept-commit)
	if got := recoverReport(t, keep); got != want {
		t.Fatalf("Recover reported %q, want %q", got, want)
	}
}

// TestOfflineReadsFailClosed pins what an offline operation does with a
// store it cannot read. Recover decides from the old configuration which
// voters must be attested fenced, and Inspect is what an operator decides
// from: a snapshot directory that cannot be listed fails both, rather than
// reading as a directory that holds no snapshot.
func TestOfflineReadsFailClosed(t *testing.T) {
	c, nodes := threeOnDisk(t, 10)
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	keep := nodes[0]
	keep.dd = openDir(t, keep.dir, 16<<10)
	t.Cleanup(func() {
		if err := keep.dd.Close(); err != nil {
			t.Error(err)
		}
	})
	// A file where the snapshot directory was: every listing fails, on
	// every platform, with no permission trick.
	snap := filepath.Join(keep.dir, "snap")
	if err := os.RemoveAll(snap); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snap, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(keep.dd); !errors.Is(err, coreconsensus.StorageFailed) {
		t.Fatalf("Inspect over an unreadable snapshot directory: %v", err)
	}
	st, err := keep.dd.Log().Stamp()
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	next := coreconsensus.ClusterID{7}
	self := coreconsensus.MemberValue{ID: keep.id, Incarnation: st.Incarnation, Slot: st.Slot, SPKI: MemorySPKI(keep.id), Voter: true}
	opt := RecoverOptions{Confirm: next.String(), Operator: "op:recover", WitnessKeys: []ed25519.PublicKey{pub}}
	for _, dn := range nodes[1:] {
		opt.Fenced = append(opt.Fenced, coreconsensus.FencedAttestationValue{
			Voter: dn.id, Method: "powered-off",
			Receipts: [][]byte{ed25519.Sign(priv, ReceiptMessage(c.cid, next, dn.id))},
		})
	}
	if err := Recover(keep.dd, next, []coreconsensus.MemberValue{self}, &opt); !errors.Is(err, coreconsensus.StorageFailed) {
		t.Fatalf("Recover over an unreadable snapshot directory: %v", err)
	}
}

func TestRestoreSnapshotIntoAnEmptyDirectory(t *testing.T) {
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
	fresh := &diskNode{dir: t.TempDir()}
	fresh.dd = openDir(t, fresh.dir, 16<<10)
	next := coreconsensus.ClusterID{8}
	id, inc, err := Init(fresh.dd, next, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	fresh.id = id
	self := coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: "fresh", SPKI: MemorySPKI(id), Voter: true}
	twin := coreconsensus.MemberValue{ID: 99, Slot: "twin", SPKI: self.SPKI}
	if _, err := RestoreSnapshot(fresh.dd, bytes.NewReader(buf.Bytes()), next, []coreconsensus.MemberValue{self, twin},
		&RestoreOptions{Expected: meta, Confirm: next.String(), Operator: "op:restore", MaxEncoded: 1 << 30, MaxDecoded: 1 << 30}); !errors.Is(err, coreconsensus.RestoreRefused) {
		t.Fatalf("restore with a shared key (review M5): %v", err)
	}
	if _, err := RestoreSnapshot(fresh.dd, bytes.NewReader(buf.Bytes()), next, []coreconsensus.MemberValue{self},
		&RestoreOptions{Expected: meta, Confirm: next.String(), Operator: "op:restore", MaxEncoded: 1 << 30, MaxDecoded: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	c.cid = next
	fresh.sm = newKV()
	cfg := diskConfig(c, fresh)
	cfg.MinVoters = 1
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fresh.node = n
	if v, _ := fresh.sm.get("k19"); v != "v19" {
		t.Fatalf("restored state: k19=%q", v)
	}
	propose(t, n, "after=restore")
	// The restore compacted the empty log past its end, and the proposal
	// appended after it: the restart replays both (#516).
	fresh.crash(t, c)
	fresh.dd = openDir(t, fresh.dir, 16<<10)
	fresh.sm = newKV()
	cfg = diskConfig(c, fresh)
	cfg.MinVoters = 1
	n, err = NewNode(cfg)
	if err != nil {
		t.Fatalf("restart after restore and propose: %v", err)
	}
	fresh.node = n
	defer fresh.crash(t, c)
	propose(t, n, "again=restart")
	if v, _ := fresh.sm.get("after"); v != "restore" {
		t.Fatalf("after the restart: after=%q, want restore", v)
	}
}

// tokenKV is a kv that records the token each command was applied under.
type tokenKV struct {
	*kv
	mu     sync.Mutex
	tokens map[uint64]coreconsensus.TokenValue
}

func newTokenKV(sm *kv) *tokenKV {
	return &tokenKV{kv: sm, tokens: map[uint64]coreconsensus.TokenValue{}}
}

func (s *tokenKV) Apply(e coreconsensus.EntryValue) any {
	s.mu.Lock()
	s.tokens[e.Position.Index] = e.Token
	s.mu.Unlock()
	return s.kv.Apply(e)
}

func (s *tokenKV) token(index uint64) (coreconsensus.TokenValue, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.tokens[index]
	return tok, ok
}

// #507: the entries Recover keeps reach the state machine under the same
// token on every node. The survivor applies them under the predecessor's
// identity; a node made with Init under the new cluster and admitted after
// Recover applied the ones it received by append under the new identity,
// which never learned the lineage, and the replicated state built on the
// token diverged. The joiner learns the lineage from the snapshot it
// installs, and a leader never ships one of another lineage: every kept
// entry it applies carries the survivor's token, and its identity names
// the predecessor and the index kept.
func TestTheEntriesRecoverKeepsHaveOneTokenOnEveryNode(t *testing.T) {
	for _, keys := range []int{40, 140} {
		t.Run(fmt.Sprintf("keys=%d", keys), func(t *testing.T) {
			c, nodes := threeOnDisk(t, keys)
			for _, dn := range nodes {
				dn.crash(t, c)
			}
			keep := nodes[0]
			keep.dd = openDir(t, keep.dir, 16<<10)
			old := c.cid
			next := coreconsensus.ClusterID{21}
			joiner := &diskNode{dir: t.TempDir()}
			joiner.dd = openDir(t, joiner.dir, 16<<10)
			jid, jinc, err := Init(joiner.dd, next, "joiner")
			if err != nil {
				t.Fatal(err)
			}
			joiner.id = jid
			members, opt := recoverArgs(t, c, nodes, keep, next)
			members = append(members, coreconsensus.MemberValue{ID: jid, Incarnation: jinc, Slot: "joiner", SPKI: MemorySPKI(jid)})
			if err := Recover(keep.dd, next, members, opt); err != nil {
				t.Fatal(err)
			}
			kept := must(keep.dd.Log().Stamp())(t).Log.KeptIndex
			c.cid = next
			machines := map[*diskNode]*tokenKV{}
			for _, dn := range []*diskNode{keep, joiner} {
				dn.sm = newKV()
				machines[dn] = newTokenKV(dn.sm)
				cfg := diskConfig(c, dn)
				cfg.Machine, cfg.MinVoters = machines[dn], 1
				dn.node = must(NewNode(cfg))(t)
			}
			defer keep.crash(t, c)
			defer joiner.crash(t, c)
			tok := propose(t, keep.node, "after=recover")
			waitAppliedOnly(t, keep, tok.Index)
			waitAppliedOnly(t, joiner, tok.Index)
			var differ []uint64
			for i := uint64(1); i <= kept; i++ {
				jt, ok := machines[joiner].token(i)
				if !ok {
					continue
				}
				if kt, _ := machines[keep].token(i); kt != jt {
					differ = append(differ, i)
				}
			}
			if len(differ) > 0 {
				i := differ[0]
				kt, _ := machines[keep].token(i)
				jt, _ := machines[joiner].token(i)
				t.Fatalf("kept entries %v applied under another token on the joiner (e.g. %d: survivor %v, joiner %v)", differ, i, kt, jt)
			}
			if got := joiner.node.Status().Log; got.Predecessor != old || got.KeptIndex != kept {
				t.Fatalf("the joiner's lineage %+v, want predecessor %s kept %d", got, old, kept)
			}
			if st := must(joiner.dd.Log().Stamp())(t); st.Log.Predecessor != old || st.Log.KeptIndex != kept {
				t.Fatalf("the joiner's stamp %+v does not record the lineage", st.Log)
			}
		})
	}
}

// proposeOnDisk proposes kv to whichever of the live nodes leads, again on a
// new leader when the one asked lost its leadership before answering; only
// for a set the test may write twice, as proposeOnLeader.
func proposeOnDisk(t *testing.T, live []*diskNode, kv string) coreconsensus.TokenValue {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		// A leader is looked for until the write's own deadline: an
		// election on a disk whose fdatasync takes most of an election
		// timeout can need several rounds, and a leader lookup that gave
		// up after five seconds failed a write that had fifteen left
		// (#587).
		var l *diskNode
		for l == nil {
			for _, dn := range live {
				if dn.node.Status().Role == coreconsensus.RoleLeader {
					l = dn
				}
			}
			if l == nil && time.Now().After(deadline) {
				t.Fatalf("propose %q: no leader", kv)
			}
			time.Sleep(5 * time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		tok, _, err := l.node.Propose(ctx, []byte(kv))
		cancel()
		if err == nil {
			return tok
		}
		retry := errors.Is(err, coreconsensus.NotLeader) ||
			errs.HasCode(err, coreconsensus.CodeOutcomeUnknown) || errs.HasCode(err, coreconsensus.CodeEntrySuperseded)
		if !retry || time.Now().After(deadline) {
			t.Fatalf("propose %q: %v", kv, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitAppliedOnly waits until dn applied index. The commit its store records
// is not waited for: an install moves the commit past it, and the store
// records one again only with a later batch, which a test that stops
// proposing may never send.
func waitAppliedOnly(t *testing.T, dn *diskNode, index uint64) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); dn.node.Status().Applied < index; {
		if time.Now().After(deadline) {
			t.Fatalf("node %d applied %d, want %d", dn.id, dn.node.Status().Applied, index)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// #516 through the driver, on disk: a node that fell behind the leader's
// compaction catches up by an install — its log compacted past its end —
// appends the entries that follow, and restarts. The restart replays the
// compaction where it stands; it used to quarantine the sound disk.
func TestAnInstalledNodeRestartsAfterAppending(t *testing.T) {
	c, nodes := threeOnDisk(t, 3)
	lead := nodes[0]
	for _, dn := range nodes {
		if dn.node.Status().Role == coreconsensus.RoleLeader {
			lead = dn
		}
	}
	lag := nodes[0]
	if lag == lead {
		lag = nodes[1]
	}
	_, lagLast := lag.dd.Log().Bounds()
	live := []*diskNode{lead}
	for _, dn := range nodes {
		if dn != lead && dn != lag {
			live = append(live, dn)
		}
	}
	c.net.Isolate(lag.id, true)
	lag.crash(t, c)
	var tok coreconsensus.TokenValue
	for i := range 200 {
		tok = proposeOnDisk(t, live, fmt.Sprintf("k%d=v%d", i, i))
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if first, _ := leaderOf(t, live).dd.Log().Bounds(); first > lagLast+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the leader never compacted past %d", lagLast)
		}
		time.Sleep(10 * time.Millisecond)
	}
	lag.open(t, c)
	c.net.Isolate(lag.id, false)
	waitAppliedOnly(t, lag, tok.Index)
	if metas := must(lag.dd.Snapshots().List())(t); len(metas) == 0 {
		t.Fatal("setup: the lagging node caught up without an install")
	}
	for i := range 5 {
		tok = proposeOnDisk(t, nodes, fmt.Sprintf("post%d=v", i))
	}
	waitAppliedOnly(t, lag, tok.Index)
	c.net.Isolate(lag.id, true)
	lag.crash(t, c)
	lag.dd = openDir(t, lag.dir, 16<<10)
	lag.sm = newKV()
	n, err := NewNode(diskConfig(c, lag))
	if err != nil {
		t.Fatalf("the installed node does not restart: %v", err)
	}
	lag.node = n
	c.net.Isolate(lag.id, false)
	tok = proposeOnDisk(t, nodes, "after=restart")
	waitAppliedOnly(t, lag, tok.Index)
	if v, _ := lag.sm.get("post4"); v != "v" {
		t.Fatalf("the restarted node: post4=%q, want v", v)
	}
	for _, dn := range nodes {
		dn.crash(t, c)
	}
}

// silenceByDamage damages synced data in the last segment of the crashed
// victim, checks the start is refused LOG_QUARANTINED, and takes the
// TruncateAndStaySilent exit, leaving victim's directory open and silent.
func silenceByDamage(t *testing.T, c *cluster, victim *diskNode) {
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
	// Flip a payload byte of the second record: synced data, a complete
	// batch, valid records after it.
	r0, ok := parseRecord(b)
	if !ok {
		t.Fatal("setup: the segment's first record does not parse")
	}
	if _, ok := parseRecord(b[r0.length:]); !ok {
		t.Fatal("setup: no complete second record to damage")
	}
	b[r0.length+recHeader+recFixed] ^= 0xff
	if err := os.WriteFile(last, b, 0o600); err != nil {
		t.Fatal(err)
	}
	victim.dd = openDir(t, victim.dir, 16<<10)
	victim.sm = newKV()
	if _, err := NewNode(diskConfig(c, victim)); !errors.Is(err, coreconsensus.LogQuarantined) {
		t.Fatalf("damaged log: %v", err)
	}
	if err := ClearQuarantine(victim.dd, TruncateAndStaySilent, "op:silent"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(victim.dir, fileSilent)); err != nil {
		t.Fatal("no silence condition persisted")
	}
}

// #524: a silent node never campaigns. The lagging third node is down while
// the leader commits with the victim, then the leader goes down and the
// victim truncates at damage to entries that leader acknowledged. With the
// leader still down the victim and the lagger must not elect anyone: the
// victim's own candidacy is a vote, and the silence grants none. It
// campaigned, won with the lagger's grant, and overwrote the acknowledged
// entries it had truncated. The network is isolated: a node really down
// sends nothing, and frames the memory network kept would reach the lagger
// at its restart and hide the defect.
func TestASilentNodeIsNeverElected(t *testing.T) {
	c, nodes := threeOnDisk(t, 3)
	var lead, victim, lagger *diskNode
	for _, dn := range nodes {
		switch {
		case dn.node.Status().Role == coreconsensus.RoleLeader && lead == nil:
			lead = dn
		case victim == nil:
			victim = dn
		default:
			lagger = dn
		}
	}
	if lead == nil || victim == nil || lagger == nil {
		t.Fatal("setup: no leader among the three")
	}
	c.net.Isolate(lagger.id, true)
	lagger.crash(t, c)
	var tok coreconsensus.TokenValue
	for i := range 30 {
		tok = propose(t, lead.node, fmt.Sprintf("ack%d=%s", i, strings.Repeat("v", 1024)))
	}
	waitAppliedOnly(t, lead, tok.Index)
	waitAppliedOnly(t, victim, tok.Index)
	c.net.Isolate(lead.id, true)
	lead.crash(t, c)
	c.net.Isolate(victim.id, true)
	victim.crash(t, c)
	silenceByDamage(t, c, victim)
	victim.node = must(NewNode(diskConfig(c, victim)))(t)
	lagger.open(t, c)
	c.net.Cut(victim.id, lagger.id, false)
	c.net.Cut(lagger.id, victim.id, false)
	// Six election timeouts at the disk timing's maximum, the lagger's
	// restart window long over: unfixed, the victim led within two.
	deadline := time.Now().Add(6 * testTimingDisk().ElectionTimeoutMax)
	for time.Now().Before(deadline) {
		for _, dn := range []*diskNode{victim, lagger} {
			if st := dn.node.Status(); st.Role == coreconsensus.RoleLeader {
				t.Fatalf("a leader was elected while the victim is silent and the old leader down: node %d (victim %v) %+v",
					dn.id, dn == victim, st)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	victim.crash(t, c)
	lagger.crash(t, c)
}

// #524: Recover removes the silence condition. The survivor's new history
// is closed to the old terms, and with the silence kept it would be the
// only voter of that history and never campaign, so never be elected.
func TestRecoverEndsTheSilence(t *testing.T) {
	c, nodes := threeOnDisk(t, 40)
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	victim := nodes[2]
	silenceByDamage(t, c, victim)
	recoverAlone(t, c, nodes, victim, coreconsensus.ClusterID{6})
	if _, err := os.Stat(filepath.Join(victim.dir, fileSilent)); !os.IsNotExist(err) {
		t.Fatalf("the silence condition survived Recover: %v", err)
	}
	victim.crash(t, c)
}

// TruncateAndStaySilent: a node quarantined for damage to synced data
// truncates at the damage, stays silent, and votes again only once a leader
// rewrote its log and committed in its own term (R3-09, R4-03).
func TestTruncateAndStaySilentRejoins(t *testing.T) {
	c, nodes := threeOnDisk(t, 40)
	victim := nodes[2]
	victim.crash(t, c)
	silenceByDamage(t, c, victim)
	n, err := NewNode(diskConfig(c, victim))
	if err != nil {
		t.Fatal(err)
	}
	victim.node = n
	var l *diskNode
	leaderBy := time.Now().Add(30 * time.Second)
	for l == nil {
		for _, dn := range nodes[:2] {
			if dn.node != nil && dn.node.Status().Role == coreconsensus.RoleLeader {
				l = dn
			}
		}
		//: no leader within the bound is a failure, never a hang.
		if l == nil && time.Now().After(leaderBy) {
			t.Fatal("no leader among the two healthy nodes")
		}
		time.Sleep(5 * time.Millisecond)
	}
	propose(t, l.node, "after=silence")
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, statErr := os.Stat(filepath.Join(victim.dir, fileSilent))
		if v, _ := victim.sm.get("after"); v == "silence" && os.IsNotExist(statErr) {
			break
		}
		if time.Now().After(deadline) {
			for _, dn := range nodes {
				if dn.node != nil {
					var d string
					dn.node.exec(func() {
						d = dn.node.r.Debug()
					})
					t.Logf("node %d: %s", dn.id, d)
				}
			}
			t.Fatalf("the silent node never rejoined (status %+v, silent file %v)", victim.node.Status(), statErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, dn := range nodes {
		if dn.node != nil {
			dn.crash(t, c)
		}
	}
}

// Two founders given the same members in another order write the same entry
// 1: the guard compares the sorted list, so the entry must be sorted too,
// or the chain refuses every append from the first leader (review M1).
func TestBootstrapEntryOneIgnoresTheMemberOrder(t *testing.T) {
	cl := coreconsensus.ClusterID{1, 2, 3}
	d1 := openDir(t, t.TempDir(), 1<<16)
	d2 := openDir(t, t.TempDir(), 1<<16)
	defer d1.Close()
	defer d2.Close()
	id1, inc1, err := Init(d1, cl, "a")
	if err != nil {
		t.Fatal(err)
	}
	id2, inc2, err := Init(d2, cl, "b")
	if err != nil {
		t.Fatal(err)
	}
	m1 := coreconsensus.MemberValue{ID: id1, Incarnation: inc1, Slot: "a", SPKI: [32]byte{1}, Voter: true}
	m2 := coreconsensus.MemberValue{ID: id2, Incarnation: inc2, Slot: "b", SPKI: [32]byte{2}, Voter: true}
	if err := Bootstrap(context.Background(), &Config{Log: d1.Log(), Cluster: cl, ID: id1}, []coreconsensus.MemberValue{m1, m2}); err != nil {
		t.Fatal(err)
	}
	if err := Bootstrap(context.Background(), &Config{Log: d2.Log(), Cluster: cl, ID: id2}, []coreconsensus.MemberValue{m2, m1}); err != nil {
		t.Fatal(err)
	}
	e1, err := d1.Log().Entries(1, 2, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := d2.Log().Entries(1, 2, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(e1[0].Data, e2[0].Data) || e1[0].ChainSum != e2[0].ChainSum {
		t.Fatal("the same founding members in another order wrote another entry 1")
	}
}

// Two founding members under one key are refused: the transport admits a
// connection by its key, and could not tell them apart (review M5).
func TestBootstrapRefusesASharedKey(t *testing.T) {
	cl := coreconsensus.ClusterID{1, 2, 3}
	d := openDir(t, t.TempDir(), 1<<16)
	defer d.Close()
	id, inc, err := Init(d, cl, "a")
	if err != nil {
		t.Fatal(err)
	}
	self := coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: "a", SPKI: [32]byte{1}, Voter: true}
	twin := coreconsensus.MemberValue{ID: id + 1, Incarnation: inc, Slot: "b", SPKI: self.SPKI, Voter: true}
	if err := Bootstrap(context.Background(), &Config{Log: d.Log(), Cluster: cl, ID: id}, []coreconsensus.MemberValue{self, twin}); !errors.Is(err, coreconsensus.BootstrapRefused) {
		t.Fatalf("bootstrap with a shared key: %v", err)
	}
}

// recoverArgs are the members and options of a Recover of keep alone into
// next, every other node attested fenced by one witness.
func recoverArgs(t *testing.T, c *cluster, nodes []*diskNode, keep *diskNode, next coreconsensus.ClusterID) ([]coreconsensus.MemberValue, *RecoverOptions) {
	t.Helper()
	st := must(keep.dd.Log().Stamp())(t)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	self := coreconsensus.MemberValue{ID: keep.id, Incarnation: st.Incarnation, Slot: st.Slot, SPKI: MemorySPKI(keep.id), Voter: true}
	opt := RecoverOptions{Confirm: next.String(), Operator: "op:recover", WitnessKeys: []ed25519.PublicKey{pub}}
	for _, dn := range nodes {
		if dn != keep {
			opt.Fenced = append(opt.Fenced, coreconsensus.FencedAttestationValue{
				Voter: dn.id, Method: "powered-off",
				Receipts: [][]byte{ed25519.Sign(priv, ReceiptMessage(c.cid, next, dn.id))},
			})
		}
	}
	return []coreconsensus.MemberValue{self}, &opt
}

// refusedStart asserts that keep, started under c's cluster, is refused by
// the STOP-RECOVER marker, with a detail that names the command to run again.
func refusedStart(t *testing.T, c *cluster, keep *diskNode, next coreconsensus.ClusterID) {
	t.Helper()
	keep.sm = newKV()
	cfg := diskConfig(c, keep)
	cfg.MinVoters = 1
	n, err := NewNode(cfg)
	if err == nil {
		keep.node = n
		t.Fatalf("a directory with a Recover in progress started under cluster %s", c.cid)
	}
	if !errors.Is(err, coreconsensus.StopMarkerPresent) {
		t.Fatalf("a start with a Recover in progress: %v, want STOP_MARKER_PRESENT", err)
	}
	fields := map[string]string{}
	for _, f := range errs.FieldsOf(err) {
		fields[f.Key()] = f.StringValue()
	}
	if fields["marker"] != markerRecover || !strings.Contains(fields["detail"], "run Recover again to cluster "+next.String()) {
		t.Fatalf("the refusal names marker %q, detail %q: want %s and the command to run again", fields["marker"], fields["detail"], markerRecover)
	}
}

// startRecovered starts keep alone under next and checks it holds the old
// history it kept, the entries adopted past the persisted commit included.
func startRecovered(t *testing.T, c *cluster, keep *diskNode, next coreconsensus.ClusterID, want map[string]string) {
	t.Helper()
	c.cid = next
	keep.sm = newKV()
	cfg := diskConfig(c, keep)
	cfg.MinVoters = 1
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatalf("the recovered node does not start: %v", err)
	}
	keep.node = n
	if tok := propose(t, n, "after=recover"); tok.Cluster != next {
		t.Fatalf("new history token %v", tok)
	}
	for k, v := range want {
		if got, _ := keep.sm.get(k); got != v {
			t.Fatalf("after Recover, %s = %q, want %q", k, got, v)
		}
	}
}

// TestAnInterruptedRecoverRefusesAStartAndResumes pins #405. Recover writes
// the forced configuration of the new members, then the new stamp; between
// the two, the directory used to start as a member of the OLD cluster
// running the NEW configuration. STOP-RECOVER, durable before the forced
// entry, refuses that start, naming the command to run again. A Recover to
// another cluster is refused; one to the same cluster still asks for the
// fence of the old voters — the interrupted forced entry is never read as
// the old configuration — then resumes from the index the first one kept,
// and the node starts under the new cluster with its state.
func TestAnInterruptedRecoverRefusesAStartAndResumes(t *testing.T) {
	type tc struct {
		name           string
		beforeTheEntry bool
	}
	runCase := func(t *testing.T, tcase tc) {
		c, nodes := threeOnDisk(t, 30)
		for _, dn := range nodes {
			dn.crash(t, c)
		}
		keep := nodes[0]
		keep.dd = openDir(t, keep.dir, 16<<10)
		last, _ := appendPastCommit(t, keep.dd, []string{"acked0=a", "acked1=b"}, -1)
		old := must(keep.dd.Log().Stamp())(t).Log.Cluster
		next := coreconsensus.ClusterID{11}
		members, opt := recoverArgs(t, c, nodes, keep, next)
		if tcase.beforeTheEntry {
			// The marker is durable, nothing else is written yet.
			if err := keep.dd.writeRecoverIntent(&recoverIntent{cluster: next, kept: last}); err != nil {
				t.Fatal(err)
			}
		} else {
			interruptBeforeTheStamp(t, keep, next, members, opt, last)
		}
		// The machine restarts.
		if err := keep.dd.Close(); err != nil {
			t.Fatal(err)
		}
		keep.dd = openDir(t, keep.dir, 16<<10)
		defer func() {
			if keep.node != nil {
				keep.crash(t, c)
				return
			}
			if err := keep.dd.Close(); err != nil {
				t.Error(err)
			}
		}()
		refusedStart(t, c, keep, next)
		other := coreconsensus.ClusterID{12}
		otherMembers, otherOpt := recoverArgs(t, c, nodes, keep, other)
		if err := Recover(keep.dd, other, otherMembers, otherOpt); !errors.Is(err, coreconsensus.RecoverRefused) {
			t.Fatalf("a Recover to another cluster while one is in progress: %v", err)
		}
		unfenced := *opt
		unfenced.Fenced = nil
		if err := Recover(keep.dd, next, members, &unfenced); !errors.Is(err, coreconsensus.RecoverRefused) {
			t.Fatalf("a resumed Recover without the fence of the old voters: %v", err)
		}
		if err := Recover(keep.dd, next, members, opt); err != nil {
			t.Fatalf("resuming the interrupted Recover: %v", err)
		}
		if _, err := os.Stat(filepath.Join(keep.dir, markerRecover)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the marker outlived the Recover that completed: %v", err)
		}
		if got, want := recoverReport(t, keep), fmt.Sprintf("op:recover recover %s resumed kept %d", next, last); got != want {
			t.Fatalf("Recover reported %q, want %q", got, want)
		}
		st := must(keep.dd.Log().Stamp())(t)
		if st.Log.Cluster != next || st.Log.Predecessor != old || st.Log.KeptIndex != last {
			t.Fatalf("stamp %+v, want cluster %s after %s keeping %d", st.Log, next, old, last)
		}
		startRecovered(t, c, keep, next, map[string]string{"k29": "v29", "acked0": "a", "acked1": "b"})
	}
	for _, tcase := range []tc{{name: "before the forced entry", beforeTheEntry: true}, {name: "between the forced entry and the stamp"}} {
		t.Run(tcase.name, func(t *testing.T) { runCase(t, tcase) })
	}
}

// interruptBeforeTheStamp runs a Recover of keep into next whose stamp
// cannot be written, a directory standing where its temporary goes: the
// forced entry is durable, the stamp is the old cluster's, and the marker is
// there. The obstacle is removed afterwards, as a repaired disk would be.
func interruptBeforeTheStamp(t *testing.T, keep *diskNode, next coreconsensus.ClusterID, members []coreconsensus.MemberValue, opt *RecoverOptions, kept uint64) {
	t.Helper()
	old := must(keep.dd.Log().Stamp())(t)
	block := filepath.Join(keep.dir, fileStamp+".tmp")
	if err := os.Mkdir(block, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Recover(keep.dd, next, members, opt); err == nil {
		t.Fatal("Recover wrote its stamp through a directory in the way")
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	if st := must(keep.dd.Log().Stamp())(t); st.Log != old.Log {
		t.Fatalf("setup: the stamp moved to %+v", st.Log)
	}
	_, last := keep.dd.Log().Bounds()
	e := must(keep.dd.Log().Entries(last, last+1, 1, nil))(t)[0]
	if last != kept+1 || e.Kind != coreconsensus.EntryMembership {
		t.Fatalf("setup: the log ends at %d with a %v entry, want the forced configuration at %d", last, e.Kind, kept+1)
	}
	if _, err := os.Stat(filepath.Join(keep.dir, markerRecover)); err != nil {
		t.Fatalf("setup: no marker after the forced entry: %v", err)
	}
}

// TestARecoverInterruptedAfterItsStampIsCompleted pins the last window of
// #405: the new stamp is durable and the marker not yet removed. The start
// is refused all the same, a Recover to another cluster too, and a Recover
// to the same cluster, confirmed and audited, only removes the marker: the
// cluster it names is no longer new, and nothing else is decided again.
func TestARecoverInterruptedAfterItsStampIsCompleted(t *testing.T) {
	c, nodes := threeOnDisk(t, 30)
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	keep := nodes[0]
	keep.dd = openDir(t, keep.dir, 16<<10)
	next := coreconsensus.ClusterID{13}
	members, opt := recoverArgs(t, c, nodes, keep, next)
	if err := Recover(keep.dd, next, members, opt); err != nil {
		t.Fatal(err)
	}
	stamp := must(keep.dd.Log().Stamp())(t)
	_, last := keep.dd.Log().Bounds()
	// The crash came before the marker's removal was durable.
	if err := keep.dd.writeRecoverIntent(&recoverIntent{cluster: next, kept: stamp.Log.KeptIndex}); err != nil {
		t.Fatal(err)
	}
	if err := keep.dd.Close(); err != nil {
		t.Fatal(err)
	}
	keep.dd = openDir(t, keep.dir, 16<<10)
	defer func() {
		if keep.node != nil {
			keep.crash(t, c)
			return
		}
		if err := keep.dd.Close(); err != nil {
			t.Error(err)
		}
	}()
	c.cid = next
	refusedStart(t, c, keep, next)
	other := coreconsensus.ClusterID{14}
	otherMembers, otherOpt := recoverArgs(t, c, nodes, keep, other)
	if err := Recover(keep.dd, other, otherMembers, otherOpt); !errors.Is(err, coreconsensus.RecoverRefused) {
		t.Fatalf("a Recover to another cluster while one is in progress: %v", err)
	}
	unconfirmed := *opt
	unconfirmed.Confirm = ""
	if err := Recover(keep.dd, next, members, &unconfirmed); !errors.Is(err, coreconsensus.RecoverRefused) {
		t.Fatalf("an unconfirmed completion: %v", err)
	}
	if err := Recover(keep.dd, next, members, opt); err != nil {
		t.Fatalf("completing the interrupted Recover: %v", err)
	}
	if _, err := os.Stat(filepath.Join(keep.dir, markerRecover)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the marker is still there: %v", err)
	}
	if got, want := recoverReport(t, keep), fmt.Sprintf("op:recover recover %s completed", next); got != want {
		t.Fatalf("Recover reported %q, want %q", got, want)
	}
	if st := must(keep.dd.Log().Stamp())(t); st != stamp {
		t.Fatalf("completing rewrote the stamp: %+v, want %+v", st, stamp)
	}
	if _, now := keep.dd.Log().Bounds(); now != last {
		t.Fatalf("completing moved the log from %d to %d", last, now)
	}
	startRecovered(t, c, keep, next, map[string]string{"k29": "v29"})
}

// A TruncateAndStaySilent interrupted after its truncation leaves the
// quarantine marker, and the operator runs the exit again. The rerun reads a
// log whose dropped records no longer name their term: it used to persist a
// lower silence term, and the node could then vote a second time in the
// term of a vote the truncation dropped. The term is persisted before the
// truncation, and a rerun never lowers it (#418).
func TestARerunOfTruncateAndStaySilentKeepsTheDroppedTerm(t *testing.T) {
	dir, d := base(t)
	b := d.d.log.batch
	at := func(index uint64, end bool) []byte {
		return appendRecord(nil, &record{
			kind: recEntry, batch: b, index: index, term: 9, end: end,
			ekind: uint8(coreconsensus.EntryCommand), data: []byte("x"),
		})
	}
	r1, r2 := at(4, false), at(5, true)
	tail(t, d, r1, 0)
	tail(t, d, r2, int64(len(r1)))
	tail(t, d, []byte{0xff}, int64(recHeader+recFixed)) // damage r1, r2 stays valid
	nd := reopenExpect(t, dir, d, true)
	defer nd.Close()
	crash := errors.New("crash after the truncation")
	nd.d.afterTruncate = func() error { return crash }
	if err := ClearQuarantine(nd, TruncateAndStaySilent, "op:silent"); !errors.Is(err, crash) {
		t.Fatalf("first run: %v", err)
	}
	nd.d.afterTruncate = nil
	if err := ClearQuarantine(nd, TruncateAndStaySilent, "op:silent"); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	s, err := os.ReadFile(filepath.Join(dir, fileSilent))
	if err != nil || len(s) != 16 {
		t.Fatalf("silence condition: %v (%d bytes)", err, len(s))
	}
	if term := binary.LittleEndian.Uint64(s); term != 9 {
		t.Fatalf("silence term %d after a rerun, want 9, the term of the records the truncation dropped", term)
	}
}

// A log compacted past its persisted commit: Recover kept the persisted
// commit, below the log, could not append its forced entry there, and every
// rerun failed the same way. The base is committed (a snapshot covers only
// applied entries), so the verified suffix is adopted from the log's first
// entry, and a log with nothing to chain from refuses with its reason (#422).
func TestRecoverAdoptsPastABaseAboveThePersistedCommit(t *testing.T) {
	dir := t.TempDir()
	d := openDir(t, dir, 1<<16)
	defer d.Close()
	var all []coreconsensus.EntryValue
	var prev uint32
	for i := uint64(1); i <= 10; i++ {
		data := fmt.Appendf(nil, "e%d", i)
		e := coreconsensus.EntryValue{Position: coreconsensus.PositionValue{Term: 1, Index: i}, Kind: coreconsensus.EntryCommand, BodySum: wire.BodySum(data), Data: data}
		e.ChainSum = wire.ChainSum(prev, &e)
		prev = e.ChainSum
		all = append(all, e)
	}
	if err := d.Log().Append(all, &coreconsensus.HardState{Term: 1, Commit: 3}); err != nil {
		t.Fatal(err)
	}
	if err := d.Log().CompactPrefix(6); err != nil {
		t.Fatal(err)
	}
	mustSync(t, d.Log())
	if f, _ := d.Log().Bounds(); f != 7 {
		t.Fatalf("setup: first %d, want 7", f)
	}
	kept, err := d.adoptedEnd(3)
	if err != nil || kept != 10 {
		t.Fatalf("adoptedEnd over a base above the commit = %d %v, want 10", kept, err)
	}
	if err := d.d.log.compactPrefixAt(20, 1); err != nil {
		t.Fatal(err)
	}
	mustSync(t, d.Log())
	if _, err := d.adoptedEnd(3); !errors.Is(err, coreconsensus.RecoverRefused) {
		t.Fatalf("nothing to chain from: %v, want RECOVER_REFUSED", err)
	}
}
