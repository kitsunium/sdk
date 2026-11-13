package multi_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/app/consensus/multi"
)

// benchCallers and benchCmdBytes are the shape of the gate of #926: 1 000
// callers proposing commands of 64 bytes on a three-node memory cluster.
const (
	benchCallers  = 1000
	benchCmdBytes = 64
)

// BenchmarkProposeCallers commits b.N commands of 64 bytes from 1 000
// concurrent callers, each command its own entry (unpacked) or packed by a
// Packer with the others waiting at the same moment (packed). It reports the
// commands and entries committed per second and the command bytes per second.
func BenchmarkProposeCallers(b *testing.B) {
	for _, packed := range []bool{false, true} {
		name := map[bool]string{false: "unpacked", true: "packed"}[packed]
		b.Run(name, func(b *testing.B) { benchPropose(b, packed) })
	}
}

func benchPropose(b *testing.B, packed bool) {
	c := newMemCluster(b, packed)
	l := c.leader(b)
	n := c.nodes[l]
	propose := func(cmd []byte) error {
		_, _, err := n.Propose(context.Background(), cmd)
		return err
	}
	if packed {
		p := multi.NewPacker(n, multi.Config{})
		b.Cleanup(func() { _ = p.Close(context.Background()) })
		propose = func(cmd []byte) error {
			_, err := p.Propose(context.Background(), cmd)
			return err
		}
	}
	first := n.Status().Commit
	var left atomic.Int64
	left.Store(int64(b.N))
	var wg sync.WaitGroup
	b.ResetTimer()
	start := time.Now()
	for i := range benchCallers {
		wg.Go(func() {
			cmd := make([]byte, benchCmdBytes)
			copy(cmd, "k")
			cmd[1] = byte('0' + i%10)
			cmd[2] = '='
			for left.Add(-1) >= 0 {
				if err := propose(cmd); err != nil {
					b.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	b.StopTimer()
	entries := float64(n.Status().Commit - first)
	b.ReportMetric(float64(b.N)/elapsed, "cmds/s")
	b.ReportMetric(entries/elapsed, "entries/s")
	b.ReportMetric(float64(b.N*benchCmdBytes)/elapsed, "cmdbytes/s")
}
