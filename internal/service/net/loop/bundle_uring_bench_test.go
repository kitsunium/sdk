//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"bytes"
	"fmt"
	"io"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// BenchmarkURingRecvBundle measures one request's round trip, fillBenchWave
// at a time over fillBenchConns keep-alive connections on one ring, with
// receive bundles on and switched off, for a small request and for one
// larger than a buffer (ns/req).
func BenchmarkURingRecvBundle(b *testing.B) {
	if err := probeBackend(corenet.LoopBackendIOURing); err != nil {
		b.Skipf("io_uring not present: %v", err)
	}
	if lim := descriptorLimit(); lim < 2*fillBenchConns+256 {
		b.Skipf("RLIMIT_NOFILE %d is too low", lim)
	}
	for _, size := range []int{240, 8192} {
		for _, on := range []bool{true, false} {
			b.Run(fmt.Sprintf("bytes=%d/bundle=%v", size, on), func(b *testing.B) {
				bc := backendCase{"iouring", corenet.LoopBackendIOURing}
				_, addr := serve(b, bc, &lineH{}, func(c *Config) {
					c.Value.Loops, c.Value.Rings = 1, 1
					if on {
						c.Value.Capabilities = []corenet.Capability{corenet.CapabilityURingRecvBundle}
					}
				})
				conns := heard(b, addr, fillBenchConns)
				defer func() {
					for _, c := range conns {
						_ = c.Close()
					}
				}()
				for _, c := range conns {
					_ = c.SetDeadline(time.Time{})
				}
				msg := bytes.Repeat([]byte{'r'}, size)
				msg[size-1] = '\n'
				got := make([]byte, size)
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
}
