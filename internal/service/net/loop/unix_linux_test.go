//go:build linux

package loop

import (
	"fmt"
	"io"
	stdnet "net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// unixListenFD binds a non-blocking Unix listener of typ on name and returns
// its descriptor.
func unixListenFD(t *testing.T, typ int, name string) int {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_UNIX, typ|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: name}); err != nil {
		_ = syscall.Close(fd)
		t.Fatalf("bind %q: %v", name, err)
	}
	if err := syscall.Listen(fd, 128); err != nil {
		_ = syscall.Close(fd)
		t.Fatalf("listen: %v", err)
	}
	return fd
}

// shortSockPath is a socket path under a directory short enough for sun_path:
// t.TempDir under the test harness's scratch directory is not.
func shortSockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "u")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

// TestUnix_seqpacketRefused holds #974's first criterion: a group that listens
// on a unixpacket socket is refused at Start with LoopOptionUnsupported naming
// network=unixpacket, on every backend asked for and on auto, and a stream
// socket on the same path family starts.
func TestUnix_seqpacketRefused(t *testing.T) {
	for _, b := range []corenet.LoopBackend{corenet.LoopBackendAuto, corenet.LoopBackendIOURing, corenet.LoopBackendEpoll} {
		t.Run(b.String(), func(t *testing.T) {
			fd := unixListenFD(t, syscall.SOCK_SEQPACKET, shortSockPath(t))
			_, err := Start(Config{Group: "seq", Value: corenet.LoopValue{Backend: b}, Handler: &lineH{}, Listeners: []Listener{{FD: fd}}, Log: io.Discard})
			if !errs.HasCode(err, corenet.CodeLoopOptionUnsupported) {
				t.Fatalf("Start = %v, want LoopOptionUnsupported", err)
			}
			named := false
			for _, f := range errs.FieldsOf(err) {
				named = named || (f.Key() == "network" && f.StringValue() == "unixpacket")
			}
			if !named {
				t.Fatalf("fields = %v, want network=unixpacket", errs.FieldsOf(err))
			}
		})
	}
}

// TestUnix_streamServed holds the other half: a unix stream listener is served
// as before — a path, and a name in the abstract namespace, which
// net.Listen("unix", "@name") binds — and a group whose only listener is Unix
// reads its TCP-only rows unprobed with a reason, not absent, so State is not
// degraded for a family that has no such option.
func TestUnix_streamServed(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		for _, c := range []struct {
			name string
			bind func(*testing.T) (name, dial string)
		}{
			{"path", func(t *testing.T) (string, string) { p := shortSockPath(t); return p, p }},
			{"abstract", func(t *testing.T) (string, string) {
				n := fmt.Sprintf("@kitsunium-loop-%d-%d", os.Getpid(), time.Now().UnixNano())
				return n, n
			}},
		} {
			t.Run(c.name, func(t *testing.T) {
				name, addr := c.bind(t)
				fd := unixListenFD(t, syscall.SOCK_STREAM, name)
				g, err := Start(Config{Group: "unix", Value: corenet.LoopValue{Backend: b.backend}, Handler: &lineH{}, Listeners: []Listener{{FD: fd}}, Log: io.Discard})
				if err != nil {
					t.Fatalf("Start: %v %v", err, errs.FieldsOf(err))
				}
				t.Cleanup(func() { _ = g.Close() })
				conn, err := stdnet.DialTimeout("unix", addr, 3*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				t.Cleanup(func() { _ = conn.Close() })
				echo(t, conn, "hello\n")
				st := g.State()
				if st.Degraded {
					t.Errorf("a unix group is degraded: %q", st.DegradedReason)
				}
				for _, c := range []corenet.Capability{corenet.CapabilityTCPKeepAlive, corenet.CapabilityTCPUserTimeout} {
					if row := capRow(t, st, c); row.Status != statusUnprobed || row.Reason != "no TCP listener" {
						t.Errorf("%s on a unix group = %+v, want unprobed, no TCP listener", c, row)
					}
				}
			})
		}
	})
}

// TestUnix_mixedGroup holds a group with one TCP and one unix listener: the
// TCP-only rows read on, the unix listener serves beside it.
func TestUnix_mixedGroup(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		tfd, taddr := listenFD(t)
		path := shortSockPath(t)
		ufd := unixListenFD(t, syscall.SOCK_STREAM, path)
		g, err := Start(Config{Group: "mixed", Value: corenet.LoopValue{Backend: b.backend}, Handler: &lineH{}, Listeners: []Listener{{FD: tfd}, {FD: ufd}}, Log: io.Discard})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = g.Close() })
		echo(t, dial(t, taddr), "tcp\n")
		conn, err := stdnet.DialTimeout("unix", path, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		t.Cleanup(func() { _ = conn.Close() })
		echo(t, conn, "unix\n")
		if row := capRow(t, g.State(), corenet.CapabilityTCPKeepAlive); row.Status != statusOn {
			t.Errorf("tcp-keepalive on a mixed group = %+v, want on", row)
		}
	})
}

// peerH records, on the loop's thread, what LoopPeer answers for each
// connection at its first bytes, and how many allocations one answer costs.
type peerH struct {
	lineH
	mu2    sync.Mutex
	got    []peerSeen
	allocs float64
}

// peerSeen is one answer.
type peerSeen struct {
	ref corenet.LoopConnRef
	v   corenet.LoopPeerValue
	ok  bool
}

func (h *peerH) OnData(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	p, has := io.(corenet.LoopPeer)
	if !has {
		panic("the loop's LoopIO is no LoopPeer")
	}
	v, ok := p.Peer(ref)
	h.mu2.Lock()
	h.got = append(h.got, peerSeen{ref, v, ok})
	h.mu2.Unlock()
	a := testing.AllocsPerRun(50, func() { _, _ = p.Peer(ref) })
	h.mu2.Lock()
	h.allocs = a
	h.mu2.Unlock()
	return h.lineH.OnData(io, ref, buf)
}

// seen returns the answers recorded so far.
func (h *peerH) seen() []peerSeen {
	h.mu2.Lock()
	defer h.mu2.Unlock()
	return append([]peerSeen(nil), h.got...)
}

// TestUnix_peer holds #974's LoopPeer on the native backends: a unix
// connection answers the connecting process's uid, gid and pid, Verified, with
// no allocation; a TCP connection answers false; a ref the connection left
// behind answers false. On io_uring the Unix listener keeps the group on
// process descriptors, where the socket can be asked, and the row says so.
func TestUnix_peer(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		h := &peerH{}
		tfd, taddr := listenFD(t)
		path := shortSockPath(t)
		ufd := unixListenFD(t, syscall.SOCK_STREAM, path)
		g, err := Start(Config{Group: "peer", Value: corenet.LoopValue{Backend: b.backend, Loops: 1}, Handler: h, Listeners: []Listener{{FD: tfd}, {FD: ufd}}, Log: io.Discard})
		if err != nil {
			t.Fatalf("Start: %v %v", err, errs.FieldsOf(err))
		}
		t.Cleanup(func() { _ = g.Close() })
		st := g.State()
		if b.backend == corenet.LoopBackendIOURing {
			if st.ProbeStage != "self_test+fds" {
				t.Fatalf("probe stage %q, want self_test+fds with a Unix listener", st.ProbeStage)
			}
			if row := capRow(t, st, corenet.CapabilityURingDirect); row.Status != statusUnprobed {
				t.Fatalf("uring-direct = %+v, want unprobed", row)
			}
		}
		uc, err := stdnet.DialTimeout("unix", path, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = uc.SetDeadline(time.Now().Add(10 * time.Second))
		t.Cleanup(func() { _ = uc.Close() })
		echo(t, uc, "unix\n")
		echo(t, dial(t, taddr), "tcp\n")
		got := h.seen()
		if len(got) != 2 {
			t.Fatalf("answers = %+v, want one per connection", got)
		}
		var unixAns, tcpAns peerSeen
		for _, s := range got {
			if s.ok {
				unixAns = s
			} else {
				tcpAns = s
			}
		}
		want := corenet.LoopPeerValue{UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), PID: uint32(os.Getpid()), Verified: true}
		if !unixAns.ok || unixAns.v != want {
			t.Fatalf("unix peer = %+v, %v, want %+v", unixAns.v, unixAns.ok, want)
		}
		if tcpAns.ok || tcpAns.v != (corenet.LoopPeerValue{}) {
			t.Fatalf("tcp peer = %+v, %v, want the zero value and false", tcpAns.v, tcpAns.ok)
		}
		h.mu2.Lock()
		allocs := h.allocs
		h.mu2.Unlock()
		// linux/386 has no getsockopt system call: its answer is the standard
		// library's, one heap structure (peer_cred_linux_386.go).
		if limit := map[bool]float64{true: 1, false: 0}[runtime.GOARCH == "386"]; allocs > limit {
			t.Fatalf("Peer allocates %v a call, want at most %v", allocs, limit)
		}
		// A stale ref: the unix connection closes, then the old ref is asked on
		// the loop's thread.
		_ = uc.Close()
		waitFor(t, "OnClose", func() bool { _, n := h.counts(); return n >= 1 })
		stale := make(chan bool, 1)
		if err := g.loops[0].Post(func(io corenet.LoopIO, _ uintptr) {
			_, ok := io.(corenet.LoopPeer).Peer(unixAns.ref)
			stale <- ok
		}, 0); err != nil {
			t.Fatal(err)
		}
		select {
		case ok := <-stale:
			if ok {
				t.Fatal("a closed connection's ref still names a peer")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the posted check never ran")
		}
	})
}
