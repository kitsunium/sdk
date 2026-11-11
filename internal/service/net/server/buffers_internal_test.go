//go:build unix

package server

import (
	"context"
	stdnet "net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// sysctlLimit reads a /proc/sys/net/core limit on Linux, or 0 where there is
// none to read.
func sysctlLimit(name string) int {
	raw, err := os.ReadFile("/proc/sys/net/core/" + name)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	return n
}

// wantBuffer is what State must report for a socket asked for ask bytes under
// the limit: on Linux twice the value, clamped first to the sysctl's limit; on
// the others whatever they hold above zero, which the test then only bounds.
func wantBuffer(t *testing.T, got, ask int, limit string) {
	t.Helper()
	if runtime.GOOS == "linux" {
		if lim := sysctlLimit(limit); lim > 0 {
			if want := 2 * min(ask, lim); got != want {
				t.Errorf("read back %d, want %d: twice min(%d, %s %d)", got, want, ask, limit, lim)
			}
			return
		}
	}
	if got <= 0 {
		t.Errorf("read back %d, want what the kernel holds", got)
	}
}

// TestBuffers_stateReadsBackWhatTheKernelHolds pins the report on every kind of
// group: a datagram group's socket, a goroutine stream group's listener and a
// loop group's, asked for 64 KiB and for 1 MiB, past rmem_max and wmem_max on a
// default host. The value read back is the kernel's, doubled and clamped, and a
// clamp is no fault: Degraded stays false.
func TestBuffers_stateReadsBackWhatTheKernelHolds(t *testing.T) {
	t.Parallel()
	//: past both ceilings on any host: the clamp is always exercised on Linux.
	past := max(1<<20, 2*sysctlLimit("rmem_max"), 2*sysctlLimit("wmem_max"))
	for _, ask := range []int{64 << 10, 1 << 20, past} {
		t.Run("datagram/"+strconv.Itoa(ask), func(t *testing.T) {
			t.Parallel()
			srv := New()
			srv.PacketGroup("p", Listen("udp", "127.0.0.1:0"), ReceiveBuffer(ask), SendBuffer(ask)).
				Handle(corenet.PacketHandlerFunc(func(context.Context, corenet.Packet) error { return nil }))
			if err := srv.Start(t.Context()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(func() { swallowErr(srv.Close()) })
			st := srv.State()
			row := st.Listeners[0]
			wantBuffer(t, row.ReceiveBuffer, ask, "rmem_max")
			wantBuffer(t, row.SendBuffer, ask, "wmem_max")
			if st.Degraded() {
				t.Errorf("a clamped buffer degraded the server: %+v", st)
			}
		})
		t.Run("goroutine/"+strconv.Itoa(ask), func(t *testing.T) {
			t.Parallel()
			srv := New()
			srv.Group("g", Listen("tcp", "127.0.0.1:0"), Plaintext(), ReceiveBuffer(ask), SendBuffer(ask)).HandleHTTP(http.NotFoundHandler())
			if err := srv.Start(t.Context()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(func() { swallowErr(srv.Close()) })
			row := srv.State().Listeners[0]
			wantBuffer(t, row.ReceiveBuffer, ask, "rmem_max")
			wantBuffer(t, row.SendBuffer, ask, "wmem_max")
		})
		for _, b := range loopBackends() {
			t.Run("loop-"+b.String()+"/"+strconv.Itoa(ask), func(t *testing.T) {
				t.Parallel()
				srv, _ := startLoopServer(t, b, &laneEchoH{}, ReceiveBuffer(ask), SendBuffer(ask))
				row := srv.State().Listeners[0]
				wantBuffer(t, row.ReceiveBuffer, ask, "rmem_max")
				wantBuffer(t, row.SendBuffer, ask, "wmem_max")
			})
		}
	}
}

// TestBuffers_anAcceptedSocketInheritsTheListeners pins what the loop engines
// rely on, on the goroutine engine where the handler can read its own socket:
// the connection a handler is given holds the SO_SNDBUF the group asked of its
// listener.
func TestBuffers_anAcceptedSocketInheritsTheListeners(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("inheritance of the listener's buffers is Linux's")
	}
	const ask = 128 << 10
	got := make(chan int, 1)
	srv := New()
	srv.Group("g", Listen("tcp", "127.0.0.1:0"), Plaintext(), SendBuffer(ask)).Handle(connFunc(func(_ context.Context, c corenet.Conn) error {
		sc, ok := rawSocket(c).(syscall.Conn)
		if !ok {
			got <- -1
			return nil
		}
		_, send := socketBuffers(sc)
		got <- send
		return nil
	}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	c, err := stdnet.Dial("tcp", srv.State().Listeners[0].Address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { swallowErr(c.Close()) })
	wantBuffer(t, <-got, ask, "wmem_max")
}

// connFunc adapts a function to a ConnHandler.
type connFunc func(context.Context, corenet.Conn) error

// ServeConn implements corenet.ConnHandler.
func (f connFunc) ServeConn(ctx context.Context, c corenet.Conn) error { return f(ctx, c) }

// TestBuffers_negativeIsRefused pins that a size below zero fails Start naming
// the option.
func TestBuffers_negativeIsRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		opt  GroupOption
	}{
		{"ReceiveBuffer", ReceiveBuffer(-1)},
		{"SendBuffer", SendBuffer(-1)},
	} {
		srv := New()
		srv.Group("g", Listen("tcp", "127.0.0.1:0"), Plaintext(), c.opt).HandleHTTP(http.NotFoundHandler())
		err := srv.Start(t.Context())
		swallowErr(srv.Close())
		if !errs.HasCode(err, corenet.CodeOptionValueInvalid) {
			t.Errorf("%s(-1): Start = %v, want OptionValueInvalid", c.name, err)
		}
	}
}
