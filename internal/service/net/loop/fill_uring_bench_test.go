//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"fmt"
	"io"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// BenchmarkURingRings_fill measures one request's round trip, fillBenchWave
// at a time, over fillBenchConns keep-alive connections on a loop of one
// ring and of three: with the rings filled in order the two cost the same
// (ns/req), where connections spread over three rings paid each batch the
// secondaries' submits and the eventfd that wakes the primary.
func BenchmarkURingRings_fill(b *testing.B) {
	if err := probeBackend(corenet.LoopBackendIOURing); err != nil {
		b.Skipf("io_uring not present: %v", err)
	}
	if lim := descriptorLimit(); lim < 2*fillBenchConns+256 {
		b.Skipf("RLIMIT_NOFILE %d is too low", lim)
	}
	for _, rings := range []int{1, 3} {
		b.Run(fmt.Sprintf("rings=%d", rings), func(b *testing.B) {
			bc := backendCase{"iouring", corenet.LoopBackendIOURing}
			_, addr := serve(b, bc, &lineH{}, func(c *Config) { c.Value.Loops, c.Value.Rings = 1, rings })
			conns := heard(b, addr, fillBenchConns)
			defer func() {
				for _, c := range conns {
					_ = c.Close()
				}
			}()
			for _, c := range conns {
				_ = c.SetDeadline(time.Time{})
			}
			msg, got := []byte("r\n"), make([]byte, 2)
			at := 0
			for b.Loop() {
				wave := conns[at : at+fillBenchWave]
				at = (at + fillBenchWave) % (fillBenchConns - fillBenchWave)
				for _, c := range wave {
					if _, err := c.Write(msg); err != nil {
						b.Fatal(err)
					}
				}
				for _, c := range wave {
					if _, err := io.ReadFull(c, got); err != nil {
						b.Fatal(err)
					}
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*fillBenchWave), "ns/req")
		})
	}
}
