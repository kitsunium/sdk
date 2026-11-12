//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"fmt"
	"io"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// BenchmarkURingPbufInc measures one request's round trip, fillBenchWave at
// a time over fillBenchConns keep-alive connections on one ring, with the
// buffers consumed incrementally and one buffer a completion, for a short
// line and for a request the size of a small HTTP request (ns/req).
func BenchmarkURingPbufInc(b *testing.B) {
	if err := probeBackend(corenet.LoopBackendIOURing); err != nil {
		b.Skipf("io_uring not present: %v", err)
	}
	if lim := descriptorLimit(); lim < 2*fillBenchConns+256 {
		b.Skipf("RLIMIT_NOFILE %d is too low", lim)
	}
	for _, size := range []int{16, 240} {
		for _, inc := range []bool{true, false} {
			mode := "plain"
			if inc {
				mode = "inc"
			}
			b.Run(fmt.Sprintf("bytes=%d/mode=%s", size, mode), func(b *testing.B) {
				bc := backendCase{"iouring", corenet.LoopBackendIOURing}
				_, addr := serve(b, bc, &lineH{}, func(c *Config) {
					c.Value.Loops, c.Value.Rings = 1, 1
					if !inc {
						c.Disabled = []corenet.Capability{corenet.CapabilityURingPbufInc}
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
				msg := make([]byte, size)
				for i := range msg {
					msg[i] = 'a' + byte(i%26)
				}
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
