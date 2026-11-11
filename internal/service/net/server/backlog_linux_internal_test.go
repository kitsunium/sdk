//go:build linux

package server

import (
	"net"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// connectsWithin dials addr n times, one after the other, each bounded by
// bound, and returns how many connected. The listener is never accepted from,
// so what connects is what the kernel queued. The dials are sequential on
// purpose: SYNs that arrive together are all answered before the queue shows
// full, and their final ACKs are dropped after the peers believe they are
// connected, so a concurrent count is the race's, not the backlog's.
func connectsWithin(t *testing.T, addr string, n int, bound time.Duration) int {
	t.Helper()
	var (
		connected int
		conns     []net.Conn
	)
	for range n {
		c, err := net.DialTimeout("tcp", addr, bound)
		if err != nil {
			continue
		}
		connected++
		conns = append(conns, c)
	}
	for _, c := range conns {
		_ = c.Close()
	}
	return connected
}

// TestBacklog_queuesAtMostBacklogPlusOneConnects pins what the option does to
// the kernel: a listener nobody accepts from, given Backlog(1), completes at
// most two TCP connects — Linux queues backlog + 1 established connections —
// and drops the SYN of the rest, which the dialers' bound then ends. Without
// the option the host's somaxconn lets all of them in.
func TestBacklog_queuesAtMostBacklogPlusOneConnects(t *testing.T) {
	t.Parallel()
	const dialers = 5
	ln, rep, err := listen(t.Context(), corenet.AddressValue{Network: "tcp", Addr: "127.0.0.1:0"}, nil, socketSpec{attempts: 1, backlog: 1})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { swallowErr(ln.Close()) })
	if rep.backlog != 1 || rep.backlogReason != "" {
		t.Fatalf("the report = %d %q, want 1 and no reason", rep.backlog, rep.backlogReason)
	}
	if got := connectsWithin(t, ln.Addr().String(), dialers, 200*time.Millisecond); got < 1 || got > 2 {
		t.Fatalf("%d of %d connects completed against a backlog of 1, want 1 or 2", got, dialers)
	}
	open, _, err := listen(t.Context(), corenet.AddressValue{Network: "tcp", Addr: "127.0.0.1:0"}, nil, socketSpec{attempts: 1})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { swallowErr(open.Close()) })
	if got := connectsWithin(t, open.Addr().String(), dialers, 200*time.Millisecond); got != dialers {
		t.Fatalf("%d of %d connects completed on a default listener, want all: the host's somaxconn is %d", got, dialers, hostSomaxconn())
	}
}

// TestBacklog_clampIsReportedNotSilent pins that a backlog above somaxconn
// reports the host's value and the reason, never silently.
func TestBacklog_clampIsReportedNotSilent(t *testing.T) {
	t.Parallel()
	host := hostSomaxconn()
	if host <= 0 || host >= 1<<30 {
		t.Skipf("somaxconn is %d: nothing to exceed", host)
	}
	ln, rep, err := listen(t.Context(), corenet.AddressValue{Network: "tcp", Addr: "127.0.0.1:0"}, nil, socketSpec{attempts: 1, backlog: host + 1000})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { swallowErr(ln.Close()) })
	if rep.backlog != host || rep.backlogReason == "" {
		t.Fatalf("the report = %d %q, want somaxconn %d and a reason", rep.backlog, rep.backlogReason, host)
	}
}
