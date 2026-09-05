//go:build linux

// Package server — adoption of listeners inherited from a supervisor.
package server

import (
	stdnet "net"
	"os"
	"strconv"
	"syscall"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// socketOf reads what a descriptor currently refers to, as /proc/self/fd
// reports it: "socket:[<inode>]", unique to that socket for as long as any
// descriptor holds it open.
//
// It replaces the obvious proof — re-bind the address and see whether it is
// free — which measures the wrong thing. A sibling test binding an ephemeral
// port can be handed the very port the fixture just released, so the re-bind
// fails with EADDRINUSE for a socket that WAS released. The inode cannot be
// confused that way: no other socket in the process can carry it.
func socketOf(t *testing.T, fd uintptr) string {
	t.Helper()
	target, err := os.Readlink("/proc/self/fd/" + strconv.FormatUint(uint64(fd), 10))
	if err != nil {
		t.Fatalf("reading what descriptor %d refers to: %v", fd, err)
	}
	return target
}

// socketOfConn is socketOf for a live listener or datagram socket.
func socketOfConn(t *testing.T, c syscall.Conn) string {
	t.Helper()
	raw, err := c.SyscallConn()
	if err != nil {
		t.Fatalf("reaching the descriptor: %v", err)
	}
	var id string
	if cerr := raw.Control(func(fd uintptr) { id = socketOf(t, fd) }); cerr != nil {
		t.Fatalf("reaching the descriptor: %v", cerr)
	}
	return id
}

// assertReleased pins that NO descriptor in this process still refers to the
// socket.
//
// It is the only assertion that actually measures a leak. Checking that Close
// was called proves the code took a path, not that the kernel got the
// descriptor back — and a descriptor leak is precisely a divergence between
// those two. A dup shares the open file description, so a listener built from
// an adopted descriptor carries the same inode: one look covers both.
func assertReleased(t *testing.T, socket string) {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	//: every descriptor the process still holds, including any dup of ours.
	for _, e := range entries {
		target, rerr := os.Readlink("/proc/self/fd/" + e.Name())
		//: a descriptor closed while the directory was being read.
		if rerr != nil {
			continue
		}
		if target == socket {
			t.Fatalf("descriptor %s still refers to %s — the attempt kept a "+
				"descriptor nothing can reclaim", e.Name(), socket)
		}
	}
}

// adoptableListenerFile returns a descriptor a stream adoption accepts, with the
// socket identity a later look uses to prove it was released.
func adoptableListenerFile(t *testing.T) (file *os.File, socket string) {
	t.Helper()
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() {
		//: File dups the descriptor, so this listener is no longer needed.
		if cerr := ln.Close(); cerr != nil {
			t.Errorf("close listener: %v", cerr)
		}
	}()
	tcp, ok := ln.(*stdnet.TCPListener)
	if !ok {
		t.Fatalf("Listen returned a %T, want *net.TCPListener", ln)
	}
	dup, ferr := tcp.File()
	if ferr != nil {
		t.Fatalf("listener file: %v", ferr)
	}
	return dup, socketOf(t, dup.Fd())
}

// adoptablePacketFile returns a descriptor a datagram adoption accepts, with the
// socket identity a later look uses to prove it was released.
func adoptablePacketFile(t *testing.T) (file *os.File, socket string) {
	t.Helper()
	pc, err := stdnet.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen packet: %v", err)
	}
	defer func() {
		//: File dups the descriptor, so this socket is no longer needed.
		if cerr := pc.Close(); cerr != nil {
			t.Errorf("close socket: %v", cerr)
		}
	}()
	udp, ok := pc.(*stdnet.UDPConn)
	if !ok {
		t.Fatalf("ListenPacket returned a %T, want *net.UDPConn", pc)
	}
	dup, ferr := udp.File()
	if ferr != nil {
		t.Fatalf("socket file: %v", ferr)
	}
	return dup, socketOf(t, dup.Fd())
}

// releaseOnCleanup gives a descriptor back when the test ends.
//
// The file is a PARAMETER rather than a captured loop variable: capturing would
// heap-escape it on every iteration, and the case under test may or may not
// consume the descriptor, so the close is best-effort either way.
func releaseOnCleanup(t *testing.T, file *os.File) {
	t.Helper()
	t.Cleanup(func() {
		//: a descriptor the adoption already consumed is the expected case.
		swallowErr(file.Close())
	})
}

// unadoptableFile returns a descriptor no socket adoption can accept.
func unadoptableFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "not-a-socket")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	return file
}

// withInherited stages a server as if the supervisor had published table.
func withInherited(table map[string][]*os.File, failure error) *Server {
	s := New()
	//: claiming the once here is what keeps the real environment out of the
	//: test: sd_listen_fds reads from fd 3, which a test binary already holds.
	s.inheritOnce.Do(func() {
		s.inherited = table
		s.inheritErr = failure
	})
	return s
}

// Test_Server_adoptedSockets pins that a named socket the supervisor did not
// pass is a FAILURE rather than a fallback to binding.
//
// A service that quietly binds its own port has lost exactly the property
// activation exists to provide: the supervisor's socket survives the exec, so no
// connection is lost and no bind races. A silent fallback loses that invisibly —
// the process looks healthy while dropping every connection the outgoing one
// still held.
func Test_Server_adoptedSockets(t *testing.T) {
	t.Parallel()
	unreadable := errs.Wrap(corenet.SocketAdoptFailed, errs.WrapParams{})

	type tc struct {
		// name describes the case.
		name string
		// published is what the supervisor passed, by name.
		published []string
		// failure stands in for a descriptor table we cannot read at all.
		failure error
		// requested is the name the group asks for.
		requested string
		// wantFiles is how many descriptors the adoption must yield.
		wantFiles int
	}
	tests := []tc{
		{name: "a published socket", published: []string{"api"}, requested: "api", wantFiles: 1},
		{name: "a name the supervisor did not publish", published: []string{"api"}, requested: "admin"},
		{name: "nothing published at all", requested: "api"},
		{name: "no descriptor table to read", failure: unreadable, requested: "api"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		table := make(map[string][]*os.File, len(c.published))
		for _, name := range c.published {
			file, _ := adoptableListenerFile(t)
			releaseOnCleanup(t, file)
			table[name] = []*os.File{file}
		}
		srv := withInherited(table, c.failure)

		files, err := srv.adoptedSockets(c.requested)

		if c.wantFiles == 0 {
			if !errs.HasCode(err, corenet.CodeSocketAdoptFailed) {
				t.Fatalf("adoptedSockets(%q) = %v, want SOCKET_ADOPT_FAILED", c.requested, err)
			}
			//: the refusal names the socket, which is what an operator compares
			//: against the unit file.
			var named bool
			for _, f := range errs.FieldsOf(err) {
				if f.Key() == "socket" {
					named = true
				}
			}
			if !named {
				t.Errorf("the refusal does not name the socket: %v", errs.FieldsOf(err))
			}
			return
		}
		if err != nil {
			t.Fatalf("adoptedSockets(%q) = %v, want nil", c.requested, err)
		}
		if len(files) != c.wantFiles {
			t.Fatalf("adoptedSockets(%q) yielded %d descriptors, want %d",
				c.requested, len(files), c.wantFiles)
		}
		//: claimed exactly once, so two groups naming the same socket surfaces
		//: as a declaration mistake rather than serving one descriptor twice.
		if _, again := srv.adoptedSockets(c.requested); again == nil {
			t.Error("the same socket was adopted twice — two groups would serve one descriptor")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_adoptStream pins the stream half of adoption end to end: a
// published descriptor becomes a live listener, and a missing name fails without
// binding anything.
func Test_Server_adoptStream(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// publish stages a descriptor under the requested name.
		publish bool
		// requested is the name the group asks for.
		requested string
	}
	tests := []tc{
		{name: "a published listener", publish: true, requested: "api"},
		{name: "a name nobody published", requested: "api"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		table := map[string][]*os.File{}
		if c.publish {
			file, _ := adoptableListenerFile(t)
			table[c.requested] = []*os.File{file}
		}
		srv := withInherited(table, nil)

		listeners, err := srv.adoptStream(c.requested)

		if !c.publish {
			if !errs.HasCode(err, corenet.CodeSocketAdoptFailed) {
				t.Fatalf("adoptStream = %v, want SOCKET_ADOPT_FAILED", err)
			}
			//: nothing is handed back, so no accept loop starts on a socket the
			//: engine does not own.
			if listeners != nil {
				t.Error("adoptStream returned listeners beside the error")
			}
			return
		}
		if err != nil {
			t.Fatalf("adoptStream = %v, want nil", err)
		}
		defer closeListeners(listeners)
		if len(listeners) != 1 {
			t.Fatalf("adoptStream yielded %d listeners, want 1", len(listeners))
		}
		//: the adopted socket is live, which is the whole point.
		if listeners[0].Addr() == nil || listeners[0].Addr().String() == "" {
			t.Error("the adopted listener reports no address")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_adoptPacket is the datagram mirror.
//
// sdlisten.Listeners only wraps stream sockets, so the datagram side goes
// through the raw descriptors — which is why adoption lives here rather than
// behind another domain's surface, and why it needs its own coverage rather
// than inheriting the stream half's.
func Test_Server_adoptPacket(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// publish stages a descriptor under the requested name.
		publish bool
		// requested is the name the group asks for.
		requested string
	}
	tests := []tc{
		{name: "a published datagram socket", publish: true, requested: "dns"},
		{name: "a name nobody published", requested: "dns"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		table := map[string][]*os.File{}
		if c.publish {
			file, _ := adoptablePacketFile(t)
			table[c.requested] = []*os.File{file}
		}
		srv := withInherited(table, nil)

		conns, err := srv.adoptPacket(c.requested)

		if !c.publish {
			if !errs.HasCode(err, corenet.CodeSocketAdoptFailed) {
				t.Fatalf("adoptPacket = %v, want SOCKET_ADOPT_FAILED", err)
			}
			if conns != nil {
				t.Error("adoptPacket returned sockets beside the error")
			}
			return
		}
		if err != nil {
			t.Fatalf("adoptPacket = %v, want nil", err)
		}
		defer closePacketConns(conns)
		if len(conns) != 1 {
			t.Fatalf("adoptPacket yielded %d sockets, want 1", len(conns))
		}
		if conns[0].LocalAddr() == nil {
			t.Error("the adopted socket reports no address")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_listenersFromFiles pins that a failed adoption gives EVERY descriptor
// back.
//
// The loop returned on the first descriptor it could not wrap, abandoning the
// listeners already built from the ones before it, the descriptor that just
// failed, and every one after. Nothing else could reclaim them: those listeners
// never reached the Server's registry, so the Close on Start's error path had
// nothing to close, and a supervisor restarting a service into this path leaks a
// descriptor per attempt. Re-binding the address the abandoned listener held is
// what proves it actually went back to the kernel.
func Test_listenersFromFiles(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// shape lists the descriptors in order; true is adoptable.
		shape []bool
	}
	tests := []tc{
		{name: "one good descriptor", shape: []bool{true}},
		{name: "several good descriptors", shape: []bool{true, true, true}},
		{name: "no descriptors at all"},
		//: the bad one is second, so a listener has already been built from the
		//: first by the time the adoption fails.
		{name: "a bad descriptor after a good one", shape: []bool{true, false}},
		{name: "a bad descriptor first", shape: []bool{false, true}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		files := make([]*os.File, 0, len(c.shape))
		sockets := make([]string, 0, len(c.shape))
		good := true
		for _, adoptable := range c.shape {
			if adoptable {
				file, socket := adoptableListenerFile(t)
				files = append(files, file)
				sockets = append(sockets, socket)
				continue
			}
			good = false
			files = append(files, unadoptableFile(t))
		}

		listeners, err := listenersFromFiles("api", files)

		if good {
			if err != nil {
				t.Fatalf("listenersFromFiles = %v, want nil", err)
			}
			adopted := len(listeners)
			if adopted != len(files) {
				t.Fatalf("adopted %d of %d descriptors", adopted, len(files))
			}
			//: an adopted listener is live, which is the point of adopting it.
			for i, ln := range listeners {
				if ln.Addr() == nil || ln.Addr().String() == "" {
					t.Errorf("adopted listener %d reports no address", i)
				}
			}
			closeListeners(listeners)
			return
		}
		//: a regular file is not a listening socket.
		if !errs.HasCode(err, corenet.CodeSocketAdoptFailed) {
			closeListeners(listeners)
			t.Fatalf("listenersFromFiles = %v, want SOCKET_ADOPT_FAILED", err)
		}
		//: every socket the attempt touched must be gone — including the one
		//: whose listener was built before the failure and which nothing else
		//: could ever have reclaimed.
		for _, socket := range sockets {
			assertReleased(t, socket)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_packetConnsFromFiles is the same property on the datagram half, which had
// the byte-identical defect.
func Test_packetConnsFromFiles(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// shape lists the descriptors in order; true is adoptable.
		shape []bool
	}
	tests := []tc{
		{name: "one good descriptor", shape: []bool{true}},
		{name: "several good descriptors", shape: []bool{true, true}},
		{name: "no descriptors at all"},
		{name: "a bad descriptor after a good one", shape: []bool{true, false}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		files := make([]*os.File, 0, len(c.shape))
		sockets := make([]string, 0, len(c.shape))
		good := true
		for _, adoptable := range c.shape {
			if adoptable {
				file, socket := adoptablePacketFile(t)
				files = append(files, file)
				sockets = append(sockets, socket)
				continue
			}
			good = false
			files = append(files, unadoptableFile(t))
		}

		conns, err := packetConnsFromFiles("dns", files)

		if good {
			if err != nil {
				t.Fatalf("packetConnsFromFiles = %v, want nil", err)
			}
			adopted := len(conns)
			if adopted != len(files) {
				t.Fatalf("adopted %d of %d descriptors", adopted, len(files))
			}
			closePacketConns(conns)
			return
		}
		if !errs.HasCode(err, corenet.CodeSocketAdoptFailed) {
			closePacketConns(conns)
			t.Fatalf("packetConnsFromFiles = %v, want SOCKET_ADOPT_FAILED", err)
		}
		//: every socket the attempt touched must be gone.
		for _, socket := range sockets {
			assertReleased(t, socket)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_closeFiles pins that the salvage path releases descriptors the adoption
// never turned into a socket. It is best-effort by design — the adoption error
// is what the caller reports — so the only observable property is that the
// descriptors actually go back.
func Test_closeFiles(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// count is how many descriptors are handed over.
		count int
		// closedAlready closes them first, as a partial adoption may have.
		closedAlready bool
	}
	tests := []tc{
		{name: "no descriptors at all"},
		{name: "one descriptor", count: 1},
		{name: "several descriptors", count: 3},
		//: a descriptor the adoption already released must not panic here.
		{name: "descriptors already closed", count: 2, closedAlready: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		files := make([]*os.File, 0, c.count)
		sockets := make([]string, 0, c.count)
		for range c.count {
			file, socket := adoptableListenerFile(t)
			files = append(files, file)
			sockets = append(sockets, socket)
		}
		if c.closedAlready {
			closeFiles(files)
		}

		closeFiles(files)

		for _, socket := range sockets {
			assertReleased(t, socket)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_closeListeners pins the stream salvage path: listeners built before an
// adoption failed are released, so Start's error path has nothing left to leak.
func Test_closeListeners(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// count is how many listeners were built before the failure.
		count int
	}
	tests := []tc{
		{name: "nothing to release"},
		{name: "one listener", count: 1},
		{name: "several listeners", count: 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		listeners := make([]stdnet.Listener, 0, c.count)
		sockets := make([]string, 0, c.count)
		for range c.count {
			ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			listeners = append(listeners, ln)
			conn, ok := ln.(syscall.Conn)
			if !ok {
				t.Fatalf("Listen returned a %T with no descriptor", ln)
			}
			sockets = append(sockets, socketOfConn(t, conn))
		}

		closeListeners(listeners)

		for _, socket := range sockets {
			assertReleased(t, socket)
		}
		//: releasing twice is what a concurrent Close does, and must not panic.
		closeListeners(listeners)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_closePacketConns is the datagram mirror of the salvage path.
func Test_closePacketConns(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// count is how many sockets were built before the failure.
		count int
	}
	tests := []tc{
		{name: "nothing to release"},
		{name: "one socket", count: 1},
		{name: "several sockets", count: 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		conns := make([]stdnet.PacketConn, 0, c.count)
		sockets := make([]string, 0, c.count)
		for range c.count {
			pc, err := stdnet.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen packet: %v", err)
			}
			conns = append(conns, pc)
			conn, ok := pc.(syscall.Conn)
			if !ok {
				t.Fatalf("ListenPacket returned a %T with no descriptor", pc)
			}
			sockets = append(sockets, socketOfConn(t, conn))
		}

		closePacketConns(conns)

		for _, socket := range sockets {
			assertReleased(t, socket)
		}
		//: releasing twice is what a concurrent Close does, and must not panic.
		closePacketConns(conns)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
