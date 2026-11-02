//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — io_uring's receive bundles (IORING_RECVSEND_BUNDLE, ADR
// 0205): one completion fills consecutive buffers of its ring, its bid the
// first's, BUF_MORE the last's under incremental consumption.
package loop

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestBufs_spans pins uring.spans on a ring of eight 64-byte buffers, in
// both modes: a completion's bytes go buffer by buffer in ring order from
// its bid; one buffer a completion consumes it whole; incrementally the
// first starts where the last completion stopped and only the last may be
// kept; and the order follows the ring after buffers went back scrambled.
func TestBufs_spans(t *testing.T) {
	for _, inc := range []bool{false, true} {
		t.Run(fmt.Sprintf("inc=%v", inc), func(t *testing.T) {
			u, step, errno := openURing(probeEntries, 0)
			if errno != 0 {
				t.Skipf("io_uring %s: %v", step, errno)
			}
			defer u.close()
			if errno := u.setupBuffers(8, 64, 8, inc); errno != 0 {
				t.Skipf("buffer ring: %v", errno)
			}
			if u.bufInc != inc {
				t.Skipf("incremental consumption %v: %v", inc, u.incRefused)
			}
			lens := func(segs [][]byte) []int {
				out := make([]int, len(segs))
				for i, s := range segs {
					out[i] = len(s)
				}
				return out
			}
			off := func(bid uint16, seg []byte) int { return int(u.bufSize) - cap(seg) }
			if !inc {
				segs, bids, kept := u.spans(0, 150, false, nil, nil)
				if fmt.Sprint(lens(segs), bids, kept) != "[64 64 22] [0 1 2] false" {
					t.Fatalf("a bundle of 150 from 0: %v %v %v", lens(segs), bids, kept)
				}
				// Back scrambled: 2, 0, 1 after 7, at entries 8, 9, 10.
				for _, b := range []uint16{2, 0, 1} {
					u.addBuf(b)
				}
				segs, bids, _ = u.spans(7, 100, false, nil, nil)
				if fmt.Sprint(lens(segs), bids) != "[64 36] [7 2]" {
					t.Fatalf("a bundle of 100 from 7: %v %v", lens(segs), bids)
				}
				return
			}
			segs, bids, kept := u.spans(0, 10, true, nil, nil)
			if fmt.Sprint(lens(segs), bids, kept) != "[10] [0] true" {
				t.Fatalf("10 bytes into 0: %v %v %v", lens(segs), bids, kept)
			}
			segs, bids, kept = u.spans(0, 150, true, nil, nil)
			if fmt.Sprint(lens(segs), bids, kept, off(0, segs[0])) != "[54 64 32] [0 1 2] true 10" {
				t.Fatalf("a bundle of 150 from 0 at 10: %v %v %v at %d", lens(segs), bids, kept, off(0, segs[0]))
			}
			segs, bids, kept = u.spans(2, 32, false, nil, nil)
			if fmt.Sprint(lens(segs), bids, kept, off(2, segs[0])) != "[32] [2] false 32" {
				t.Fatalf("the rest of 2: %v %v %v at %d", lens(segs), bids, kept, off(2, segs[0]))
			}
		})
	}
}

// TestURingRecvBundle_spansBuffers holds the loop while one connection sends
// lines longer than a buffer, on a ring of sixteen 256-byte buffers filled
// with 0xAA and returned scrambled, in both buffer modes: every echo is
// exact, some completion fills several buffers, every buffer of a completion
// but its first starts at its first byte, and no buffer goes back while the
// kernel keeps it.
func TestURingRecvBundle_spansBuffers(t *testing.T) {
	const lines, size, lineLen = 6, 256, 900
	eachBackend(t, func(t *testing.T, b backendCase) {
		onlyIOURing(t, b)
		eachBufMode(t, func(t *testing.T, inc bool) {
			var mu sync.Mutex
			var faults []string
			spanned, inBundle := 0, 0
			open := map[uint16]bool{}
			var hold atomic.Bool
			release, held := make(chan struct{}), make(chan struct{}, 1)
			h := &lineH{}
			h.batchEnd = func(corenet.LoopIO) {
				if hold.CompareAndSwap(true, false) {
					held <- struct{}{}
					<-release
				}
			}
			hooks := loopHooks{bufs: 16, bufSize: size, fill: true, shuffle: true}
			hooks.bufSeen = func(bid uint16, off, n int, kept, first, returned bool) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case returned:
					if open[bid] {
						faults = append(faults, fmt.Sprintf("bid %d returned while the kernel kept it", bid))
					}
				case first:
					inBundle = 1
					open[bid] = kept
				default:
					inBundle++
					if inBundle == 2 {
						spanned++
					}
					if off != 0 {
						faults = append(faults, fmt.Sprintf("bid %d, a bundle's buffer past its first, starts at %d", bid, off))
					}
					open[bid] = kept
				}
			}
			g, addr := serve(t, b, h, func(c *Config) {
				c.Value.Loops, c.Value.Rings = 1, 1
				c.hooks = hooks
				c.Value.Capabilities = []corenet.Capability{corenet.CapabilityURingRecvBundle}
				bufMode(c, inc)
			})
			requireMode(t, g, inc)
			if !bundled(g) {
				t.Skipf("uring-recv-bundle %s on %s", capStatus(g, corenet.CapabilityURingRecvBundle), kernelRelease())
			}
			c := dial(t, addr)
			_ = c.SetDeadline(time.Now().Add(20 * time.Second))
			r := bufio.NewReader(c)
			hold.Store(true)
			_, _ = c.Write([]byte("hold\n"))
			<-held
			var want bytes.Buffer
			for i := range lines {
				line := append(bytes.Repeat([]byte{byte('a' + i)}, lineLen), '\n')
				want.Write(line)
				if _, err := c.Write(line); err != nil {
					t.Fatal(err)
				}
			}
			time.Sleep(50 * time.Millisecond)
			close(release)
			if got, err := r.ReadString('\n'); err != nil || got != "hold\n" {
				t.Fatalf("the hold's echo: %q, %v", got, err)
			}
			got := make([]byte, want.Len())
			if _, err := io.ReadFull(r, got); err != nil || !bytes.Equal(got, want.Bytes()) {
				t.Fatalf("%d bytes of echo, %v: not the lines sent", len(got), err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(faults) > 0 {
				t.Fatalf("%d faults, the first: %s", len(faults), faults[0])
			}
			t.Logf("%d completions filled several buffers", spanned)
			if spanned == 0 {
				t.Fatal("no completion filled more than one buffer: bundles were not used")
			}
		})
	})
}

// TestURingRecvBundle_refused plays a kernel that refuses receive bundles to
// a group that opted into them: it serves on io_uring with one buffer a
// completion, and the row reads missing with the refusal.
func TestURingRecvBundle_refused(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		onlyIOURing(t, b)
		g, addr := serve(t, b, &lineH{}, func(c *Config) {
			c.hooks = loopHooks{failStage: "recv_bundle"}
			c.Value.Capabilities = []corenet.Capability{corenet.CapabilityURingRecvBundle}
		})
		if bundled(g) {
			t.Fatal("recvs are bundles after the kernel refused them")
		}
		row := capRow(t, g.State(), corenet.CapabilityURingRecvBundle)
		if row.Status != statusMissing || row.Reason == "" {
			t.Fatalf("uring-recv-bundle row %+v, want missing with the refusal", row)
		}
		for range 4 {
			echoBigClose(t, addr)
		}
	})
}

// TestURingRecvBundle_optIn pins the class A-code shape: a group that did not
// opt in arms no bundle and its row reads available or absent; the
// operator's switch wins over an opt-in, the row disabled.
func TestURingRecvBundle_optIn(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		onlyIOURing(t, b)
		plain, _ := serve(t, b, &lineH{}, nil)
		if bundled(plain) {
			t.Fatal("recvs are bundles without an opt-in")
		}
		if st := capStatus(plain, corenet.CapabilityURingRecvBundle); st != statusAvailable && st != statusAbsent {
			t.Fatalf("uring-recv-bundle %s without an opt-in, want available or absent", st)
		}
		off, addr := serve(t, b, &lineH{}, func(c *Config) {
			c.Value.Capabilities = []corenet.Capability{corenet.CapabilityURingRecvBundle}
			c.Disabled = []corenet.Capability{corenet.CapabilityURingRecvBundle}
		})
		if bundled(off) || capStatus(off, corenet.CapabilityURingRecvBundle) != statusDisabled {
			t.Fatalf("switched off over an opt-in: bundled %v, row %s", bundled(off), capStatus(off, corenet.CapabilityURingRecvBundle))
		}
		echoBigClose(t, addr)
	})
}

// bundled reports whether g's first loop arms its recvs as bundles.
func bundled(g *Group) bool {
	return uringOf(g).uses.recvFlags&recvBundle != 0
}

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
