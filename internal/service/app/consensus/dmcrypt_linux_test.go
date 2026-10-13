//go:build linux && dmcrypt

package consensus

import (
	"crypto/sha256"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
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
// second one fsync per batch gives (D16: ≥ 10 000 entries/s of 256 B).
func TestDMCryptBench(t *testing.T) {
	dir := dmDir(t)
	d, err := OpenDataDir(dir + "/bench")
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

// TestDMCryptWriter appends batches until it is killed, and prints the last
// index of each batch once its Sync returned: what it acknowledges.
func TestDMCryptWriter(t *testing.T) {
	dir := dmDir(t)
	// Small segments so the cut often lands near a rotation.
	d, err := OpenDataDirWith(dir+"/crash", DataDirOptions{SegmentBytes: 256 << 10})
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
		if d, err = OpenDataDirWith(dir+"/crash", DataDirOptions{SegmentBytes: 256 << 10}); err != nil {
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
