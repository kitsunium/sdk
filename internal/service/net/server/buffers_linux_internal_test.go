//go:build linux

package server

import (
	"context"
	"encoding/binary"
	"errors"
	stdnet "net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// The netlink constants sock_diag(7) names and the syscall package does not.
const (
	// sockDiagByFamily is SOCK_DIAG_BY_FAMILY.
	sockDiagByFamily = 20
	// inetDiagSkMeminfo is INET_DIAG_SKMEMINFO, the attribute carrying the
	// socket's memory counters.
	inetDiagSkMeminfo = 7
	// skMeminfoSndbuf is SK_MEMINFO_SNDBUF, the counters' index of sk_sndbuf.
	skMeminfoSndbuf = 3
	// inetDiagMsgBytes is sizeof(struct inet_diag_msg).
	inetDiagMsgBytes = 72
	// tcpEstablishedMask selects TCP_ESTABLISHED in idiag_states.
	tcpEstablishedMask = 1 << 1
)

// diagSndbuf asks the kernel, over NETLINK_INET_DIAG, for the send buffer of
// the established TCP/IPv4 socket on local talking to peer. It is the one way
// to read a socket no descriptor names: io_uring accepts into direct
// descriptors, which /proc/self/fd does not list.
func diagSndbuf(t *testing.T, local, peer int) (int, bool) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_INET_DIAG)
	if err != nil {
		t.Fatalf("netlink socket: %v", err)
	}
	defer func() { _ = syscall.Close(fd) }()
	tv := syscall.NsecToTimeval(int64(5 * time.Second))
	if serr := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); serr != nil {
		t.Fatalf("SO_RCVTIMEO: %v", serr)
	}
	// struct nlmsghdr, then struct inet_diag_req_v2: the family, TCP, the
	// memory attribute, the established state, and an id the dump ignores.
	req := make([]byte, syscall.NLMSG_HDRLEN+56)
	binary.NativeEndian.PutUint32(req[0:], uint32(len(req)))
	binary.NativeEndian.PutUint16(req[4:], sockDiagByFamily)
	binary.NativeEndian.PutUint16(req[6:], syscall.NLM_F_REQUEST|syscall.NLM_F_DUMP)
	req[syscall.NLMSG_HDRLEN] = syscall.AF_INET
	req[syscall.NLMSG_HDRLEN+1] = syscall.IPPROTO_TCP
	req[syscall.NLMSG_HDRLEN+2] = 1 << (inetDiagSkMeminfo - 1)
	binary.NativeEndian.PutUint32(req[syscall.NLMSG_HDRLEN+4:], tcpEstablishedMask)
	if serr := syscall.Sendto(fd, req, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); serr != nil {
		t.Fatalf("netlink send: %v", serr)
	}
	buf := make([]byte, 1<<16)
	for {
		n, _, rerr := syscall.Recvfrom(fd, buf, 0)
		if errors.Is(rerr, syscall.EINTR) {
			continue
		}
		if rerr != nil {
			t.Fatalf("netlink read: %v", rerr)
		}
		msgs, perr := syscall.ParseNetlinkMessage(buf[:n])
		if perr != nil {
			t.Fatalf("netlink parse: %v", perr)
		}
		for _, m := range msgs {
			switch m.Header.Type {
			case syscall.NLMSG_DONE:
				return 0, false
			case syscall.NLMSG_ERROR:
				t.Fatalf("netlink answered an error: %x", m.Data)
			}
			if size, ok := matchDiag(m.Data, local, peer); ok {
				return size, true
			}
		}
	}
}

// matchDiag reads one struct inet_diag_msg: the send buffer of the socket, if
// it is the one on local talking to peer.
func matchDiag(data []byte, local, peer int) (int, bool) {
	if len(data) < inetDiagMsgBytes {
		return 0, false
	}
	// Ports are the only big-endian fields of the message.
	if int(binary.BigEndian.Uint16(data[4:])) != local || int(binary.BigEndian.Uint16(data[6:])) != peer {
		return 0, false
	}
	for attrs := data[inetDiagMsgBytes:]; len(attrs) >= 4; {
		size, kind := int(binary.NativeEndian.Uint16(attrs)), binary.NativeEndian.Uint16(attrs[2:])
		if size < 4 || size > len(attrs) {
			return 0, false
		}
		if kind == inetDiagSkMeminfo && size >= 4+(skMeminfoSndbuf+1)*4 {
			return int(binary.NativeEndian.Uint32(attrs[4+skMeminfoSndbuf*4:])), true
		}
		attrs = attrs[min((size+3)&^3, len(attrs)):]
	}
	return 0, false
}

// TestBuffers_aLoopGroupsAcceptedSocketInheritsTheListeners pins the audit's
// case on the loop engines, which give a handler no socket to read: the socket
// a loop accepted holds the SO_SNDBUF the group asked of its listener, read
// back from the kernel by its ports.
func TestBuffers_aLoopGroupsAcceptedSocketInheritsTheListeners(t *testing.T) {
	t.Parallel()
	const ask = 1 << 20
	for _, b := range []corenet.LoopBackend{corenet.LoopBackendAuto, corenet.LoopBackendEpoll} {
		t.Run(b.String(), func(t *testing.T) {
			t.Parallel()
			srv, addr := startLoopServer(t, b, &laneEchoH{}, SendBuffer(ask))
			c := loopDial(t, addr)
			// The echo proves the loop accepted the socket and served it.
			msg := []byte("ping\n")
			if _, err := c.Write(msg); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Read(make([]byte, len(msg))); err != nil {
				t.Fatal(err)
			}
			t.Logf("backend %v", srv.State().Loops[0].Backend)
			local := c.RemoteAddr().(*stdnet.TCPAddr).Port
			peer := c.LocalAddr().(*stdnet.TCPAddr).Port
			got, ok := diagSndbuf(t, local, peer)
			if !ok {
				t.Fatalf("the kernel lists no socket on %d talking to %d", local, peer)
			}
			wantBuffer(t, got, ask, "wmem_max")
		})
	}
}

// TestBuffers_anAdoptedSocketTakesTheGroupsSizes pins the group's sizes on a
// socket no bind set them on: a supervisor's, on the goroutine engine, the
// datagram engine and a loop group, reported as they stand.
func TestBuffers_anAdoptedSocketTakesTheGroupsSizes(t *testing.T) {
	t.Parallel()
	const ask = 64 << 10
	cases := []struct {
		name  string
		file  func(*testing.T) *os.File
		start func(*Server)
	}{
		{"stream", listenerFile, func(s *Server) {
			s.Group("g", Adopt("sock"), Plaintext(), ReceiveBuffer(ask), SendBuffer(ask)).HandleHTTP(http.NotFoundHandler())
		}},
		{"loop", listenerFile, func(s *Server) {
			s.Group("g", Adopt("sock"), Loop(corenet.LoopValue{Backend: corenet.LoopBackendEpoll}), ReceiveBuffer(ask), SendBuffer(ask)).HandleLoop(&laneEchoH{})
		}},
		{"datagram", packetFile, func(s *Server) {
			s.PacketGroup("g", Adopt("sock"), ReceiveBuffer(ask), SendBuffer(ask)).
				Handle(corenet.PacketHandlerFunc(func(_ context.Context, _ corenet.Packet) error { return nil }))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := withInherited(map[string][]*os.File{"sock": {c.file(t)}}, nil)
			c.start(srv)
			if err := srv.Start(t.Context()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(func() { swallowErr(srv.Close()) })
			row := srv.State().Listeners[0]
			if !row.Adopted {
				t.Fatalf("row not adopted: %+v", row)
			}
			wantBuffer(t, row.ReceiveBuffer, ask, "rmem_max")
			wantBuffer(t, row.SendBuffer, ask, "wmem_max")
		})
	}
}

// TestBufferRefusal_enobufsIsNoFault pins the BSD and darwin answer to a size
// past their ceiling, which only those platforms give: it fails no bind.
func TestBufferRefusal_enobufsIsNoFault(t *testing.T) {
	t.Parallel()
	if err := bufferRefusal(syscall.ENOBUFS); err != nil {
		t.Errorf("ENOBUFS = %v, want no fault", err)
	}
	if err := bufferRefusal(syscall.EINVAL); !errors.Is(err, syscall.EINVAL) {
		t.Errorf("EINVAL = %v, want it kept", err)
	}
	if err := bufferRefusal(nil); err != nil {
		t.Errorf("nil = %v", err)
	}
}
