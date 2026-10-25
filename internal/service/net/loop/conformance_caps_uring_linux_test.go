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

// TestCaps_deferPrimaryOnly pins P3a's shape and its wake: DEFER_TASKRUN on
// the ring the thread waits on alone, and, with every connection on a
// secondary ring, an echo round trip woken by the secondaries' eventfd, not
// by the tick: the median stays under half a tick.
func TestCaps_deferPrimaryOnly(t *testing.T) {
	g, addr := codeCase(t, func(cfg *Config) {
		cfg.Value.Rings = 2
		cfg.hooks = loopHooks{primaryIdle: true}
	}, corenet.CapabilityURingDeferTaskrun)
	ul := uringOf(g)
	if !ul.rings[0].u.deferred() || ul.rings[1].u.deferred() {
		t.Fatalf("deferred: primary %v, secondary %v; want true, false", ul.rings[0].u.deferred(), ul.rings[1].u.deferred())
	}
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
	p50 := lat[rounds/2]
	t.Logf("%s: secondary-only round trip p50=%v p90=%v max=%v", kernelRelease(), p50, lat[rounds*9/10], lat[rounds-1])
	// The median, not a tail: a loaded runner stretches the tail, while a
	// wake that waits for the tick moves the median to half a tick.
	if p50 >= tickEvery/2 {
		t.Fatalf("p50 %v >= half a tick (%v): the secondary's completions wait for the tick", p50, tickEvery/2)
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
