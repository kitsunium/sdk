//go:build linux && dmcrypt

package consensus

import (
	"crypto/sha256"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The dm-crypt lane of D15 and R9-03. It runs only under the dmcrypt tag,
// inside the privileged Linux container testdata/dmcrypt/run.sh starts,
// over a filesystem mounted on a LUKS2 dm-crypt device, as the nodes run.
// DMCRYPT_DIR names the data directory.

func dmDir(t *testing.T) string {
	d := os.Getenv("DMCRYPT_DIR")
	if d == "" {
		t.Skip("DMCRYPT_DIR not set: run testdata/dmcrypt/run.sh")
	}
	return d
}

// payload is entry i's deterministic content, so a verifier can tell a torn
// or foreign entry from the one that was written.
func payload(i uint64) []byte {
	size := 100 + int(i*7919%2900)
	seed := sha256.Sum256([]byte(strconv.FormatUint(i, 10)))
	out := make([]byte, 0, size)
	for len(out) < size {
		out = append(out, seed[:]...)
	}
	return out[:size]
}

func pct(ds []time.Duration, p float64) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[min(len(s)-1, int(float64(len(s))*p))]
}

// TestDMCryptBench measures one Sync per batch over dm-crypt: 64 × 256 B
// (D15's row) and 1 × 256 B, p50, p90, p99 and max, and the entries per
// second one Sync per batch gives, two fdatasyncs since #417 (D16: ≥ 10 000
// entries/s of 256 B).
func TestDMCryptBench(t *testing.T) {
	dir := dmDir(t)
	d, err := NewDataDir(dir + "/bench")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	l := d.Log()
	_, last := l.Bounds()
	data := make([]byte, 256)
	for _, shape := range []struct {
		name  string
		batch int
		n     int
	}{{"batch64x256B", 64, 2000}, {"batch1x256B", 1, 2000}} {
		var lat []time.Duration
		start := time.Now()
		for k := 0; k < shape.n; k++ {
			es := make([]coreconsensus.EntryValue, shape.batch)
			for i := range es {
				last++
				es[i] = coreconsensus.EntryValue{
					Position: coreconsensus.PositionValue{Term: 1, Index: last},
					Kind:     coreconsensus.EntryCommand, BodySum: wire.BodySum(data), Data: data,
				}
			}
			if err := l.Append(es, nil); err != nil {
				t.Fatal(err)
			}
			t0 := time.Now()
			if err := l.Sync(); err != nil {
				t.Fatal(err)
			}
			lat = append(lat, time.Since(t0))
		}
		total := time.Since(start)
		eps := float64(shape.batch*shape.n) / total.Seconds()
		fmt.Printf("BENCH %s n=%d p50=%v p90=%v p99=%v max=%v entries_per_s=%.0f\n", shape.name, shape.n,
			pct(lat, 0.50), pct(lat, 0.90), pct(lat, 0.99), pct(lat, 1), eps)
	}
}

// devWritten is the bytes the device DMCRYPT_STATDEV names (a block device
// under /sys/block) was sent, journal and metadata included: the seventh
// field of its stat, in 512-byte sectors. It is -1 without the variable.
func devWritten(t *testing.T) int64 {
	dev := os.Getenv("DMCRYPT_STATDEV")
	if dev == "" {
		return -1
	}
	b, err := os.ReadFile("/sys/block/" + dev + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(b))
	if len(f) < 7 {
		t.Fatalf("stat of %s: %q", dev, b)
	}
	n, err := strconv.ParseInt(f[6], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n * 512
}

// TestDMCryptPreallocBench is G1's row "Append 64 x 256 B + Sync" per
// filesystem and per Preallocate (#922): 64 MiB segments, the default,
// filled until three more were published, each Sync timed, and what the
// device was sent per segment. The segment prepared at the open is
// finished before the count starts, and the one the last publication
// prepared before it stops, so each published segment counts its own
// preallocation once. The mode is CONSENSUS_PREALLOCATE's.
func TestDMCryptPreallocBench(t *testing.T) {
	dir := dmDir(t)
	mode := "zerofill"
	if preallocEverywhere {
		mode = "fallocate"
	}
	d, err := NewDataDir(dir + "/prealloc-" + mode)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	l := d.d.log
	if preallocEverywhere && l.prealloc != PreallocateFallocate {
		t.Fatalf("fallocate asked for, the log zero-fills: %s", l.preallocWhy)
	}
	waitPrepared := func() {
		if p := l.prep; p != nil {
			<-p.done
		}
	}
	waitPrepared()
	_, last := l.Bounds()
	data := make([]byte, 256)
	start := l.segs[len(l.segs)-1].n
	before := devWritten(t)
	var lat []time.Duration
	var dataBytes int64
	for l.segs[len(l.segs)-1].n < start+3 {
		es := make([]coreconsensus.EntryValue, 64)
		for i := range es {
			last++
			es[i] = coreconsensus.EntryValue{
				Position: coreconsensus.PositionValue{Term: 1, Index: last},
				Kind:     coreconsensus.EntryCommand, BodySum: wire.BodySum(data), Data: data,
			}
		}
		if err := l.Append(es, nil); err != nil {
			t.Fatal(err)
		}
		dataBytes += int64(len(l.pending))
		t0 := time.Now()
		if err := l.Sync(); err != nil {
			t.Fatal(err)
		}
		lat = append(lat, time.Since(t0))
	}
	waitPrepared()
	segs := int64(l.segs[len(l.segs)-1].n - start)
	written := devWritten(t)
	perSeg := int64(-1)
	if before >= 0 {
		perSeg = (written - before) / segs
	}
	fmt.Printf("PREALLOC fs=%s mode=%s syncs=%d segments=%d p50=%v p90=%v p99=%v max=%v device_bytes_per_segment=%d record_bytes_per_segment=%d\n",
		os.Getenv("DMCRYPT_FS"), mode, len(lat), segs, pct(lat, 0.50), pct(lat, 0.90), pct(lat, 0.99), pct(lat, 1),
		perSeg, dataBytes/segs)
}

// syncPathWindow is one window of TestDMCryptSyncPathBench: n Syncs of
// 64 x 256 B on l, their entries per second and their p99.
func syncPathWindow(t *testing.T, l *fileLog, last *uint64, n int) (eps float64, p99 time.Duration) {
	t.Helper()
	data := make([]byte, 256)
	lat := make([]time.Duration, 0, n)
	start := time.Now()
	for k := 0; k < n; k++ {
		es := make([]coreconsensus.EntryValue, 64)
		for i := range es {
			*last++
			es[i] = coreconsensus.EntryValue{
				Position: coreconsensus.PositionValue{Term: 1, Index: *last},
				Kind:     coreconsensus.EntryCommand, BodySum: wire.BodySum(data), Data: data,
			}
		}
		if err := l.Append(es, nil); err != nil {
			t.Fatal(err)
		}
		t0 := time.Now()
		if err := l.Sync(); err != nil {
			t.Fatal(err)
		}
		lat = append(lat, time.Since(t0))
	}
	return float64(64*n) / time.Since(start).Seconds(), pct(lat, 0.99)
}

// orderStat is benchstat's distribution-free 95 % interval of rs, the
// order statistics ADR 0190 D2 holds a gate on: the 2nd and 9th of ten.
func orderStat(rs []float64) (lo, hi float64) {
	s := slices.Clone(rs)
	slices.Sort(s)
	k := 0
	if len(s) >= 10 {
		k = 1
	}
	return s[k], s[len(s)-1-k]
}

// TestDMCryptSyncPathBench is G1's row "Append 64 x 256 B + Sync" for each
// SyncPath against SyncFdatasync, in ADR 0190's shape (#923): engines A
// (SyncFdatasync) and B (SyncRWFDsync) are two logs of one process on the
// same filesystem, a round is 2 x 8 windows in the order A, B, B, A, ...,
// so a drift of the host lands on both, and it reports B over A of the
// entries per second and of the Sync p99. DMCRYPT_ROUNDS rounds (10 by
// default) give the interval of the ratios, which the gate judges: B may
// be the default on this device only if the entries/s ratio's lower bound
// is >= 1.00 and the p99 ratio's upper bound is <= 1.00.
func TestDMCryptSyncPathBench(t *testing.T) {
	dir := dmDir(t)
	if syncEverywhere {
		t.Fatal("run without CONSENSUS_SYNC_PATH: engine A is SyncFdatasync")
	}
	rounds := 10
	if v, err := strconv.Atoi(os.Getenv("DMCRYPT_ROUNDS")); err == nil && v > 0 {
		rounds = v
	}
	const perWindow = 200
	open := func(name string, mode SyncPath) *fileLog {
		d, err := NewDataDirWith(dir+"/syncpath-"+name, DataDirOptions{SyncPath: mode})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { abandon(d.Close()) })
		return d.d.log
	}
	a, b := open("fdatasync", SyncFdatasync), open("rwfdsync", SyncRWFDsync)
	if b.syncPath != SyncRWFDsync {
		t.Fatalf("RWF_DSYNC asked for, the log syncs with fdatasync: %s", b.syncWhy)
	}
	logs := [2]*fileLog{a, b}
	var lasts [2]uint64
	_, lasts[0] = a.Bounds()
	_, lasts[1] = b.Bounds()
	var epsR, p99R []float64
	for r := 0; r < rounds; r++ {
		var eps, p99 [2]float64
		for w := 0; w < 16; w++ {
			// A, B, B, A, A, B, B, A, ...: engine (w+1)/2 % 2.
			e := ((w + 1) / 2) % 2
			v, p := syncPathWindow(t, logs[e], &lasts[e], perWindow)
			eps[e] += v / 8
			p99[e] += float64(p) / 8
		}
		epsR = append(epsR, eps[1]/eps[0])
		p99R = append(p99R, p99[1]/p99[0])
		fmt.Printf("SYNCPATH fs=%s pair=fdatasync:rwfdsync round=%d a:entries_per_s=%.0f b:entries_per_s=%.0f ratio:entries_per_s=%.3f a:p99=%v b:p99=%v ratio:p99=%.3f\n",
			os.Getenv("DMCRYPT_FS"), r, eps[0], eps[1], epsR[r], time.Duration(p99[0]), time.Duration(p99[1]), p99R[r])
	}
	elo, ehi := orderStat(epsR)
	plo, phi := orderStat(p99R)
	verdict := "keep-fdatasync"
	if elo >= 1 && phi <= 1 {
		verdict = "rwfdsync-may-be-default"
	}
	fmt.Printf("SYNCPATH_GATE fs=%s rounds=%d ratio:entries_per_s=[%.3f,%.3f] ratio:p99=[%.3f,%.3f] gate=entries_per_s>=1.00,p99<=1.00 verdict=%s\n",
		os.Getenv("DMCRYPT_FS"), rounds, elo, ehi, plo, phi, verdict)
}

// TestDMCryptWriter appends batches until it is killed, and prints the last
// index of each batch once its Sync returned: what it acknowledges.
func TestDMCryptWriter(t *testing.T) {
	dir := dmDir(t)
	// Small segments so the cut often lands near a rotation.
	d, err := NewDataDirWith(dir+"/crash", DataDirOptions{SegmentBytes: 256 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DMCRYPT_STOP_AT_SEAL") == "1" {
		// The cut lands between a durable seal and the next segment's
		// publication: the window of review H2.
		d.d.log.afterSeal = func() {
			fmt.Fprintln(os.Stdout, "SEALED")
			select {}
		}
	}
	l := d.Log()
	_, last := l.Bounds()
	hs, _ := l.HardState()
	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 1))
	ackFirst := os.Getenv("DMCRYPT_ACK_BEFORE_SYNC") == "1"
	for {
		n := 1 + rng.IntN(32)
		es := make([]coreconsensus.EntryValue, n)
		for i := range es {
			last++
			p := payload(last)
			es[i] = coreconsensus.EntryValue{
				Position: coreconsensus.PositionValue{Term: 1, Index: last},
				Kind:     coreconsensus.EntryCommand, BodySum: wire.BodySum(p), Data: p,
			}
		}
		hs.Term, hs.Commit = 1, last
		if err := l.Append(es, &hs); err != nil {
			t.Fatal(err)
		}
		if ackFirst {
			// The negative control: acknowledged before the sync, as
			// raft-sql-poc's unsafeAckBeforeSync did. The lane must catch it.
			fmt.Fprintf(os.Stdout, "SYNCED %d\n", last)
		}
		if err := l.Sync(); err != nil {
			// The device was cut: nothing more is acknowledged.
			fmt.Fprintf(os.Stdout, "SYNCFAIL %v\n", err)
			select {}
		}
		if !ackFirst {
			fmt.Fprintf(os.Stdout, "SYNCED %d\n", last)
		}
	}
}

// TestDMCryptVerify reopens the log after a power cut: it must not be
// quarantined, must hold every acknowledged entry, each one intact. It opens
// the log twice: a repair the first open made must leave a log the second
// open accepts (review H2).
func TestDMCryptVerify(t *testing.T) {
	dir := dmDir(t)
	acked, _ := strconv.ParseUint(os.Getenv("DMCRYPT_ACKED"), 10, 64)
	var d DataDir
	for pass := 1; pass <= 2; pass++ {
		if pass > 1 {
			d.Close()
		}
		var err error
		if d, err = NewDataDirWith(dir+"/crash", DataDirOptions{SegmentBytes: 256 << 10}); err != nil {
			t.Fatalf("open %d: %v", pass, err)
		}
		if err := d.d.log.checkMarkers(); err != nil {
			t.Fatalf("open %d after the cut: %v", pass, err)
		}
	}
	defer d.Close()
	first, last := d.Log().Bounds()
	if last < acked {
		t.Fatalf("lost acknowledged entries: last %d < acked %d", last, acked)
	}
	es, err := d.Log().Entries(first, last+1, 1<<40, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if string(e.Data) != string(payload(e.Position.Index)) {
			t.Fatalf("entry %d is not the one written", e.Position.Index)
		}
	}
	_, _, tails := d.d.log.logStats()
	fmt.Printf("VERIFY ok first=%d last=%d acked=%d unacked_kept=%d tail_truncated=%d\n",
		first, last, acked, last-acked, tails)
}
