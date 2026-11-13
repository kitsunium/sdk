//go:build linux

// Sparse: the sparse keep-alive load paired over the veth pair (#979). Busy
// polling removes the wake a request costs on an idle lane, and polls only
// the NAPI instances of its sockets: loopback has none, a veth pair with GRO
// on its far end has one, so the load runs where L3 runs — the child in the
// namespace of scripts/ci/net-veth.sh, on the far address — or is skipped.
// Engine A is a loop that sleeps in its wait, B the same with busy-poll
// opted into; a round reports each one's CPU a request and the 99th
// percentile of a request's round trip, as the client times it, and their
// ratios B over A. tools/netgate reports them; busy-poll's ADR sets the
// threshold.

package server_test

import (
	"bufio"
	stdnet "net"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

const (
	// loadSparse names the load.
	loadSparse = "Sparse"
	// sparseVethConns is each engine's open connections.
	sparseVethConns = 1000
	// sparseVethRequests bounds a round's requests, both engines: at
	// sparseRate a round lasts four seconds, whatever b.N the job asks.
	sparseVethRequests = 4000
	// The veth pair's environment, as L3 reads it
	// (load_l3_linux_internal_test.go): the far address, and the prefix that
	// runs the child on it.
	sparsePeerEnv = "KITSUNIUM_NETGATE_L3_PEER"
	sparseExecEnv = "KITSUNIUM_NETGATE_L3_EXEC"
)

// sparseGroupOption is a Sparse engine's group option: one loop on the best
// backend, as TestLoadSparseChild's, busy-poll opted into for loop-busypoll.
func sparseGroupOption(engine string) server.GroupOption {
	v := corenet.LoopValue{
		Backend: corenet.LoopBackendAuto, Loops: 1, SoftConns: 2 * sparseVethConns,
		Capabilities: []corenet.Capability{corenet.CapabilityURingDeferTaskrun, corenet.CapabilityURingNoSQArray, corenet.CapabilityURingRingFDs},
	}
	if engine == "loop-busypoll" {
		v.Capabilities = append(v.Capabilities, corenet.CapabilityBusyPoll)
	}
	return server.Loop(v)
}

// BenchmarkLoadSparse is Sparse paired: the loop that sleeps against the
// loop that busy-polls (#979), over the veth pair.
func BenchmarkLoadSparse(b *testing.B) {
	b.Run("pair=loop:loop-busypoll", func(b *testing.B) { benchPairedSparse(b, "loop", "loop-busypoll") })
}

// sparseSide is one engine of the pair: its open connections.
type sparseSide struct {
	pool *sparsePool
}

// window sends n requests at sparseRate, each to an idle connection, and
// returns the child's CPU they took and each request's round trip.
func (s *sparseSide) window(b *testing.B, child *loadChild, n int) (uint64, []time.Duration) {
	before := child.sample(b)
	lat := make([]time.Duration, n)
	errc := make(chan error, 1)
	var wg sync.WaitGroup
	start := time.Now()
	//: request k leaves at start + k/sparseRate, whatever the answers do.
	for k := range n {
		if d := time.Until(start.Add(time.Duration(k) * time.Second / sparseRate)); d > 0 {
			time.Sleep(d)
		}
		i, ok := s.pool.take()
		if !ok {
			b.Fatal("every connection is waiting")
		}
		wg.Go(func() {
			t0 := time.Now()
			if err := sparseRoundTrip(s.pool.conns[i], s.pool.readers[i]); err != nil {
				select {
				case errc <- err:
				default:
				}
			}
			lat[k] = time.Since(t0)
			s.pool.put(i)
		})
	}
	wg.Wait()
	after := child.sample(b)
	select {
	case err := <-errc:
		b.Fatal(err)
	default:
	}
	return after[0] - before[0], lat
}

// benchPairedSparse serves engines a and b in one child on the veth pair's
// far end and sends a round's requests in 2×pairedWindows windows, a, b, b,
// a, …: each engine's CPU a request and 99th percentile, and their ratios.
func benchPairedSparse(b *testing.B, engineA, engineB string) {
	peer := os.Getenv(sparsePeerEnv)
	if peer == "" {
		b.Skip("Sparse runs over the veth pair of scripts/ci/net-veth.sh: " + sparsePeerEnv + " is unset")
	}
	child := startLoadChildIn(b, os.Getenv(sparseExecEnv), stdnet.JoinHostPort(peer, "0"), loadSparse, engineA, engineB)
	sides := [2]*sparseSide{{pool: dialSparseN(b, child.addrs[0], sparseVethConns)}, {pool: dialSparseN(b, child.addrs[1], sparseVethConns)}}
	per := max(min(b.N, sparseVethRequests)/(2*pairedWindows), 1)
	b.ResetTimer()
	var cpu [2]uint64
	var lat [2][]time.Duration
	//: A, B, B, A, …: every window of B has one of A beside it.
	for w := range pairedWindows {
		order := [2]int{0, 1}
		if w%2 == 1 {
			order = [2]int{1, 0}
		}
		for _, i := range order {
			c, l := sides[i].window(b, child, per)
			cpu[i] += c
			lat[i] = append(lat[i], l...)
		}
	}
	b.StopTimer()
	n := float64(pairedWindows * per)
	perA, perB := float64(cpu[0])/n, float64(cpu[1])/n
	p99A, p99B := percentile99(lat[0]), percentile99(lat[1])
	b.ReportMetric(perA, "a:cpu-ns/req")
	b.ReportMetric(perB, "b:cpu-ns/req")
	b.ReportMetric(perB/perA, "ratio:cpu-ns/req")
	b.ReportMetric(p99A, "a:p99-ns")
	b.ReportMetric(p99B, "b:p99-ns")
	b.ReportMetric(p99B/p99A, "ratio:p99-ns")
}

// percentile99 is the 99th percentile of d, in nanoseconds: the sample of
// rank ceil(0.99 n), the smallest at or above 99 % of them.
func percentile99(d []time.Duration) float64 {
	s := slices.Clone(d)
	slices.Sort(s)
	return float64(s[(len(s)*99+99)/100-1])
}

// TestLoadSparsePaired_wired serves Sparse's two engines side by side in one
// child on loopback — no NAPI to poll there, the wiring only — and runs a few
// round trips on each, so a harness that no longer serves its load fails the
// suite, not only the bench the veth lane runs.
func TestLoadSparsePaired_wired(t *testing.T) {
	child := startLoadChild(t, loadSparse, "loop", "loop-busypoll")
	before := child.sample(t)
	for _, addr := range child.addrs {
		c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		br := bufio.NewReader(c)
		for range 3 {
			if err := sparseRoundTrip(c, br); err != nil {
				t.Fatalf("%s: %v", addr, err)
			}
		}
		_ = c.Close()
	}
	if after := child.sample(t); after[0] <= before[0] {
		t.Fatalf("CPU %v then %v: want it grown", before, after)
	}
}

// TestPercentile99 pins the rank: of 1 000 samples 1..1000 the 990th, of one
// sample that one.
func TestPercentile99(t *testing.T) {
	d := make([]time.Duration, 1000)
	for i := range d {
		d[len(d)-1-i] = time.Duration(i + 1)
	}
	if got := percentile99(d); got != 990 {
		t.Fatalf("p99 of 1..1000 = %v, want 990", got)
	}
	if got := percentile99([]time.Duration{7}); got != 7 {
		t.Fatalf("p99 of one sample = %v, want 7", got)
	}
}
