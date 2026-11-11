package server

import (
	stdnet "net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// unixNoDelayReason is what the tcp-nodelay row says on a group with no TCP
// listener (#974).
const unixNoDelayReason = "unix socket"

// TestLoopGroup_refusesUnixpacket holds #974: a loop group that declares a
// unixpacket address is refused at Start on every backend, naming the network,
// before anything is bound. Every backend reads a connection as a byte stream,
// so a record past the buffer would reach OnData cut.
func TestLoopGroup_refusesUnixpacket(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(socketDir(t), "seq.sock")
			srv := New()
			t.Cleanup(func() { swallowErr(srv.Close()) })
			srv.Group("loop", Listen("unixpacket", path), Loop(corenet.LoopValue{Backend: b})).HandleLoop(&loopLineH{})
			err := srv.Start(t.Context())
			if !errs.HasCode(err, corenet.CodeLoopOptionUnsupported) {
				t.Fatalf("Start = %v, want LoopOptionUnsupported", err)
			}
			if !slices.Contains(errs.FieldsOf(err), errs.String("network", "unixpacket")) {
				t.Fatalf("fields = %v, want network=unixpacket", errs.FieldsOf(err))
			}
			if _, serr := os.Stat(path); !os.IsNotExist(serr) {
				t.Fatalf("the refused address was bound: stat = %v", serr)
			}
		})
	}
}

// TestLoopRefusal_adoptedUnixpacket holds the half a declared address cannot
// show: an inherited unixpacket socket is known only once adopted, and the
// portable backend has no leaf to refuse it.
func TestLoopRefusal_adoptedUnixpacket(t *testing.T) {
	t.Parallel()
	if platformLacks("unixpacket") {
		t.Skipf("%s has no unixpacket socket", runtime.GOOS)
	}
	path := filepath.Join(socketDir(t), "seq.sock")
	seq, err := stdnet.Listen("unixpacket", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { swallowErr(seq.Close()) })
	if rerr := refuseUnixpacketListeners("loop", []stdnet.Listener{seq}); !errs.HasCode(rerr, corenet.CodeLoopOptionUnsupported) {
		t.Fatalf("refusal = %v, want LoopOptionUnsupported", rerr)
	}
	stream, err := stdnet.Listen("unix", filepath.Join(socketDir(t), "stream.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { swallowErr(stream.Close()) })
	if rerr := refuseUnixpacketListeners("loop", []stdnet.Listener{stream}); rerr != nil {
		t.Fatalf("a unix stream is refused: %v", rerr)
	}
}

// startLoopOn starts one loop group on b over opts and returns its State.
func startLoopOn(t *testing.T, b corenet.LoopBackend, opts ...GroupOption) corenet.StateValue {
	t.Helper()
	srv := New()
	t.Cleanup(func() { swallowErr(srv.Close()) })
	opts = append(opts, Loop(corenet.LoopValue{Backend: b}))
	srv.Group("loop", opts...).HandleLoop(&loopLineH{})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v %v", err, errs.FieldsOf(err))
	}
	return srv.State()
}

// TestCapabilities_unixGroupHasNoNoDelay holds #974: a group with no TCP
// listener reports tcp-nodelay unprobed, by name, and no degradation; the row
// was reported absent, a false fault, because the family has no such option.
func TestCapabilities_unixGroupHasNoNoDelay(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			t.Parallel()
			st := startLoopOn(t, b, Listen("unix", filepath.Join(socketDir(t), "only.sock")))
			if row := capRowOf(t, st.Loops[0], corenet.CapabilityTCPNoDelay); row.Status != statusUnprobed || row.Reason != unixNoDelayReason {
				t.Fatalf("tcp-nodelay = %+v, want unprobed, %q", row, unixNoDelayReason)
			}
			if f := capabilityFaults(st); len(f) != 0 {
				t.Fatalf("a unix group has a capability fault: %+v", f)
			}
			// Auto may fall back where io_uring is absent, which is its own fault.
			if b != corenet.LoopBackendAuto && st.Degraded() {
				t.Fatalf("a unix group is degraded: %+v", st.Faults)
			}
		})
	}
}

// TestCapabilities_mixedGroupKeepsNoDelay holds the other half: one TCP
// listener beside a Unix one is enough for the row to read on.
func TestCapabilities_mixedGroupKeepsNoDelay(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			t.Parallel()
			st := startLoopOn(t, b, Listen("tcp", "127.0.0.1:0"), Listen("unix", filepath.Join(socketDir(t), "mixed.sock")))
			if row := capRowOf(t, st.Loops[0], corenet.CapabilityTCPNoDelay); row.Status != statusOn {
				t.Fatalf("tcp-nodelay = %+v, want on", row)
			}
		})
	}
}

// peerAnswer is what LoopPeer said in one OnOpen.
type peerAnswer struct {
	found   bool
	value   corenet.LoopPeerValue
	ok      bool
	staleOK bool
}

// peerH answers each connection's OnOpen with what LoopPeer says of it, and of
// the same slot under another generation.
type peerH struct {
	loopLineH
	got chan peerAnswer
}

func (h *peerH) OnOpen(io corenet.LoopIO, ref corenet.LoopConnRef) {
	lp, found := io.(corenet.LoopPeer)
	if !found {
		h.got <- peerAnswer{}
		return
	}
	v, ok := lp.Peer(ref)
	stale := ref
	stale.Gen++
	_, staleOK := lp.Peer(stale)
	h.got <- peerAnswer{found: true, value: v, ok: ok, staleOK: staleOK}
}

// peerOfDial starts a portable loop group on network and address, dials it and
// returns what LoopPeer said when the connection opened.
func peerOfDial(t *testing.T, network, address string) peerAnswer {
	t.Helper()
	h := &peerH{got: make(chan peerAnswer, 1)}
	srv := New()
	t.Cleanup(func() { swallowErr(srv.Close()) })
	srv.Group("loop", Listen(network, address), Loop(corenet.LoopValue{Backend: corenet.LoopBackendPortable})).HandleLoop(h)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v %v", err, errs.FieldsOf(err))
	}
	c, err := stdnet.DialTimeout(network, srv.State().Listeners[0].Address, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { swallowErr(c.Close()) })
	select {
	case a := <-h.got:
		return a
	case <-time.After(10 * time.Second):
		t.Fatal("the connection never opened")
		return peerAnswer{}
	}
}

// TestPortableLoop_peerUnix holds #974 on the portable backend: LoopPeer names
// a Unix connection's peer as the kernel recorded it, where the backend can
// ask the kernel, and answers false elsewhere and for a stale reference.
func TestPortableLoop_peerUnix(t *testing.T) {
	t.Parallel()
	a := peerOfDial(t, "unix", filepath.Join(socketDir(t), "peer.sock"))
	if !a.found {
		t.Fatal("the portable backend's LoopIO is not a LoopPeer")
	}
	if a.staleOK {
		t.Fatal("a stale reference has a peer")
	}
	if runtime.GOOS != "linux" {
		if a.ok || a.value.Verified {
			t.Fatalf("Peer on %s = %+v, %v: nothing here can verify a peer", runtime.GOOS, a.value, a.ok)
		}
		return
	}
	want := corenet.LoopPeerValue{UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), PID: uint32(os.Getpid()), Verified: true}
	if !a.ok || a.value != want {
		t.Fatalf("Peer = %+v, %v, want %+v", a.value, a.ok, want)
	}
}

// TestPortableLoop_peerTCP holds that a TCP connection has no peer to name.
func TestPortableLoop_peerTCP(t *testing.T) {
	t.Parallel()
	a := peerOfDial(t, "tcp", "127.0.0.1:0")
	if !a.found {
		t.Fatal("the portable backend's LoopIO is not a LoopPeer")
	}
	if a.ok || a.value.Verified {
		t.Fatalf("Peer on TCP = %+v, %v, want false", a.value, a.ok)
	}
}
