package server_test

import (
	"context"
	stdnet "net"
	"os"
	"os/exec"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// childAddrEnv marks the re-executed half of the adoption test and carries the
// address the inherited socket is bound to.
const childAddrEnv string = "KITSUNIUM_ADOPT_CHILD_ADDR"

// TestAdoptWithoutActivationFails pins the property socket activation exists to
// provide: a service asked to adopt a socket the supervisor never passed must
// FAIL, never quietly bind its own. A silent fallback would lose the socket
// continuity that makes a zero-downtime restart possible, and would lose it
// invisibly — the process would look healthy while dropping every connection
// the outgoing one still held.
func TestAdoptWithoutActivationFails(t *testing.T) {
	//: not parallel — it mutates the process environment.
	t.Setenv("LISTEN_FDS", "")
	srv := server.New()
	srv.Group("api", server.Adopt("api")).HandleFunc(noopHandler)
	err := srv.Start(context.Background())
	if !errs.HasCode(err, corenet.CodeSocketAdoptFailed) {
		t.Fatalf("expected SOCKET_ADOPT_FAILED, got %v", err)
	}
}

// TestAdoptWrongNameFails pins that a name mismatch between the unit file and
// the program is reported rather than bound around.
func TestAdoptWrongNameFails(t *testing.T) {
	//: not parallel — it mutates the process environment.
	t.Setenv("LISTEN_FDS", "1")
	t.Setenv("LISTEN_FDNAMES", "published")
	srv := server.New()
	srv.Group("api", server.Adopt("requested")).HandleFunc(noopHandler)
	err := srv.Start(context.Background())
	if !errs.HasCode(err, corenet.CodeSocketAdoptFailed) {
		t.Fatalf("expected SOCKET_ADOPT_FAILED, got %v", err)
	}
}

// TestGroupWithNeitherAddressNorAdoptFails pins that adoption does not weaken
// the "a group must listen somewhere" check.
func TestGroupWithNeitherAddressNorAdoptFails(t *testing.T) {
	t.Parallel()
	srv := server.New()
	srv.Group("api").HandleFunc(noopHandler)
	if err := srv.Start(context.Background()); !errs.HasCode(err, corenet.CodeInvalidAddress) {
		t.Fatalf("expected INVALID_ADDRESS, got %v", err)
	}
}

// TestAdoptServesTheInheritedSocket is the end-to-end proof, run across a real
// exec because that is the only place adoption exists.
//
// It cannot be staged in-process: sd_listen_fds(3) reads from descriptor 3, and
// a Go test binary already holds that descriptor. exec.Cmd.ExtraFiles places a
// file at exactly fd 3 in the child, which is what a supervisor does, so the
// child wakes up in the state a socket-activated service wakes up in — holding
// a socket it never bound.
func TestAdoptServesTheInheritedSocket(t *testing.T) {
	//: the child half runs the same binary; do not recurse.
	if os.Getenv(childAddrEnv) != "" {
		t.Skip("child half; driven by the parent")
	}
	ln := listenTCP(t)
	file, err := ln.File()
	if err != nil {
		t.Fatalf("listener file: %v", err)
	}
	defer closeOrFail(t, file)
	addr := ln.Addr().String()
	//: close the original so only the inherited descriptor stays live, exactly
	//: as after an exec where the parent's copy is gone.
	closeOrFail(t, ln)

	out, runErr := runAdoptChild(t, file, addr)
	if runErr != nil {
		t.Fatalf("the adopting child failed: %v\n%s", runErr, out)
	}
}

// runAdoptChild re-executes this test binary with the socket at fd 3.
func runAdoptChild(t *testing.T, socket *os.File, addr string) (output []byte, err error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0],
		"-test.run=TestAdoptChildServesInheritedSocket", "-test.v")
	//: ExtraFiles[0] lands on fd 3 — where sd_listen_fds starts.
	cmd.ExtraFiles = []*os.File{socket}
	//: LISTEN_PID is deliberately omitted. sdlisten treats an absent pid as
	//: "addressed to me", which is what its own Prepare emits, and a parent
	//: cannot know the child's pid before starting it anyway.
	cmd.Env = append(os.Environ(),
		"LISTEN_FDS=1",
		"LISTEN_FDNAMES=api",
		childAddrEnv+"="+addr,
	)
	return cmd.CombinedOutput()
}

// TestAdoptChildServesInheritedSocket is the child half: it adopts the socket it
// was handed and proves it serves traffic on it.
func TestAdoptChildServesInheritedSocket(t *testing.T) {
	addr := os.Getenv(childAddrEnv)
	//: without the marker this is the parent pass, where there is no fd 3.
	if addr == "" {
		t.Skip("parent half; driven by TestAdoptServesTheInheritedSocket")
	}
	srv := server.New()
	srv.Group("api", server.Adopt("api")).HandleFunc(echoHandler)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("adopting an inherited socket failed: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })

	state := srv.State()
	if len(state.Listeners) != 1 {
		t.Fatalf("listeners = %d, want 1", len(state.Listeners))
	}
	//: the distinction matters to an operator: an adopted socket survived a
	//: restart, a freshly bound one did not.
	if !state.Listeners[0].Adopted {
		t.Fatal("an inherited socket was not reported as adopted")
	}
	//: a different address would mean the engine bound its own socket instead
	//: of taking over the one it was handed — the silent fallback we forbid.
	if state.Listeners[0].Address != addr {
		t.Fatalf("adopted address = %q, want the inherited %q",
			state.Listeners[0].Address, addr)
	}
	if got := roundTrip(t, addr, "inherited"); got != "inherited\n" {
		t.Fatalf("echo = %q, want %q", got, "inherited\n")
	}
}

// listenTCP binds an ephemeral TCP listener for a test.
func listenTCP(t *testing.T) *stdnet.TCPListener {
	t.Helper()
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tcp, ok := ln.(*stdnet.TCPListener)
	if !ok {
		t.Fatalf("expected a *net.TCPListener, got %T", ln)
	}
	return tcp
}
