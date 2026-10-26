package consensus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// A node whose log reaches MaxLogBytes less LogReserveBytes raises the
// no-space alarm, and once it is committed every write is refused NO_SPACE
// before the log reaches MaxLogBytes. The settings were validated and read
// nowhere: the log grew until a rotation met a full disk and stopped every
// node on STORAGE_FAILED (#515). SnapshotEveryBytes is the other half: a
// log of large entries is cut on its bytes, before SnapshotEvery entries.
// The headroom is three segments of 256 KiB: Config refuses two or less
// (#696).
func TestALogPastItsQuotaRaisesNoSpaceAndRefusesWrites(t *testing.T) {
	const quota, reserve = 1<<20 + 256<<10, 512 << 10
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 1, 5}}
	dn := &diskNode{dir: t.TempDir()}
	dn.dd = openDir(t, dn.dir, 256<<10)
	id, inc, err := Init(dn.dd, c.cid, "s0")
	if err != nil {
		t.Fatal(err)
	}
	dn.id = id
	config := func(everyBytes int64) Config {
		cfg := diskConfig(c, dn)
		cfg.MinVoters, cfg.MaxLogBytes, cfg.LogReserveBytes = 1, quota, reserve
		cfg.SnapshotEvery, cfg.SnapshotEveryBytes, cfg.MaxSnapshotBytes = 1<<30, everyBytes, 1<<30
		return cfg
	}
	dn.sm = newKV()
	members := []coreconsensus.MemberValue{{ID: id, Incarnation: inc, Slot: "s0", SPKI: MemorySPKI(id), Voter: true}}
	if err := Bootstrap(context.Background(), new(config(1<<30)), members); err != nil {
		t.Fatal(err)
	}
	n := must(NewNode(config(1 << 30)))(t)
	dn.node = n
	defer dn.crash(t, c)
	value := bytes.Repeat([]byte("x"), 64<<10)
	var refused error
	for i := 0; i < 64 && refused == nil; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _, err := n.Propose(ctx, fmt.Appendf(nil, "k%d=%s", i, value))
		cancel()
		switch {
		case err == nil:
		case errors.Is(err, coreconsensus.NoSpace):
			refused = err
		case errors.Is(err, coreconsensus.NotLeader):
			i--
			time.Sleep(10 * time.Millisecond)
		default:
			t.Fatalf("propose %d: %v", i, err)
		}
	}
	if refused == nil {
		t.Fatal("4 MiB written past a 1.25 MiB quota and no write was refused NO_SPACE")
	}
	// The node refuses its writes as soon as it finds its disk short; the
	// alarm it proposed then commits.
	waitFor(t, "the no-space alarm", func() bool { return n.Status().NoSpace != 0 })
	if _, _, err := n.Propose(context.Background(), []byte("after=1")); !errors.Is(err, coreconsensus.NoSpace) {
		t.Fatalf("a write under the committed alarm: %v", err)
	}
	if b := dn.dd.d.log.logBytes(); b > quota {
		t.Fatalf("the log holds %d bytes, past MaxLogBytes %d", b, quota)
	}
}

// SnapshotEveryBytes cuts a log of large entries on its bytes, long before
// SnapshotEvery entries: it was validated and read nowhere (#515).
func TestSnapshotEveryBytesCutsOnTheLogsBytes(t *testing.T) {
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 1, 5, 1}, size: 1,
		configure: func(_ *testNode, cfg *Config) {
			cfg.SnapshotEvery, cfg.SnapshotEveryBytes = 1<<30, 128<<10
		},
	})
	tn := c.nodes[0]
	c.leader(5 * time.Second)
	value := bytes.Repeat([]byte("y"), 16<<10)
	var tok coreconsensus.TokenValue
	for i := range 20 {
		tok = propose(t, tn.node, fmt.Sprintf("k%d=%s", i, value))
	}
	c.waitApplied(tok.Index, 10*time.Second)
	cutsSettled(t, tn)
	if metas := must(tn.snaps.List())(t); len(metas) == 0 {
		t.Fatalf("320 KiB applied past SnapshotEveryBytes 128 KiB and no snapshot was cut")
	}
}

// A committed no-space alarm refuses every command NO_SPACE, local or
// forwarded, and admits its clearing; once cleared, writes go through
// (D17, #515).
func TestACommittedNoSpaceAlarmRefusesWritesUntilCleared(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := l.node.admin(ctx, coreconsensus.EntryNoSpaceRaise, wire.AdminValue{Operator: "op:test"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the alarm on the follower", func() bool { return f.node.Status().NoSpace != 0 })
	alarm := l.node.Status().NoSpace
	if _, _, err := l.node.Propose(ctx, []byte("a=1")); !errors.Is(err, coreconsensus.NoSpace) {
		t.Fatalf("a write on the leader under the alarm: %v", err)
	}
	if _, _, err := f.node.Propose(ctx, []byte("b=1")); !errors.Is(err, coreconsensus.NoSpace) {
		t.Fatalf("a write forwarded under the alarm: %v", err)
	}
	pre := coreconsensus.PreconditionSpec{Log: l.node.lineage(), Alarm: alarm}
	if err := l.node.ClearNoSpace(ctx, "op:clear", pre); err != nil {
		t.Fatalf("clear: %v", err)
	}
	propose(t, l.node, "c=1")
}
