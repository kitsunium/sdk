package consensus

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// backupFixture is a three-node cluster that cuts no snapshot on its own,
// so the tail after an export stays in the log for the appends.
func backupFixture(t *testing.T) (*cluster, []*diskNode) {
	t.Helper()
	return threeOnDiskTuned(t, 10, func(cfg *Config) {
		cfg.SnapshotEvery, cfg.SnapshotEveryBytes = 1<<20, 64<<20
	})
}

// proposeMany writes n distinct keys from prefix, concurrently, to
// whichever node leads, and returns the highest index they committed at.
func proposeMany(t *testing.T, nodes []*diskNode, prefix string, n int) uint64 {
	t.Helper()
	const workers = 128
	var mu sync.Mutex
	var last uint64
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := w; i < n; i += workers {
				tok := proposeOnDisk(t, nodes, fmt.Sprintf("%s%d=v%d", prefix, i, i))
				mu.Lock()
				last = max(last, tok.Index)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return last
}

// backupFiles is what one run of the backup lifecycle wrote.
type backupFiles struct {
	export, tail1, tail2 []byte
	point                BackupPointValue
	state                map[string]string
}

// writeBackup exports a cluster, commits 2 × half entries after the cut
// with an append after each half, and returns the files and the backup
// node's state at the last append's point.
func writeBackup(t *testing.T, half int) backupFiles {
	t.Helper()
	c, nodes := backupFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	l := leaderOf(t, nodes)
	var f backupFiles
	var ex, a1, a2 bytes.Buffer
	p0, err := l.node.ExportBackup(ctx, "op:backup", &ex)
	if err != nil {
		t.Fatal(err)
	}
	proposeMany(t, nodes, "a", half)
	p1, err := l.node.AppendBackup(ctx, p0, &a1)
	if err != nil {
		t.Fatal(err)
	}
	last := proposeMany(t, nodes, "b", half)
	f.point, err = l.node.AppendBackup(ctx, p1, &a2)
	if err != nil {
		t.Fatal(err)
	}
	if f.point.Position.Index < last || p1.Position.Index <= p0.Position.Index || f.point.Position.Index-p0.Position.Index < uint64(2*half) {
		t.Fatalf("points %+v, %+v, %+v; the last write at %d", p0, p1, f.point, last)
	}
	l.sm.mu.Lock()
	f.state = maps.Clone(l.sm.m)
	l.sm.mu.Unlock()
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	f.export, f.tail1, f.tail2 = ex.Bytes(), a1.Bytes(), a2.Bytes()
	return f
}

// restoreOptions are the bounds and confirmation of a restore to next.
func restoreOptions(next coreconsensus.ClusterID) *RestoreOptions {
	return &RestoreOptions{Confirm: next.String(), Operator: "op:restore", MaxEncoded: 1 << 30, MaxDecoded: 1 << 30}
}

// An export and two appends, 10 000 entries after the cut, restore, offline, a new cluster whose state is
// the source's at the last append's point, and that cluster takes writes.
func TestABackupOfAnExportAndTwoAppendsRestoresTheState(t *testing.T) {
	f := writeBackup(t, 5000)
	next := coreconsensus.ClusterID{9}
	p := &BackupParams{Export: bytes.NewReader(f.export), Tails: []io.Reader{bytes.NewReader(f.tail1), bytes.NewReader(f.tail2)}}
	at, err := VerifyBackup(p, restoreOptions(next))
	if err != nil || at != f.point {
		t.Fatalf("verify: %+v, %v; want %+v", at, err, f.point)
	}
	fresh, self := freshJoining(t, next)
	p = &BackupParams{Export: bytes.NewReader(f.export), Tails: []io.Reader{bytes.NewReader(f.tail1), bytes.NewReader(f.tail2)}, Machine: newKV()}
	got, err := RestoreBackup(fresh.dd, p, next, []coreconsensus.MemberValue{self}, restoreOptions(next))
	if err != nil {
		t.Fatal(err)
	}
	if got.Position != f.point.Position || got.Log.KeptIndex != f.point.Position.Index || got.Log.Cluster != next {
		t.Fatalf("restored at %+v, log %+v; want %+v", got.Position, got.Log, f.point)
	}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: next}
	fresh.sm = newKV()
	cfg := diskConfig(c, fresh)
	cfg.MinVoters = 1
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fresh.node = n
	defer fresh.crash(t, c)
	fresh.sm.mu.Lock()
	restored := maps.Clone(fresh.sm.m)
	fresh.sm.mu.Unlock()
	if len(restored) != len(f.state) || !maps.Equal(restored, f.state) {
		t.Fatalf("restored %d keys, the source held %d at the backup's point", len(restored), len(f.state))
	}
	propose(t, n, "after=restore")
}

// A backup whose tail does not continue the snapshot's chain is refused by
// VerifyBackup and by RestoreBackup before anything is written: a missing
// append, an entry whose chain value was rewritten under a valid record
// sum, a record whose bytes rotted.
func TestABackupWhoseTailBreaksTheChainIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	f := writeBackup(t, 100)
	next := coreconsensus.ClusterID{9}
	_, _, tail, err := scanBackup(&BackupParams{Export: bytes.NewReader(f.export), Tails: []io.Reader{bytes.NewReader(f.tail1)}}, true)
	if err != nil {
		t.Fatal(err)
	}
	broken := rewriteTail(t, f.export, tail)
	rotted := bytes.Clone(f.tail1)
	rotted[len(rotted)/2] ^= 1
	fresh, self := freshJoining(t, next)
	for name, c := range map[string]struct {
		tails [][]byte
		code  errs.Code
	}{
		"an append missing": {tails: [][]byte{f.tail2}, code: coreconsensus.CodeRestoreRefused},
		"a broken chain":    {tails: [][]byte{broken, f.tail2}, code: coreconsensus.CodeRestoreRefused},
		"a rotted record":   {tails: [][]byte{rotted, f.tail2}, code: coreconsensus.CodeSnapshotCorrupt},
	} {
		readers := func() []io.Reader {
			var rs []io.Reader
			for _, b := range c.tails {
				rs = append(rs, bytes.NewReader(b))
			}
			return rs
		}
		if _, err := VerifyBackup(&BackupParams{Export: bytes.NewReader(f.export), Tails: readers()}, restoreOptions(next)); !errs.HasCode(err, c.code) {
			t.Fatalf("%s: verify %v", name, err)
		}
		p := &BackupParams{Export: bytes.NewReader(f.export), Tails: readers(), Machine: newKV()}
		if _, err := RestoreBackup(fresh.dd, p, next, []coreconsensus.MemberValue{self}, restoreOptions(next)); !errs.HasCode(err, c.code) {
			t.Fatalf("%s: restore %v", name, err)
		}
	}
	// Nothing was written: the same directory restores the sound backup.
	p := &BackupParams{Export: bytes.NewReader(f.export), Tails: []io.Reader{bytes.NewReader(f.tail1), bytes.NewReader(f.tail2)}, Machine: newKV()}
	if _, err := RestoreBackup(fresh.dd, p, next, []coreconsensus.MemberValue{self}, restoreOptions(next)); err != nil {
		t.Fatalf("the refusals wrote to the directory: %v", err)
	}
	if err := fresh.dd.Close(); err != nil {
		t.Fatal(err)
	}
}

// rewriteTail writes tail as an append after export's snapshot, its first
// entry's chain value changed and every record sum valid.
func rewriteTail(t *testing.T, export []byte, tail []coreconsensus.EntryValue) []byte {
	t.Helper()
	_, base, _, err := scanBackup(&BackupParams{Export: bytes.NewReader(export)}, false)
	if err != nil {
		t.Fatalf("scan the export: %v", err)
	}
	var entries []coreconsensus.EntryValue
	for _, e := range tail {
		if e.Position.Index > base.Position.Index {
			entries = append(entries, e)
		}
	}
	entries[0].ChainSum ^= 1
	var out bytes.Buffer
	bw := &backupWriter{w: &out}
	if err := bw.header(backupTail); err != nil {
		t.Fatal(err)
	}
	if err := bw.record(recEntries, wire.AppendMessage(nil, &coreconsensus.MessageValue{Kind: coreconsensus.MsgApp, Entries: entries})); err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if err := bw.record(recTrailer, appendTrailer(nil, base, BackupPointValue{Position: last.Position, ChainTip: last.ChainSum}, uint64(len(entries)))); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
