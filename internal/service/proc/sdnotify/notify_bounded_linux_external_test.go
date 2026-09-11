//go:build linux

// Package sdnotify_test — the bounded send, on the one kernel whose behaviour
// the harness needs.
//
// Making a send block requires filling the SUPERVISOR's receive queue, and that
// is Linux's mechanism: the block is charged per sender while the queue belongs
// to the receiver, and a brand new sender parks at the 514th small datagram.
// Elsewhere the state simply does not arise — FreeBSD accepted all 8192 fillers
// ("the receive queue never filled, so the test cannot block a sender") and
// macOS refused the send outright instead of parking it, so the deadline had
// nothing to interrupt and the error carried no os.ErrDeadlineExceeded. Both
// were seen in CI on these tests' first run; a weaker assertion would have kept
// them green while testing nothing.
package sdnotify_test

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsdnotify "github.com/kitsunium/sdk/internal/service/proc/sdnotify"
)

// fillTimeout bounds one filler write. It is short because a filler that has to
// wait has already told us what we needed: the queue is full.
const fillTimeout time.Duration = 100 * time.Millisecond

// fillReceiveQueue leaves path in the state a supervisor that has stopped
// reading produces: a receive queue so full that a BRAND NEW sender blocks on
// its first datagram. That distinction is the whole harness. On Linux the
// blocking is accounted per SENDER while the queue is bounded by the RECEIVER,
// so filling from one connection only blocks that one — measured: a single
// sender stopped at its 13th 8 KiB datagram while a fresh socket still wrote in
// 10 µs. What stops everybody is the receiver's queue LENGTH, reached at 514
// small datagrams on this kernel — and every sdnotify send is a fresh socket
// writing one small datagram, which is exactly the shape that then blocks.
//
// So the fill uses fresh senders too, and stops when one of them blocks.
func fillReceiveQueue(t *testing.T, path string) {
	t.Helper()
	addr := &net.UnixAddr{Name: path, Net: "unixgram"}
	//: a bounded loop: the queue is finite, and a kernel that never refuses one
	//: of these is one this test cannot describe.
	for range 8192 {
		conn, derr := net.DialUnix("unixgram", nil, addr)
		if derr != nil {
			t.Fatalf("dialling %s: %v", path, derr)
		}
		if derr = conn.SetWriteDeadline(time.Now().Add(fillTimeout)); derr != nil {
			t.Fatalf("setting the filler's write deadline: %v", derr)
		}
		_, werr := conn.Write([]byte("FILL=1\n"))
		//: the queued datagrams outlive their sender, so closing costs nothing.
		if cerr := conn.Close(); cerr != nil {
			t.Logf("closing a filler: %v", cerr)
		}
		if werr != nil {
			//: a fresh sender now blocks — the state we came for.
			return
		}
	}
	t.Fatal("the receive queue never filled, so the test cannot block a sender")
}

// TestNotifyContextStopsWaitingWhenTheSupervisorStopsReading is the whole
// reason the context sibling exists. A unixgram write blocks once the
// RECEIVER's buffer is full, and the receiver is the supervisor: a paused,
// stopped or merely slow systemd leaves this process waiting with nothing it
// can do about it. Notify has no deadline and waits forever; NotifyContext
// stops at the caller's.
//
// Seen failing against Notify: the same send never returned, and the test
// binary was killed by its own timeout — "panic: test timed out after 20s".
func TestNotifyContextStopsWaitingWhenTheSupervisorStopsReading(t *testing.T) {
	//: not parallel — it sets $NOTIFY_SOCKET, which is process-wide.
	socket, _ := listenOn(t)
	fillReceiveQueue(t, socket)
	t.Setenv("NOTIFY_SOCKET", socket)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := svcsdnotify.NotifyContext(ctx, map[string]string{"READY": "1"})
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("a send into a full buffer reported success")
	}
	//: the datagram did not go out, so it is the domain's own send failure —
	//: and the cause survives, because a caller distinguishing "too slow" from
	//: "no socket" reads it through errors.Is.
	if !errs.HasCode(err, coreproc.CodeNotifyFailed) {
		t.Errorf("err = %v, want NOTIFY_FAILED", err)
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("err = %v, want it to carry os.ErrDeadlineExceeded", err)
	}
	//: context.DeadlineExceeded is deliberately NOT asserted here. The socket's
	//: deadline and the context's are independent timers that fire at the same
	//: instant, so which one is observed is a race — locally the context won
	//: and on the CI runner the socket did, which is how this assertion was
	//: caught. os.ErrDeadlineExceeded above is the carrier a caller reads for
	//: an expiry, and it is always present; the cancellation case below is the
	//: one where the context's own error is definite.
	//: a generous ceiling: what is under test is that it RETURNS, not its
	//: precision. Without the bound it never does.
	if elapsed > 5*time.Second {
		t.Errorf("the send took %v, want it bounded by the 100ms context", elapsed)
	}
}

// TestNotifyContextIsCancellableWithNoDeadlineAtAll covers the other half of
// the bound: a context with no deadline still reaches a write already parked on
// the supervisor's buffer, because a net.Conn has no other way to be
// interrupted and the deadline set in the past is what unparks it.
// # Goroutine lifetime
//
// One goroutine holds the parked send. It ends when the send returns, which
// the cancel below is what causes; the receive after it is what waits for that
// to have happened, so nothing outlives the test.
func TestNotifyContextIsCancellableWithNoDeadlineAtAll(t *testing.T) {
	//: not parallel — it sets $NOTIFY_SOCKET.
	socket, _ := listenOn(t)
	fillReceiveQueue(t, socket)
	t.Setenv("NOTIFY_SOCKET", socket)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		//: the send parks on the full buffer until the cancel below.
		done <- svcsdnotify.NotifyContext(ctx, map[string]string{"READY": "1"})
	}()
	//: cancel from the outside, which is the supervisor-independent exit.
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled send reported success")
		}
		if !errs.HasCode(err, coreproc.CodeNotifyFailed) {
			t.Errorf("err = %v, want NOTIFY_FAILED", err)
		}
		//: a cancellation reaches the socket as a deadline in the past, so it
		//: is indistinguishable from an expiry unless ctx's own error travels
		//: with it. Seen failing before it did: errors.Is said false.
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want it to carry context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled send never returned")
	}
}
