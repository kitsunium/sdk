//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — the class A-code capabilities' io_uring internals (ADR
// 0189): P3a's primary-only DEFER_TASKRUN and its wake, P3b's layout, P3c's
// unregister before close, and the operator's switch over an opt-in.
package loop

import (
	"bufio"
	"io"
	"slices"
	"strconv"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestCaps_noSQArrayLayout pins P3b: with NO_SQARRAY the kernel reports no
// SQ array (sqOff.array == 0) and every ring of the loop was set up so.
func TestCaps_noSQArrayLayout(t *testing.T) {
	g, _ := codeCase(t, func(cfg *Config) { cfg.Value.Rings = 2 }, corenet.CapabilityURingNoSQArray)
	for i, rr := range uringOf(g).rings {
		if !rr.u.noArray() || rr.u.params.sqOff.array != 0 {
			t.Errorf("ring %d: noArray=%v sqOff.array=%d, want true and 0", i, rr.u.noArray(), rr.u.params.sqOff.array)
		}
	}
}

// secondaryRoundTrips serves every connection on a secondary ring of a
// two-ring loop, with cs opted into and hooks set, and returns 200 echo round
// trips sorted, failing on any that never completes.
func secondaryRoundTrips(t *testing.T, hooks loopHooks, cs ...corenet.Capability) (*Group, []time.Duration) {
	t.Helper()
	hooks.primaryIdle = true
	g, addr := codeCase(t, func(cfg *Config) {
		cfg.Value.Rings = 2
		cfg.hooks = hooks
	}, cs...)
	c := dial(t, addr)
	r := bufio.NewReader(c)
	const rounds = 200
	lat := make([]time.Duration, 0, rounds)
	for i := range rounds {
		start := time.Now()
		_, _ = c.Write([]byte("w" + strconv.Itoa(i) + "\n"))
		if _, err := r.ReadString('\n'); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		lat = append(lat, time.Since(start))
	}
	st := g.State()
	if st.RingStates[0].Accepted != 0 || st.RingStates[1].Accepted == 0 {
		t.Fatalf("accepted primary=%d secondary=%d: the traffic is not on the secondary alone", st.RingStates[0].Accepted, st.RingStates[1].Accepted)
	}
	slices.Sort(lat)
	return g, lat
}

// TestCaps_deferPrimaryOnly pins P3a's shape and its wake: DEFER_TASKRUN on
// the ring the thread waits on alone, and, with every connection on a
// secondary ring, every round trip completes, bounded by the primary's wait
// (one tick), and no slower than the same loop without DEFER_TASKRUN. With
// DEFER_TASKRUN on every ring the second round trip never completes. The
// medians of each shape are logged, the secondaries' wake being the
// kernel's: some kernels run a cooperative secondary's completions only once
// the primary's wait times out.
func TestCaps_deferPrimaryOnly(t *testing.T) {
	g, deferred := secondaryRoundTrips(t, loopHooks{}, corenet.CapabilityURingDeferTaskrun)
	ul := uringOf(g)
	if !ul.rings[0].u.deferred() || ul.rings[1].u.deferred() {
		t.Fatalf("deferred: primary %v, secondary %v; want true, false", ul.rings[0].u.deferred(), ul.rings[1].u.deferred())
	}
	_, coop := secondaryRoundTrips(t, loopHooks{})
	_, plain := secondaryRoundTrips(t, loopHooks{secondaryPlain: true}, corenet.CapabilityURingDeferTaskrun)
	n := len(deferred)
	t.Logf("%s: secondary-only round trip p50/p90/max — defer %v/%v/%v, coop %v/%v/%v, defer with plain secondaries %v/%v/%v",
		kernelRelease(), deferred[n/2], deferred[n*9/10], deferred[n-1], coop[n/2], coop[n*9/10], coop[n-1], plain[n/2], plain[n*9/10], plain[n-1])
	if deferred[n/2] > coop[n/2]+tickEvery/2 {
		t.Fatalf("defer p50 %v past coop p50 %v by more than half a tick", deferred[n/2], coop[n/2])
	}
	if deferred[n*9/10] > 2*tickEvery {
		t.Fatalf("defer p90 %v past two ticks: the wake is not bounded by the primary's wait", deferred[n*9/10])
	}
}

// TestCaps_ringFDsFIN pins P3c: rings registered with their thread are
// unregistered before they close, so closing the group closes every
// connection, which its peer reads as the end; a ring left registered would
// outlive its close and its direct descriptors with it.
func TestCaps_ringFDsFIN(t *testing.T) {
	g, addr := codeCase(t, func(cfg *Config) { cfg.Value.Rings = 2 }, corenet.CapabilityURingRingFDs)
	ul := uringOf(g)
	if ul.registered != 2 {
		t.Fatalf("%d rings registered, want 2", ul.registered)
	}
	c := dial(t, addr)
	_, _ = c.Write([]byte("hi\n"))
	r := bufio.NewReader(c)
	if _, err := r.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := r.ReadByte(); err != io.EOF {
		t.Fatalf("after Close = %v, want EOF", err)
	}
	for i, rr := range ul.rings {
		if rr.u.ringIdx != -1 || rr.u.fd != -1 {
			t.Errorf("ring %d: ringIdx=%d fd=%d after close", i, rr.u.ringIdx, rr.u.fd)
		}
	}
}

// TestCaps_disabledNeverProbed names a class A-code capability both opted
// into and switched off: the operator wins, the row reads disabled, and no
// ring carries it.
func TestCaps_disabledNeverProbed(t *testing.T) {
	b := backendCase{"iouring", corenet.LoopBackendIOURing}
	if err := probeBackend(b.backend); err != nil {
		t.Skipf("io_uring not present: %v", err)
	}
	g, _ := serve(t, b, &lineH{}, func(cfg *Config) {
		cfg.Value.Capabilities = []corenet.Capability{corenet.CapabilityURingDeferTaskrun, corenet.CapabilityURingSQRewind}
		cfg.Disabled = []corenet.Capability{corenet.CapabilityURingDeferTaskrun, corenet.CapabilityURingNoSQArray}
		cfg.DisabledBy = "test"
	})
	st := g.State()
	if row := capRow(t, st, corenet.CapabilityURingDeferTaskrun); row.Status != statusDisabled || row.Reason != "test" {
		t.Fatalf("defer = %+v", row)
	}
	if row := capRow(t, st, corenet.CapabilityURingSQRewind); row.Status != statusMissing {
		t.Fatalf("rewind without no-sqarray = %+v, want missing", row)
	}
	for i, rr := range uringOf(g).rings {
		if rr.u.deferred() || rr.u.rewind() || rr.u.noArray() {
			t.Errorf("ring %d carries a switched-off flag", i)
		}
	}
}
