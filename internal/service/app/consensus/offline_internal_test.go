package consensus

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// threeOnDisk founds three nodes on data directories, writes n keys, and
// stops them all.
func threeOnDisk(t *testing.T, n int) (*cluster, []*diskNode) {
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5}}
	var nodes []*diskNode
	var members []coreconsensus.Member
	for i := 0; i < 3; i++ {
		dn := &diskNode{dir: t.TempDir()}
		dn.dd = openDir(t, dn.dir, 16<<10)
		slot := fmt.Sprintf("r%d", i)
		id, inc, err := Init(dn.dd, c.cid, slot)
		if err != nil {
			t.Fatal(err)
		}
		dn.id = id
		nodes = append(nodes, dn)
		members = append(members, coreconsensus.Member{ID: id, Incarnation: inc, Slot: slot, SPKI: MemorySPKI(id), Voter: true})
	}
	for _, dn := range nodes {
		dn.sm = newKV()
		if err := Bootstrap(context.Background(), diskConfig(c, dn), members); err != nil {
			t.Fatal(err)
		}
		_ = dn.dd.Close()
		dn.open(t, c)
	}
	var l *diskNode
	deadline := time.Now().Add(5 * time.Second)
	for l == nil && time.Now().Before(deadline) {
		for _, dn := range nodes {
			if dn.node.Status().Role == coreconsensus.RoleLeader {
				l = dn
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if l == nil {
		t.Fatal("no leader")
	}
	for i := 0; i < n; i++ {
		propose(t, l.node, fmt.Sprintf("k%d=v%d", i, i))
	}
	c.waitAppliedDisk(nodes)
	return c, nodes
}

func (c *cluster) waitAppliedDisk(nodes []*diskNode) {
	var want uint64
	for _, dn := range nodes {
		want = max(want, dn.node.Status().Commit)
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, dn := range nodes {
		for dn.node.Status().Applied < want && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestRecoverStartsANewHistoryFromOneNode(t *testing.T) {
	c, nodes := threeOnDisk(t, 30)
	for _, dn := range nodes {
		dn.crash(c)
	}
	keep := nodes[0]
	keep.dd = openDir(t, keep.dir, 16<<10)
	st, _ := keep.dd.Log().Stamp()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	next := coreconsensus.ClusterID{6}
	self := coreconsensus.Member{ID: keep.id, Incarnation: st.Incarnation, Slot: st.Slot, SPKI: MemorySPKI(keep.id), Voter: true}
	opt := RecoverOptions{Confirm: next.String(), Operator: "op:recover", WitnessKeys: []ed25519.PublicKey{pub}}
	// Without a fence attestation for the two old voters, it fails closed.
	if err := Recover(keep.dd, next, []coreconsensus.Member{self}, opt); !errors.Is(err, coreconsensus.RecoverRefused) {
		t.Fatalf("unattested recover: %v", err)
	}
	for _, dn := range nodes[1:] {
		opt.Fenced = append(opt.Fenced, coreconsensus.FencedAttestation{
			Voter: dn.id, Method: "powered-off",
			Receipts: [][]byte{ed25519.Sign(priv, ReceiptMessage(c.cid, next, dn.id))},
		})
	}
	if err := Recover(keep.dd, next, []coreconsensus.Member{self}, opt); err != nil {
		t.Fatal(err)
	}
	c.cid = next
	keep.sm = newKV()
	cfg := diskConfig(c, keep)
	cfg.MinVoters = 1
	n, err := NewRaft(cfg)
	if err != nil {
		t.Fatal(err)
	}
	keep.node = n
	defer keep.crash(c)
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
	if _, err := NewRaft(diskConfig(c, old)); !errors.Is(err, coreconsensus.DataDirMismatch) {
		t.Fatalf("old pair restarted under the new cluster: %v", err)
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
		dn.crash(c)
	}
	fresh := &diskNode{dir: t.TempDir()}
	fresh.dd = openDir(t, fresh.dir, 16<<10)
	next := coreconsensus.ClusterID{8}
	id, inc, err := Init(fresh.dd, next, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	fresh.id = id
	self := coreconsensus.Member{ID: id, Incarnation: inc, Slot: "fresh", SPKI: MemorySPKI(id), Voter: true}
	if _, err := RestoreSnapshot(fresh.dd, bytes.NewReader(buf.Bytes()), next, []coreconsensus.Member{self},
		RestoreOptions{Expected: meta, Confirm: next.String(), Operator: "op:restore", MaxEncoded: 1 << 30, MaxDecoded: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	c.cid = next
	fresh.sm = newKV()
	cfg := diskConfig(c, fresh)
	cfg.MinVoters = 1
	n, err := NewRaft(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fresh.node = n
	defer fresh.crash(c)
	if v, _ := fresh.sm.get("k19"); v != "v19" {
		t.Fatalf("restored state: k19=%q", v)
	}
	propose(t, n, "after=restore")
}

// TruncateAndStaySilent: a node quarantined for damage to synced data
// truncates at the damage, stays silent, and votes again only once a leader
// rewrote its log and committed in its own term (R3-09, R4-03).
func TestTruncateAndStaySilentRejoins(t *testing.T) {
	c, nodes := threeOnDisk(t, 40)
	victim := nodes[2]
	victim.crash(c)
	segs, _ := filepath.Glob(filepath.Join(victim.dir, "log", "seg-????????????????"))
	last := segs[len(segs)-1]
	b, err := os.ReadFile(last)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a payload byte of the second record: synced data, a complete
	// batch, valid records after it.
	r0, _ := parseRecord(b)
	r1, _ := parseRecord(b[r0.length:])
	b[r0.length+recHeader+recFixed] ^= 0xff
	_ = r1
	if err := os.WriteFile(last, b, 0o600); err != nil {
		t.Fatal(err)
	}
	victim.dd = openDir(t, victim.dir, 16<<10)
	victim.sm = newKV()
	if _, err := NewRaft(diskConfig(c, victim)); !errors.Is(err, coreconsensus.LogQuarantined) {
		t.Fatalf("damaged log: %v", err)
	}
	if err := ClearQuarantine(victim.dd, TruncateAndStaySilent, "op:silent"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(victim.dir, fileSilent)); err != nil {
		t.Fatal("no silence condition persisted")
	}
	n, err := NewRaft(diskConfig(c, victim))
	if err != nil {
		t.Fatal(err)
	}
	victim.node = n
	var l *diskNode
	for l == nil {
		for _, dn := range nodes[:2] {
			if dn.node != nil && dn.node.Status().Role == coreconsensus.RoleLeader {
				l = dn
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	tok := propose(t, l.node, "after=silence")
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
	_ = tok
	for _, dn := range nodes {
		if dn.node != nil {
			dn.crash(c)
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
	m1 := coreconsensus.Member{ID: id1, Incarnation: inc1, Slot: "a", SPKI: [32]byte{1}, Voter: true}
	m2 := coreconsensus.Member{ID: id2, Incarnation: inc2, Slot: "b", SPKI: [32]byte{2}, Voter: true}
	if err := Bootstrap(context.Background(), Config{Log: d1.Log(), Cluster: cl, ID: id1}, []coreconsensus.Member{m1, m2}); err != nil {
		t.Fatal(err)
	}
	if err := Bootstrap(context.Background(), Config{Log: d2.Log(), Cluster: cl, ID: id2}, []coreconsensus.Member{m2, m1}); err != nil {
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
