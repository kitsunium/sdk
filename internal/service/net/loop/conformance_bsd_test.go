//go:build darwin || freebsd || openbsd || netbsd || dragonfly

// Package loop — the conformance suite's scaffolding on darwin and the BSDs:
// kqueue is the one backend, required by default wherever the binary runs,
// and an expectation compiled in by -ldflags -X, as Linux's are, replaces
// that default (ADR 0222).
package loop

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// requiredBackends is the comma-separated list of backends this test binary
// must find present. Here it defaults to kqueue, the one backend there is:
// the lanes that run the binary — the darwin runner and the BSD guests, whose
// binaries scripts/ci/test-binaries.sh links with its own -ldflags — require
// it without being told, and a lane that cannot overrides it at link time:
//
//	go test -ldflags=-X=github.com/kitsunium/sdk/internal/service/net/loop.requiredBackends=none
var requiredBackends = "kqueue"

// backendsPresent lists the backends this binary can run.
func backendsPresent() []backendCase {
	return []backendCase{{"kqueue", corenet.LoopBackendKqueue}}
}

// idleClients is the memory test's client role, set by its init where the
// race-off build compiles it; nil otherwise.
var idleClients func(addr string) int

// TestMain runs the memory test's client role when one is marked, and the
// suite otherwise.
func TestMain(m *testing.M) {
	if addr := os.Getenv("KITSUNIUM_LOOP_IDLE_CLIENTS"); addr != "" && idleClients != nil {
		os.Exit(idleClients(addr))
	}
	os.Exit(m.Run())
}

// TestBackendsRequired fails closed when the binary was given no
// expectation, and names every expected backend that is not present.
func TestBackendsRequired(t *testing.T) {
	if requiredBackends == "" {
		t.Fatal("no backend expectation compiled in: build with -ldflags=-X=github.com/kitsunium/sdk/internal/service/net/loop.requiredBackends=<list|none>")
	}
	if requiredBackends == "none" {
		return
	}
	present := map[string]bool{}
	for _, b := range backendsPresent() {
		err := probeBackend(b.backend)
		if err == nil {
			present[b.name] = true
			continue
		}
		t.Logf("net/loop backend %s absent: %v %v", b.name, err, errs.FieldsOf(err))
	}
	for _, want := range strings.Split(requiredBackends, ",") {
		if !present[want] {
			t.Errorf("backend %q is required on this lane and not present", want)
		}
	}
	release, _ := syscall.Sysctl("kern.osrelease")
	t.Logf("net/loop backends: required=%s present=%v kernel=%s", requiredBackends, present, release)
}

// TestKqueue_probeStages fails kqueue's probe at each stage through the test
// hook: an explicit kqueue is refused with LoopBackendUnavailable naming the
// stage, as io_uring's probe is; with no hook it passes every stage and names
// the last.
func TestKqueue_probeStages(t *testing.T) {
	for _, stage := range []string{stageCreate, stageWake, stageSocket} {
		t.Run(stage, func(t *testing.T) {
			fd, _ := listenFD(t)
			_, err := Start(Config{
				Group: "probe", Value: corenet.LoopValue{Backend: corenet.LoopBackendKqueue}, Handler: &lineH{},
				Listeners: []Listener{{FD: fd}}, Log: os.Stderr, hooks: loopHooks{failStage: stage},
			})
			if !errs.HasCode(err, corenet.CodeLoopBackendUnavailable) || !strings.Contains(fmt.Sprint(errs.FieldsOf(err)), stage) {
				t.Fatalf("Start failing %s = %v %v, want LoopBackendUnavailable naming it", stage, err, errs.FieldsOf(err))
			}
		})
	}
	g, _ := serve(t, backendCase{"kqueue", corenet.LoopBackendKqueue}, &lineH{}, nil)
	if st := g.State(); st.Backend != corenet.LoopBackendKqueue || st.ProbeStage != stageSocket || st.Degraded {
		t.Fatalf("State = %v stage %q degraded %v", st.Backend, st.ProbeStage, st.Degraded)
	}
}

// TestKqueue_linuxBackendsRefused asks the leaf for io_uring and epoll here:
// both are refused with UnsupportedPlatform, Linux's mechanics.
func TestKqueue_linuxBackendsRefused(t *testing.T) {
	for _, b := range []corenet.LoopBackend{corenet.LoopBackendIOURing, corenet.LoopBackendEpoll} {
		fd, _ := listenFD(t)
		_, err := Start(Config{Group: "linux", Value: corenet.LoopValue{Backend: b}, Handler: &lineH{}, Listeners: []Listener{{FD: fd}}, Log: os.Stderr})
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Fatalf("Start(%v) = %v, want UnsupportedPlatform", b, err)
		}
	}
}

// listenFD binds a loopback listener and hands back a non-blocking,
// close-on-exec descriptor of it, as server does; darwin has no
// SOCK_NONBLOCK, so the flags are set after the socket is made.
func listenFD(t testing.TB) (int, string) {
	t.Helper()
	syscall.ForkLock.RLock()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Listen(fd, 4096); err != nil {
		t.Fatal(err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	return fd, fmt.Sprintf("127.0.0.1:%d", sa.(*syscall.SockaddrInet4).Port)
}

// countFDs counts the process's open descriptors by asking each number below
// the soft limit's first 65536: FreeBSD lists only three in /dev/fd unless
// fdescfs is mounted, so the directory is not asked.
func countFDs(t *testing.T) int {
	t.Helper()
	limit := min(descriptorLimit(), 1<<16)
	n := 0
	var st syscall.Stat_t
	for fd := range int(limit) {
		if syscall.Fstat(fd, &st) == nil {
			n++
		}
	}
	return n
}

// drainNetworks are the listeners the drain is pinned on here: TCP and Unix.
// Shards(0) binds per-loop SO_REUSEPORT listeners, which darwin and most BSDs
// do not balance; its case stays Linux's.
var drainNetworks = []string{"tcp", "unix"}

// drainQueueCap bounds the queued connections of the drain cases below the
// listen backlog darwin and the BSDs grant by default (somaxconn, 128): past
// it a connection waits in its SYN retries rather than in the queue.
const drainQueueCap int = 96

// networkShards names Shards(0)'s listeners, which drainNetworks leaves out
// here.
const networkShards string = "tcp+shards"

// serveShards is not reached here: drainNetworks names no shards.
func serveShards(t *testing.T, _ backendCase, _ corenet.LoopHandler, _, _ int) (*Group, string) {
	t.Helper()
	t.Fatal("Shards(0) is not pinned on kqueue")
	return nil, ""
}

// streamSocket is a non-blocking, close-on-exec stream socket of family;
// darwin has no SOCK_NONBLOCK, so the flags are set after the socket is made.
func streamSocket(family int) (int, error) {
	syscall.ForkLock.RLock()
	fd, err := syscall.Socket(family, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return -1, err
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return -1, err
	}
	return fd, nil
}

// shortSockPath is a socket path under a directory short enough for sun_path
// (104 bytes on darwin and the BSDs).
func shortSockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "u")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir + "/s"
}
