package server_test

import (
	"context"
	"io"
	stdnet "net"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// TestConnLimitRefusesBeyondTheCeiling pins that the ceiling actually turns
// connections away rather than queuing them silently. The held handler occupies
// the only slot, so the second connection must be refused and closed while the
// first is still being served.
func TestConnLimitRefusesBeyondTheCeiling(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := server.New()
	srv.Group("capped",
		server.Listen("tcp", "127.0.0.1:0"),
		server.MaxConns(1),
	).HandleFunc(func(_ context.Context, c corenet.Conn) error {
		if _, err := io.WriteString(c, "held\n"); err != nil {
			return err
		}
		<-release
		return nil
	})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	addr := srv.State().Listeners[0].Address

	//: the first connection claims the only slot and holds it.
	first, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial first: %v", err)
	}
	defer closeOrFail(t, first)
	readLine(t, first, "held\n")

	//: the second is accepted by the kernel, then refused by the ceiling.
	second, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial second: %v", err)
	}
	defer closeOrFail(t, second)
	if derr := second.SetReadDeadline(time.Now().Add(3 * time.Second)); derr != nil {
		t.Fatalf("set deadline: %v", derr)
	}
	buf := make([]byte, 8)
	if _, rerr := second.Read(buf); rerr == nil {
		t.Fatal("the second connection was served despite a ceiling of one")
	}
	//: the refusal must be counted, or an operator cannot see saturation.
	waitFor(t, func() bool { return srv.State().RejectedConns >= 1 })
}

// TestConnLimitReleasesItsSlot pins that a served connection gives its slot
// back: a ceiling that leaked slots would wedge the server after MaxConns
// connections, which is far worse than having no ceiling at all.
//
// The ceiling is four rather than one, and that is not padding. A slot is
// released when the handler returns, which happens after the client has already
// seen its echo and closed — so with a ceiling of one, a client reconnecting
// immediately can legitimately meet a server that is still occupied. That
// rejection is correct behaviour, not a leak, and an earlier version of this
// test asserted zero rejections at a ceiling of one and failed roughly one run
// in three. Four slots absorb the release window, so what remains under test is
// the property this test is named for.
func TestConnLimitReleasesItsSlot(t *testing.T) {
	t.Parallel()
	srv := server.New()
	srv.Group("capped",
		server.Listen("tcp", "127.0.0.1:0"),
		server.MaxConns(4),
	).HandleFunc(echoHandler)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	addr := srv.State().Listeners[0].Address

	//: far more connections than slots, so a leak wedges the server well
	//: before the loop ends.
	for i := range 20 {
		if got := roundTrip(t, addr, "ping"); got != "ping\n" {
			t.Fatalf("connection %d got %q, want %q — a slot leaked", i, got, "ping\n")
		}
	}
	if rejected := srv.State().RejectedConns; rejected != 0 {
		t.Fatalf("RejectedConns = %d for sequential traffic under a ceiling of 4, want 0", rejected)
	}
}

// TestNoCeilingByDefault pins that an unset MaxConns means no ceiling, not a
// ceiling of zero — the difference between a working server and a dead one.
func TestNoCeilingByDefault(t *testing.T) {
	t.Parallel()
	_, addr := startEcho(t)
	for range 3 {
		if got := roundTrip(t, addr, "open"); got != "open\n" {
			t.Fatalf("echo = %q", got)
		}
	}
}

// readLine reads one line and asserts it.
func readLine(t *testing.T, c stdnet.Conn, want string) {
	t.Helper()
	if derr := c.SetReadDeadline(time.Now().Add(3 * time.Second)); derr != nil {
		t.Fatalf("set deadline: %v", derr)
	}
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != want {
		t.Fatalf("read %q, want %q", buf, want)
	}
}
