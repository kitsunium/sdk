//go:build linux

// Package loop — the steering rows of #857: a loop's thread pinned to a CPU,
// SO_INCOMING_CPU on its shard, TCP_NOTSENT_LOWAT on its listeners.
package loop

import (
	"io"
	stdnet "net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// allowedCPUs is the process's CPU set, in order.
func allowedCPUs(t *testing.T) []int {
	t.Helper()
	var mask [cpuMaskBytes]byte
	_, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_GETAFFINITY, 0, uintptr(cpuMaskBytes), uintptr(unsafe.Pointer(&mask)))
	if errno != 0 {
		t.Fatalf("sched_getaffinity: %v", errno)
	}
	var out []int
	for cpu := range cpuMaskBytes * 8 {
		if mask[cpu/8]&(1<<(cpu%8)) != 0 {
			out = append(out, cpu)
		}
	}
	return out
}

// loopTIDs are a group's loop threads, in loop order.
func loopTIDs(t *testing.T, g *Group) []int {
	t.Helper()
	var out []int
	for _, lp := range g.loops {
		out = append(out, lp.tid)
	}
	return out
}

// cpusAllowed reads Cpus_allowed_list of thread tid.
func cpusAllowed(t *testing.T, tid int) string {
	t.Helper()
	b, err := os.ReadFile("/proc/self/task/" + strconv.Itoa(tid) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(line, "Cpus_allowed_list:"); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Fatalf("no Cpus_allowed_list for %d", tid)
	return ""
}

// TestConformance_affinity pins loop-affinity on every backend: two loops
// pinned to two CPUs of the process's set read them back in
// Cpus_allowed_list, the row on; a CPU no host here has leaves the loop
// unpinned, serving, and the row missing — the fault server reports.
func TestConformance_affinity(t *testing.T) {
	cpus := allowedCPUs(t)
	if len(cpus) < 2 {
		t.Skipf("the process may run on %v only: two CPUs needed", cpus)
	}
	eachBackend(t, func(t *testing.T, b backendCase) {
		want := cpus[len(cpus)-2:]
		g, addr := serve(t, b, &lineH{}, func(c *Config) { c.Value.Loops, c.Value.Affinity = 2, want })
		for i, tid := range loopTIDs(t, g) {
			if got := cpusAllowed(t, tid); got != strconv.Itoa(want[i]) {
				t.Errorf("loop %d (tid %d) Cpus_allowed_list = %q, want %d", i, tid, got, want[i])
			}
		}
		if row := rowOf(g.State(), corenet.CapabilityLoopAffinity); row.Status != statusOn {
			t.Fatalf("loop-affinity = %+v, want on", row)
		}
		g, addr = serve(t, b, &lineH{}, func(c *Config) { c.Value.Affinity = []int{maxAffinityCPU} })
		if row := rowOf(g.State(), corenet.CapabilityLoopAffinity); row.Status != statusMissing || !strings.Contains(row.Reason, "invalid argument") {
			t.Fatalf("loop-affinity = %+v, want missing, EINVAL", row)
		}
		if got := cpusAllowed(t, loopTIDs(t, g)[0]); strings.Contains(got, strconv.Itoa(maxAffinityCPU)) {
			t.Fatalf("Cpus_allowed_list = %q", got)
		}
		c := dial(t, addr)
		echo(t, c, "unpinned\n")
		_ = c.Close()
		g, _ = serve(t, b, &lineH{}, func(c *Config) {
			c.Value.Affinity = want[:1]
			c.Disabled = []corenet.Capability{corenet.CapabilityLoopAffinity}
		})
		if row := rowOf(g.State(), corenet.CapabilityLoopAffinity); row.Status != statusDisabled {
			t.Fatalf("switched off: %+v", row)
		}
		if got := cpusAllowed(t, loopTIDs(t, g)[0]); got == strconv.Itoa(want[0]) && len(cpus) > 1 {
			t.Fatalf("pinned to %s under the switch", got)
		}
	})
	if _, err := Start(Config{Group: "g", Value: corenet.LoopValue{Loops: 2, Affinity: []int{0}}, Handler: &lineH{}, Log: io.Discard}); !errs.HasCode(err, corenet.CodeLoopValueInvalid) {
		t.Fatalf("Affinity of 1 on 2 loops = %v, want LOOP_VALUE_INVALID", err)
	}
}

// TestConformance_incomingCPU pins so-incoming-cpu: each loop's shard reads
// its loop's CPU back; without Affinity the row says so.
func TestConformance_incomingCPU(t *testing.T) {
	cpus := allowedCPUs(t)
	eachBackend(t, func(t *testing.T, b backendCase) {
		fds, addr := reuseportFDs(t, 2)
		aff := []int{cpus[0], cpus[len(cpus)-1]}
		g, err := Start(Config{
			Group: t.Name(), Value: corenet.LoopValue{Backend: b.backend, Loops: 2, Affinity: aff, Capabilities: []corenet.Capability{corenet.CapabilitySOIncomingCPU}},
			Handler: &lineH{}, Listeners: []Listener{{FD: fds[0], Loop: 1}, {FD: fds[1], Loop: 2}}, Log: io.Discard,
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = g.Close() })
		if row := rowOf(g.State(), corenet.CapabilitySOIncomingCPU); row.Status != statusOn {
			t.Fatalf("so-incoming-cpu = %+v, want on", row)
		}
		for i, fd := range fds {
			if v, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, soIncomingCPU); err != nil || v != aff[i] {
				t.Fatalf("listener %d SO_INCOMING_CPU = %d, %v, want %d", i, v, err, aff[i])
			}
		}
		c := dial(t, addr)
		echo(t, c, "steered\n")
		_ = c.Close()
		g2, _ := serve(t, b, &lineH{}, func(c *Config) {
			c.Value.Capabilities = []corenet.Capability{corenet.CapabilitySOIncomingCPU}
		})
		if row := rowOf(g2.State(), corenet.CapabilitySOIncomingCPU); row.Status != statusUnprobed || row.Reason != "no Affinity" {
			t.Fatalf("so-incoming-cpu without Affinity = %+v", row)
		}
	})
}

// lowatH writes 4 MiB at open, 64 KiB at a time until the output refuses,
// and the rest at each OnWritable, where it first reads its socket's bytes
// not yet sent — tcp_info's tcpi_notsent_bytes over sock_diag, SIOCOUTQNSD's
// count, which a direct descriptor answers too: the count TCP_NOTSENT_LOWAT
// bounds, unlike SIOCOUTQ's (/proc/net/tcp's tx_queue), which adds the bytes
// sent and not yet acknowledged.
type lowatH struct {
	lineH
	port     atomic.Int64
	left     int
	writable chan int
}

func (h *lowatH) OnOpen(io corenet.LoopIO, ref corenet.LoopConnRef) {
	h.left = 4 << 20
	h.push(io, ref)
}

// push writes what is left until the output refuses.
func (h *lowatH) push(io corenet.LoopIO, ref corenet.LoopConnRef) {
	chunk := make([]byte, 64<<10)
	for h.left > 0 {
		n := min(h.left, len(chunk))
		if io.Write(ref, chunk[:n]) != nil {
			return
		}
		h.left -= n
	}
}

func (h *lowatH) OnData(corenet.LoopIO, corenet.LoopConnRef, []byte) int { return 0 }

func (h *lowatH) OnWritable(io corenet.LoopIO, ref corenet.LoopConnRef) {
	h.writable <- notsentBytes(int(h.port.Load()))
	h.push(io, ref)
}

const (
	// sockDiagByFamily is SOCK_DIAG_BY_FAMILY, the inet_diag request.
	sockDiagByFamily = 20
	// inetDiagInfo is INET_DIAG_INFO, the attribute carrying tcp_info.
	inetDiagInfo = 2
	// tcpiNotsentOffset is tcpi_notsent_bytes' offset in struct tcp_info
	// (Linux 4.6 and later).
	tcpiNotsentOffset = 144
	// inetDiagMsgLen is struct inet_diag_msg's size, before its attributes.
	inetDiagMsgLen = 72
)

// notsentBytes is tcpi_notsent_bytes of the established TCP socket whose
// local port is port — the server's side —, read over NETLINK_SOCK_DIAG; -1
// when none is found or the kernel's tcp_info is too old to carry it.
func notsentBytes(port int) int {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_INET_DIAG)
	if err != nil {
		return -1
	}
	defer syscall.Close(fd)
	// nlmsghdr, then inet_diag_req_v2: family, protocol, ext, pad, states,
	// and a zero sockid — every established socket, filtered below.
	req := make([]byte, syscall.NLMSG_HDRLEN+56)
	*(*uint32)(unsafe.Pointer(&req[0])) = uint32(len(req))
	*(*uint16)(unsafe.Pointer(&req[4])) = sockDiagByFamily
	*(*uint16)(unsafe.Pointer(&req[6])) = syscall.NLM_F_REQUEST | syscall.NLM_F_DUMP
	body := req[syscall.NLMSG_HDRLEN:]
	body[0], body[1], body[2] = syscall.AF_INET, syscall.IPPROTO_TCP, 1<<(inetDiagInfo-1)
	*(*uint32)(unsafe.Pointer(&body[4])) = 1 << 1 // TCP_ESTABLISHED
	if err := syscall.Sendto(fd, req, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return -1
	}
	buf := make([]byte, 1<<16)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			return -1
		}
		msgs, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return -1
		}
		for _, m := range msgs {
			if m.Header.Type == syscall.NLMSG_DONE || m.Header.Type == syscall.NLMSG_ERROR {
				return -1
			}
			if v, ok := diagNotsent(m.Data, port); ok {
				return v
			}
		}
	}
}

// diagNotsent reads one inet_diag_msg: its tcpi_notsent_bytes when its
// source port is port.
func diagNotsent(d []byte, port int) (int, bool) {
	// inet_diag_msg: family, state, timer, retrans, then the sockid, whose
	// source port comes first, big-endian.
	if len(d) < inetDiagMsgLen || int(d[4])<<8|int(d[5]) != port {
		return 0, false
	}
	for a := d[inetDiagMsgLen:]; len(a) >= 4; {
		l := int(*(*uint16)(unsafe.Pointer(&a[0])))
		if l < 4 || l > len(a) {
			return 0, false
		}
		if *(*uint16)(unsafe.Pointer(&a[2])) == inetDiagInfo {
			info := a[4:l]
			if len(info) < tcpiNotsentOffset+4 {
				return -1, true
			}
			return int(*(*uint32)(unsafe.Pointer(&info[tcpiNotsentOffset]))), true
		}
		a = a[min((l+3)&^3, len(a)):]
	}
	return 0, false
}

// lowatSegment is how far past the lowat a socket's unsent bytes may go:
// sendmsg checks the lowat only as it opens a new segment, so the one it
// fills last — 64 KiB at most, GSO's bound on the loopback — crosses it.
const lowatSegment = 64 << 10

// kernelUnsent is what this kernel leaves unsent on a plain socket, the loop
// nowhere: an accepted stdlib connection given TCP_NOTSENT_LOWAT lowat when
// lowat is positive, written 4 MiB to a peer that reads nothing until the
// writes stop (300 ms), its tcpi_notsent_bytes read as the loop's are. It is
// the kernel's own answer to the option, the bound the loop is held to where
// the kernel overshoots by more than one 64 KiB segment — Fedora's 6.14
// in #1336's CI left 528,256 bytes unsent under a 128 KiB lowat on both
// backends, with the same sendmsg, the same lowat check and the same
// loopback GSO bound as 6.12 (net/ipv4/tcp.c, tcp_ipv4.c, loopback.c read in
// both releases): the overshoot is the kernel's, not the loop's.
func kernelUnsent(t *testing.T, lowat int) int {
	t.Helper()
	ln, err := stdnet.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c := dial(t, ln.Addr().String())
	defer c.Close()
	sc, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	if lowat > 0 {
		raw, err := sc.(*stdnet.TCPConn).SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		var serr error
		if err := raw.Control(func(fd uintptr) { serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpNotsentLowat, lowat) }); err != nil || serr != nil {
			t.Fatalf("TCP_NOTSENT_LOWAT on a plain socket: %v %v", err, serr)
		}
	}
	_ = sc.SetWriteDeadline(time.Now().Add(300 * time.Millisecond))
	_, _ = sc.Write(make([]byte, 4<<20))
	_, ps, _ := stdnet.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(ps)
	return notsentBytes(port)
}

// TestConformance_notsentLowat pins tcp-notsent-lowat on every backend, on
// the count the option bounds — tcpi_notsent_bytes, SIOCOUTQNSD's —: with
// 128 KiB, a 4 MiB Write to a peer that stops reading leaves the socket the
// lowat unsent and at most one segment more — without the option it would
// hold its whole send buffer —, OnWritable does not fire while the peer
// reads nothing, and fires once the peer drained, each time with the socket
// within that bound; a kernel that refuses the option leaves the row
// missing. "Below the lowat" itself cannot be read at OnWritable: the kernel
// reports the socket writable under it, the loop's flush then fills it to
// the next segment past it, and OnWritable follows the flush.
func TestConformance_notsentLowat(t *testing.T) {
	const lowat = 128 << 10
	eachBackend(t, func(t *testing.T, b backendCase) {
		h := &lowatH{writable: make(chan int, 256)}
		g, addr := serve(t, b, h, func(c *Config) { c.Value.NotsentLowat = lowat })
		if row := rowOf(g.State(), corenet.CapabilityTCPNotsentLowat); row.Status != statusOn {
			t.Fatalf("tcp-notsent-lowat = %+v, want on", row)
		}
		_, ps, _ := stdnet.SplitHostPort(addr)
		port, _ := strconv.Atoi(ps)
		h.port.Store(int64(port))
		c := dial(t, addr)
		defer c.Close()
		time.Sleep(300 * time.Millisecond)
		select {
		case q := <-h.writable:
			t.Fatalf("OnWritable while the peer read nothing, %d bytes unsent", q)
		default:
		}
		//: the lowat and one segment more, or what the kernel itself leaves
		//: on a plain socket with the option, and one segment more: never
		//: what it leaves without the option. The kernel's own is measured
		//: once, where the loop's count passes the lowat's segment.
		bound, plain := lowat+lowatSegment, 0
		kernelBound := func(q int) int {
			if plain == 0 {
				var bare int
				plain, bare = kernelUnsent(t, lowat), kernelUnsent(t, 0)
				t.Logf("%s: %d bytes unsent; a plain socket leaves %d with the lowat, %d without", b.name, q, plain, bare)
				if plain >= bare {
					t.Fatalf("a plain socket leaves %d bytes unsent with the lowat and %d without: this kernel does not bound sendmsg by TCP_NOTSENT_LOWAT", plain, bare)
				}
				bound = max(plain, lowat) + lowatSegment
			}
			return bound
		}
		if q := notsentBytes(port); q < lowat || q > bound {
			if q < lowat || q > kernelBound(q) {
				t.Fatalf("%d bytes unsent with a peer that reads nothing, want the lowat %d and at most one segment more than the kernel's own %d (-1: no tcp_info)", q, lowat, plain)
			}
		}
		buf := make([]byte, 64<<10)
		total := 0
		for total < 4<<20 {
			n, err := c.Read(buf)
			if err != nil {
				t.Fatalf("read after %d: %v", total, err)
			}
			total += n
			time.Sleep(100 * time.Microsecond)
		}
		seen, most := 0, 0
		for len(h.writable) > 0 {
			q := <-h.writable
			seen++
			most = max(most, q)
			//: the same bound: Fedora's 6.14 filled the socket past the
			//: lowat's segment at a flush as its own sendmsg does (#857).
			if q < 0 || (q > bound && q > kernelBound(q)) {
				t.Fatalf("OnWritable with %d bytes unsent, want at most %d", q, bound)
			}
		}
		if seen == 0 {
			t.Fatal("no OnWritable once the peer drained")
		}
		t.Logf("%s: %d OnWritable, at most %d bytes unsent (lowat %d)", b.name, seen, most, lowat)
	})
	saved := setsockoptHook
	setsockoptHook = func(fd, level, opt, v int) error {
		if opt == tcpNotsentLowat && level == syscall.IPPROTO_TCP {
			return syscall.ENOPROTOOPT
		}
		return saved(fd, level, opt, v)
	}
	t.Cleanup(func() { setsockoptHook = saved })
	eachBackend(t, func(t *testing.T, b backendCase) {
		g, _ := serve(t, b, &lineH{}, func(c *Config) { c.Value.NotsentLowat = lowat })
		if row := rowOf(g.State(), corenet.CapabilityTCPNotsentLowat); row.Status != statusMissing {
			t.Fatalf("a refusing kernel: %+v, want missing", row)
		}
	})
}
