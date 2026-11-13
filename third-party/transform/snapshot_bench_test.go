package transform_test

import (
	"context"
	"io"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
)

// snapshotBenchState is the state the snapshot benchmark cuts and installs:
// 256 MiB of the JSON corpus, in 1 MiB chunks as consensus writes them.
const snapshotBenchState int64 = 256 << 20

// snapshotBenchEvery keeps the automatic cuts out of the measured window:
// the benchmark cuts on request, through SaveSnapshot.
const snapshotBenchEvery uint64 = 16384

// BenchmarkSnapshotCutAndInstall measures, through consensus's engine on its
// memory doubles, the two durations #917 asks of a 256 MiB state with each
// codec a node can run: the cut — SaveSnapshot on the leader, the machine's
// stream chunked, compressed and published — and the install — a follower
// restarted behind the leader's compacted log, from its start to the
// snapshot applied: the transfer, the verification of every chunk and the
// restore, each a whole decode. It reports both per operation (cut-s,
// install-s) and the encoded size (encoded-MiB); ns/op is the whole
// scenario and is not the number.
func BenchmarkSnapshotCutAndInstall(b *testing.B) {
	for _, codec := range []struct {
		name  string
		codec func(testing.TB) coretransform.BoundedDecompressor
	}{{"flate", flateCodec}, {"zstd", zstdCodec}} {
		b.Run(codec.name+"/256MiB", func(b *testing.B) {
			var cut, install time.Duration
			var encoded int64
			for range b.N {
				c, i, e := cutAndInstall(b, codec.codec(b))
				cut, install, encoded = cut+c, install+i, e
			}
			b.ReportMetric(cut.Seconds()/float64(b.N), "cut-s")
			b.ReportMetric(install.Seconds()/float64(b.N), "install-s")
			b.ReportMetric(float64(encoded)/(1<<20), "encoded-MiB")
		})
	}
}

// cutAndInstall runs the scenario once: a follower stopped, the leader cut
// on request until its log starts past the follower's and the step function
// holds no entry the follower lacks, the last cut timed,
// then the follower restarted and timed to the snapshot applied.
func cutAndInstall(b *testing.B, codec coretransform.BoundedDecompressor) (cut, install time.Duration, encoded int64) {
	b.Helper()
	b.StopTimer()
	defer b.StartTimer()
	c := newSnapCluster(b, codec, snapshotBenchEvery, snapshotBenchState)
	l := c.leader()
	lag := c.other(l)
	c.kill(lag)
	_, behind := lag.log.Bounds()
	var tok coreconsensus.TokenValue
	var meta coreconsensus.SnapshotMetaValue
	// Two snapshots are kept and the step function holds its log back to
	// the older (ADR 0152 R6-02): a follower behind the store's first index
	// is still fed by appends until a cut past the second compacts that.
	for round := 1; ; round++ {
		tok = c.propose(l, 8)
		start := time.Now()
		b.StartTimer()
		m, err := l.node.SaveSnapshot(context.Background(), coreconsensus.Operator("automation:bench"), io.Discard)
		b.StopTimer()
		if err != nil {
			b.Fatalf("SaveSnapshot: %v", err)
		}
		cut, meta = time.Since(start), m
		if first, _ := l.log.Bounds(); round >= 3 && first > behind+1 && meta.Position.Index >= tok.Index {
			break
		}
	}
	// The other follower cuts too, untimed: a leadership that moved to it
	// must install as well, never feed the follower by appends.
	other := c.other(l, lag)
	if _, err := other.node.SaveSnapshot(context.Background(), coreconsensus.Operator("automation:bench"), io.Discard); err != nil {
		b.Fatalf("SaveSnapshot on the other follower: %v", err)
	}
	start := time.Now()
	b.StartTimer()
	c.restart(lag)
	c.waitApplied(lag, meta.Position.Index, 10*time.Minute)
	b.StopTimer()
	install = time.Since(start)
	// The follower's log moves past the snapshot, which only its install
	// does; the store does so once the restore answered.
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		first, last := lag.log.Bounds()
		if first > meta.Position.Index {
			break
		}
		if time.Now().After(deadline) {
			b.Fatalf("the follower's log [%d, %d] never moved past snapshot %s", first, last, meta.ID)
		}
	}
	return cut, install, meta.EncodedSize
}
