//go:build linux

// Package health_test — the announcement's bound. Every test here binds a real
// AF_UNIX datagram socket, because what is under test is what happens when the
// supervisor stops reading one, and no double can make a kernel buffer full.
//
// Linux-only, like its sd_notify neighbours, and here for a second reason: the
// state the tests need is produced by a kernel MECHANISM — the receive queue's
// length bound — measured on Linux. Another kernel may fill at another point or
// not at all, and a test that silently stopped reproducing the wedge would
// still pass.
package health_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	svchealth "github.com/kitsunium/sdk/internal/service/health"
)

// fillTimeout bounds one filler write. It is short because a filler that has to
// wait has already told us what we came to know: the queue is full.
const fillTimeout time.Duration = 100 * time.Millisecond

// deafSupervisor binds a notify socket nobody ever reads, fills its receive
// queue, and points $NOTIFY_SOCKET at it. Every send after it blocks.
//
// The queue is filled with FRESH senders because that is how the kernel
// accounts it: the block is charged per sender while the queue belongs to the
// receiver, so filling from one connection blocks only that one — measured, a
// single sender stopped at its 13th 8 KiB datagram while a new socket still
// wrote in 10 µs. What stops everybody is the receiver's queue LENGTH, reached
// at 514 small datagrams on this kernel. Each sd_notify send is a fresh socket
// writing one small datagram, which is precisely the shape that then blocks.
func deafSupervisor(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ktn")
	if err != nil {
		t.Fatalf("creating a short temporary directory: %v", err)
	}
	t.Cleanup(func() {
		if rerr := os.RemoveAll(dir); rerr != nil {
			t.Logf("removing %s: %v", dir, rerr)
		}
	})
	path := filepath.Join(dir, "n.sock")
	addr := &net.UnixAddr{Name: path, Net: "unixgram"}
	listener, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		t.Fatalf("binding %s: %v", path, err)
	}
	t.Cleanup(func() {
		if cerr := listener.Close(); cerr != nil {
			t.Logf("closing the listener: %v", cerr)
		}
	})
	//: a bounded loop: the queue is finite, and a kernel that never refuses one
	//: of these is one this test cannot describe.
	for range 8192 {
		if fillOnce(t, addr) {
			t.Setenv("NOTIFY_SOCKET", path)
			return
		}
	}
	t.Fatal("the receive queue never filled, so the test cannot block a sender")
}

// fillOnce writes one datagram from a brand new sender and reports whether it
// blocked — which is the state deafSupervisor is filling towards.
func fillOnce(t *testing.T, addr *net.UnixAddr) (blocked bool) {
	t.Helper()
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		t.Fatalf("dialling %s: %v", addr.Name, err)
	}
	//: the queued datagrams outlive their sender, so closing costs nothing.
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			t.Logf("closing a filler: %v", cerr)
		}
	}()
	if err = conn.SetWriteDeadline(time.Now().Add(fillTimeout)); err != nil {
		t.Fatalf("setting the filler's write deadline: %v", err)
	}
	_, err = conn.Write([]byte("FILL=1\n"))
	return err != nil
}

// TestADeafSupervisorCannotStopTheProbeFromAnswering is the defect the bound
// exists for, and the reason it is worth serialising the send at all.
//
// The announcement is decided, sent and committed under one lock, so two probes
// cannot leave the supervisor showing the older status. An UNBOUNDED send under
// that lock turns a stuck supervisor into a stuck registry: the first probe
// parks in the write, every later probe parks behind the lock, and the
// readiness the datagram was meant to announce becomes unobservable because
// announcing it hung. A probe endpoint is then the outage it was watching for.
//
// Seen failing with the send unbounded: the first Probe never returned and the
// test binary was killed by its own timeout ("panic: test timed out after 20s",
// the goroutine parked in internal/poll.(*FD).WriteMsg under health.notify).
func TestADeafSupervisorCannotStopTheProbeFromAnswering(t *testing.T) {
	//: not parallel — $NOTIFY_SOCKET is process-wide.
	deafSupervisor(t)
	var notifyErrors atomic.Int64
	registry, _ := newRegistry(t, svchealth.Config{
		Notify:        true,
		OnNotifyError: func(error) { notifyErrors.Add(1) },
	})
	var calls atomic.Int64
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{Name: "db", Check: passing(&calls)})
	//: the probe carries its own deadline, which is what bounds the datagram:
	//: an announcement must not outlive the answer it belongs to. It is a REAL
	//: deadline rather than the manual clock's, because it ends up on a socket
	//: and the kernel enforcing it knows only wall time — the audit's rule is
	//: about WAITING on package time, which nothing here does.
	for poll := range 2 {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		started := time.Now()
		report := registry.Probe(ctx, corehealth.ProbeReadiness)
		elapsed := time.Since(started)
		cancel()
		//: the check itself never failed: the supervisor is deaf, the
		//: dependency is fine, and a probe says what it measured.
		if !report.Status.Serving() {
			t.Errorf("poll %d: status = %v, want serving — the dependency passed", poll, report.Status)
		}
		//: a generous ceiling. What is under test is that it RETURNS; without
		//: the bound the first poll never does.
		if elapsed > 5*time.Second {
			t.Errorf("poll %d took %v, want it bounded by the probe's own deadline", poll, elapsed)
		}
	}
	//: and the failure is reported rather than swallowed: the supervisor did
	//: NOT hear the announcement, which is exactly what the hook is for.
	if got := notifyErrors.Load(); got == 0 {
		t.Error("a datagram that never reached the supervisor was reported as sent")
	}
	if got := calls.Load(); got == 0 {
		t.Error("the readiness check never ran")
	}
}

// TestAProbeWithNoDeadlineStillBoundsItsAnnouncement covers the shape nobody
// configures and everybody has: an HTTP handler's context carries no deadline,
// and neither does context.Background. There is no caller bound to inherit, so
// the registry uses the budget it gives a check — an unreadable notify socket
// and an unresponsive dependency are the same kind of wait, and the registry
// already states how long it tolerates one.
//
// The budget is armed on the INJECTED clock, so this test moves time rather
// than waiting on it, which is the package's own rule (nosleep audit).
func TestAProbeWithNoDeadlineStillBoundsItsAnnouncement(t *testing.T) {
	//: not parallel — $NOTIFY_SOCKET is process-wide.
	deafSupervisor(t)
	var notifyErrors atomic.Int64
	registry, clk := newRegistry(t, svchealth.Config{
		Notify:        true,
		OnNotifyError: func(error) { notifyErrors.Add(1) },
	})
	//: no check is registered ON PURPOSE. A check arms a budget timer of its
	//: own, and this test moves the clock: with two timers armed, the advance
	//: would race the two and could expire the dependency instead of the
	//: datagram. With none, the only timer in flight is the announcement's,
	//: which is what BlockUntil below waits for and what the advance expires.
	done := make(chan corehealth.ReportValue, 1)
	go func() {
		//: no deadline at all — the registry's own budget is the only bound.
		done <- registry.Probe(context.Background(), corehealth.ProbeReadiness)
	}()
	//: BlockUntil proves the announcement's timer EXISTS before the clock is
	//: moved, so the advance can never race ahead of the arming and leave a
	//: send that simply never expires.
	clk.BlockUntil(1)
	clk.Advance(budget * 2)
	//: a plain receive, with no wall-clock rescue: this package waits through
	//: clock.Timed and its own audit says so. An unbounded announcement makes
	//: this line hang until the test binary's timeout, which is the same
	//: verdict with a longer message.
	if report := <-done; !report.Status.Serving() {
		t.Errorf("status = %v, want serving — nothing was measured to be failing", report.Status)
	}
	if got := notifyErrors.Load(); got == 0 {
		t.Error("a datagram that never reached the supervisor was reported as sent")
	}
}
