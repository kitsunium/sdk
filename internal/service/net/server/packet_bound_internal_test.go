// Package server — a bound datagram socket's source, sink and handover.
package server

import (
	"context"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// drainingSource is a source that hands over: its drain ends when told.
type drainingSource struct {
	portableReader
	// drains reports whether handover starts a drain.
	drains bool
	// handed and stopped record the calls.
	handed, stopped bool
}

// handover implements handoverSource.
func (d *drainingSource) handover() bool {
	d.handed = true
	//: as configured.
	return d.drains
}

// stopGRO implements handoverSource.
func (d *drainingSource) stopGRO() { d.stopped = true }

// Test_Server_handoverPackets pins the wait: every socket's drain is started,
// and the wait ends when each read loop has ended, or when the budget does.
func Test_Server_handoverPackets(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// drains is whether the source has a drain to wait for.
		drains bool
		// ends closes done before the budget.
		ends bool
		// budget bounds the wait.
		budget time.Duration
	}
	tests := []tc{
		{name: "nothing to drain", budget: time.Second},
		{name: "a drain that ends", drains: true, ends: true, budget: 5 * time.Second},
		{name: "a drain past Shutdown's budget", drains: true, budget: 50 * time.Millisecond},
		{name: "a drain past the handover's own budget", drains: true, budget: time.Minute},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := New()
		src := &drainingSource{drains: c.drains}
		bound := &boundPacketConn{source: src, done: make(chan struct{})}
		srv.packetConns = []*boundPacketConn{bound}
		if c.ends {
			go func() {
				time.Sleep(20 * time.Millisecond)
				close(bound.done)
			}()
		}
		//: the clock starts before the budget's deadline is set, so the wait
		//: is never measured shorter than the budget it ran out: started
		//: after it, the time between the two readings went missing, and a
		//: runner that let 0.3 ms pass there measured 49.7 ms for a budget of
		//: 50 ms (#768).
		start := time.Now()
		ctx, cancel := context.WithTimeout(t.Context(), c.budget)
		defer cancel()

		srv.handoverPackets(ctx)

		if !src.handed {
			t.Fatal("the socket was not handed over")
		}
		took := time.Since(start)
		//: a drain that never ends is waited for no longer than the shorter of
		//: the two budgets, and has GRO switched off when it is cut.
		if c.drains && !c.ends {
			if limit := min(c.budget, handoverBudget); took < limit || took > limit+time.Second {
				t.Fatalf("returned after %v, want about %v", took, limit)
			}
			if !src.stopped {
				t.Fatal("a cut drain left GRO on")
			}
		}
		srv.stopGROPackets()
		if !src.stopped {
			t.Fatal("stopGROPackets did not reach the source")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_gsoFaults pins the report: one fault a group, however many of
// its sockets the device refused, for as long as the server lives.
func Test_Server_gsoFaults(t *testing.T) {
	t.Parallel()
	srv := New()
	if got := srv.gsoFaults(); len(got) != 0 {
		t.Fatalf("faults before any refusal: %v", got)
	}

	srv.recordGSOOff("a")
	srv.recordGSOOff("a")
	srv.recordGSOOff("b")

	got := srv.gsoFaults()
	if len(got) != 2 || got[0].Group != "a" || got[1].Group != "b" || got[0].Source != faultGSO {
		t.Fatalf("faults %+v, want one gso fault for a, then b", got)
	}
	if st := srv.State(); !st.Degraded() {
		t.Fatal("State is not degraded with GSO off")
	}
}

// Test_Server_newBoundPacket pins what a bound socket is built with: a reply
// sink that writes at once unless the group asked for BatchReplies.
func Test_Server_newBoundPacket(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// batch is the group's BatchReplies.
		batch bool
	}
	tests := []tc{{name: "replies at once"}, {name: "BatchReplies", batch: true}}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := New()
			receiver, _ := udpPair(t)
			bound := srv.newBoundPacket(&PacketGroup{name: "g", batchReplies: c.batch}, corenet.AddressValue{}, receiver)
			_, atOnce := bound.sink.(portableSink)
			if atOnce == c.batch || bound.source == nil || bound.done == nil {
				t.Fatalf("sink %T for BatchReplies=%v", bound.sink, c.batch)
			}
		})
	}
}
