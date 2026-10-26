//go:build linux

// L3, the datagram load of the network plan's merge gate (plan P8,
// tools/netgate): UDP datagrams of 1200 bytes from a GSO sender of 54
// segments, 4 flows at a fixed rate, over a veth pair whose far end sits in a
// network namespace (scripts/ci/net-veth.sh). The receiver is a child — this
// binary re-run on TestLoadL3Child, inside the namespace — reading through
// the package's own datagramSource, and the metrics are ITS CPU per datagram
// and the datagrams each receive returned. Engine A is today's recvmmsg
// reader; engine B, GRO and sendmmsg in datagramSource, lands with P4a, and
// until then the P4a gate is not required.
//
// The measure is paired, as L1's and L2's (load_gate_linux_test.go): one
// receiver reads both engines' sockets, each through its own source, and a
// round sends to them in windows A, B, B, A, … at the one fixed rate. It
// reports each engine's metrics and their ratios, B over A — one paired
// sample a round, whose interval tools/netgate judges.

package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	stdnet "net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The load, as the plan fixes it.
const (
	// l3DatagramSize is each datagram's payload.
	l3DatagramSize = 1200
	// l3Segments is how many datagrams one GSO send carries: 64800 bytes,
	// under the 64 KiB a UDP send may hold.
	l3Segments = 54
	// l3Flows is the number of sending sockets, each its own 4-tuple.
	l3Flows = 4
	// l3Rate is the fixed rate, in datagrams a second over all flows.
	l3Rate = 200_000
	// l3Slots is the receiver's batch.
	l3Slots = 64
	// l3ReadBuffer is the receive buffer the receiver asks for, so a burst of
	// every flow's segments is queued rather than dropped; the kernel caps it
	// at net.core.rmem_max, and the bench reports what was lost.
	l3ReadBuffer = 8 << 20
)

// l3Windows is how many windows of each engine one paired round sends, as
// L1's and L2's pairedWindows: A, B, then B, A, repeated.
const l3Windows = 8

// udpSegment is UDP_SEGMENT, the socket option that turns a send into GSO
// segments of its value (include/uapi/linux/udp.h; Linux 4.18). The standard
// library's syscall package does not define it.
const udpSegment = 103

// The environment of an L3 run: the receiver's address in the namespace, the
// sender's on the near end of the pair, and the command prefix that runs the
// child in the namespace ("sudo ip netns exec netgate-l3"). Without the
// first, the bench is skipped: L3 is measured over the veth pair or not at all.
const (
	l3PeerEnv  = "KITSUNIUM_NETGATE_L3_PEER"
	l3LocalEnv = "KITSUNIUM_NETGATE_L3_LOCAL"
	l3ExecEnv  = "KITSUNIUM_NETGATE_L3_EXEC"
	l3BindEnv  = "KITSUNIUM_NETGATE_L3_BIND"
	l3ChildEnv = "KITSUNIUM_NETGATE_CHILD"
)

// TestLoadL3Child is the receiving half of BenchmarkLoadL3, and runs only in
// the child it starts: it binds, prints its address, reads through
// datagramSource counting datagrams and receives, and answers each line on
// stdin with its CPU, datagrams and receives, until stdin closes.
func TestLoadL3Child(t *testing.T) {
	engines, ok := strings.CutPrefix(os.Getenv(l3ChildEnv), "L3:")
	if !ok {
		t.Skip("the receiving half of BenchmarkLoadL3: run by it, in a child")
	}
	var readers []*l3Reader
	line := "addr"
	for _, engine := range strings.Split(engines, ",") {
		r, err := listenL3(engine, os.Getenv(l3BindEnv))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.pc.Close() }()
		readers = append(readers, r)
		line += " " + r.pc.LocalAddr().String()
	}
	fmt.Println(line)
	l3Answer(os.Stdin, os.Stdout, func() []uint64 {
		var vs []uint64
		for _, r := range readers {
			vs = append(vs, r.datagrams.Load(), r.receives.Load())
		}
		return vs
	})
}

// l3Sources builds each L3 engine's reader over its socket, by label: the one
// table an engine B is added to.
var l3Sources = map[string]func(stdnet.PacketConn) datagramSource{
	"recvmmsg": newDatagramSource,
}

// l3Reader is one engine's socket in the receiver, and what it counted.
type l3Reader struct {
	pc                  stdnet.PacketConn
	datagrams, receives atomic.Uint64
}

// listenL3 binds a socket for engine and reads it through the engine's
// source, counting datagrams and receives, until the socket closes.
//
// Goroutine lifecycle: it launches the reader, which returns once the socket
// is closed.
func listenL3(engine, bind string) (*l3Reader, error) {
	build, ok := l3Sources[engine]
	if !ok {
		return nil, fmt.Errorf("no L3 engine %q", engine)
	}
	pc, err := stdnet.ListenPacket("udp", bind)
	if err != nil {
		return nil, err
	}
	if udp, ok := pc.(*stdnet.UDPConn); ok {
		_ = udp.SetReadBuffer(l3ReadBuffer)
	}
	r := &l3Reader{pc: pc}
	src := build(pc)
	go func() {
		slots := newSlots(l3Slots, l3DatagramSize)
		for {
			n, rerr := src.readBatch(slots)
			if rerr != nil {
				return
			}
			r.receives.Add(1)
			r.datagrams.Add(uint64(n))
		}
	}()
	return r, nil
}

// l3Answer answers each line read from in with the process's CPU (user+system,
// all threads, ns), then extra's fields; it returns when in closes.
func l3Answer(in io.Reader, out io.Writer, extra func() []uint64) {
	r := bufio.NewReader(in)
	line := make([]byte, 0, 128)
	for {
		if _, err := r.ReadSlice('\n'); err != nil {
			return
		}
		var ru syscall.Rusage
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
		line = strconv.AppendInt(line[:0], ru.Utime.Nano()+ru.Stime.Nano(), 10)
		for _, v := range extra() {
			line = append(line, ' ')
			line = strconv.AppendUint(line, v, 10)
		}
		_, _ = out.Write(append(line, '\n'))
	}
}

// l3Child is a running receiver.
type l3Child struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	out   *bufio.Reader
	addrs []string
}

// startL3Child runs the receiver bound to bind, one socket for each of
// engines, under prefix when given.
func startL3Child(tb testing.TB, prefix, bind string, engines ...string) *l3Child {
	tb.Helper()
	env := []string{l3ChildEnv + "=L3:" + strings.Join(engines, ","), l3BindEnv + "=" + bind}
	var cmd *exec.Cmd
	if p := strings.Fields(prefix); len(p) > 0 {
		// sudo and ip netns exec reset the environment: it goes through env.
		args := append(append(p[1:], "env"), env...)
		args = append(args, os.Args[0], "-test.run=^TestLoadL3Child$", "-test.count=1")
		cmd = exec.Command(p[0], args...)
	} else {
		cmd = exec.Command(os.Args[0], "-test.run=^TestLoadL3Child$", "-test.count=1")
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		tb.Fatal(err)
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		tb.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		tb.Fatal(err)
	}
	c := &l3Child{cmd: cmd, in: in, out: bufio.NewReader(outPipe)}
	tb.Cleanup(func() {
		_ = c.in.Close()
		done := make(chan struct{})
		go func() { _ = c.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = c.cmd.Process.Kill()
			<-done
		}
	})
	line, err := c.out.ReadString('\n')
	addrs, ok := strings.CutPrefix(strings.TrimSpace(line), "addr ")
	c.addrs = strings.Fields(addrs)
	if err != nil || !ok || len(c.addrs) != len(engines) {
		tb.Fatalf("L3 receiver printed %q, %v, want one address an engine", line, err)
	}
	return c
}

// l3Sample is the receiver's CPU, then each socket's datagrams and receives,
// in the order of the engines it was started with.
type l3Sample struct {
	cpu                 uint64
	datagrams, receives []uint64
}

// sample reads the receiver's CPU and each socket's datagrams and receives.
func (c *l3Child) sample(tb testing.TB) l3Sample {
	tb.Helper()
	if _, err := c.in.Write([]byte("s\n")); err != nil {
		tb.Fatal(err)
	}
	line, err := c.out.ReadString('\n')
	f := strings.Fields(line)
	if err != nil || len(f) != 1+2*len(c.addrs) {
		tb.Fatalf("L3 receiver sample %q, %v", line, err)
	}
	vs := make([]uint64, len(f))
	for i := range vs {
		v, perr := strconv.ParseUint(f[i], 10, 64)
		if perr != nil {
			tb.Fatalf("L3 receiver sample %q: %v", line, perr)
		}
		vs[i] = v
	}
	s := l3Sample{cpu: vs[0]}
	for i := range c.addrs {
		s.datagrams = append(s.datagrams, vs[1+2*i])
		s.receives = append(s.receives, vs[2+2*i])
	}
	return s
}

// errNoGSO reports a kernel that refused UDP_SEGMENT.
var errNoGSO = errors.New("UDP_SEGMENT refused: this kernel has no UDP GSO")

// dialGSO opens one flow: a UDP socket from local to peer whose every send of
// l3Segments*l3DatagramSize bytes leaves as l3Segments datagrams.
func dialGSO(local, peer string) (*stdnet.UDPConn, error) {
	raddr, err := stdnet.ResolveUDPAddr("udp", peer)
	if err != nil {
		return nil, err
	}
	var laddr *stdnet.UDPAddr
	if local != "" {
		laddr = &stdnet.UDPAddr{IP: stdnet.ParseIP(local)}
	}
	c, err := stdnet.DialUDP("udp", laddr, raddr)
	if err != nil {
		return nil, err
	}
	raw, err := c.SyscallConn()
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	var serr error
	if cerr := raw.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_UDP, udpSegment, l3DatagramSize)
	}); cerr != nil || serr != nil {
		_ = c.Close()
		return nil, fmt.Errorf("%w: %v %v", errNoGSO, cerr, serr)
	}
	return c, nil
}

// sendL3 sends datagrams datagrams over l3Flows GSO flows at l3Rate, each
// flow pacing its sends on an absolute schedule so a late send does not slow
// the rate after it, the flows offset from one another so their bursts
// interleave. It returns how many datagrams it sent: datagrams rounded up to
// whole sends of every flow.
func sendL3(flows []*stdnet.UDPConn, datagrams int) (int, error) {
	payload := make([]byte, l3Segments*l3DatagramSize)
	perFlow := (datagrams + len(flows)*l3Segments - 1) / (len(flows) * l3Segments)
	every := time.Duration(float64(time.Second) * l3Segments * float64(len(flows)) / l3Rate)
	var wg sync.WaitGroup
	errc := make(chan error, len(flows))
	start := time.Now()
	for f, c := range flows {
		offset := every * time.Duration(f) / time.Duration(len(flows))
		wg.Go(func() {
			for i := range perFlow {
				if d := time.Until(start.Add(offset + time.Duration(i)*every)); d > 0 {
					time.Sleep(d)
				}
				if _, err := c.Write(payload); err != nil {
					errc <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errc)
	return perFlow * len(flows) * l3Segments, <-errc
}

// settle waits until socket i's datagram count stops moving, or reaches
// want, within two seconds.
func settle(tb testing.TB, c *l3Child, i int, want uint64) l3Sample {
	tb.Helper()
	last := c.sample(tb)
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		s := c.sample(tb)
		if s.datagrams[i] >= want || s.datagrams[i] == last.datagrams[i] {
			return s
		}
		last = s
	}
	return last
}

// l3Side is one engine of an L3 pair: its socket's index in the receiver and
// the flows that send to it, and what its windows counted.
type l3Side struct {
	i                        int
	flows                    []*stdnet.UDPConn
	cpu, got, sent, receives uint64
}

// window sends n datagrams to the side's socket at l3Rate and adds the
// receiver's CPU, the datagrams read, the datagrams sent and the receives to
// the side's totals.
func (s *l3Side) window(b *testing.B, child *l3Child, n int) {
	before := child.sample(b)
	sent, err := sendL3(s.flows, n)
	if err != nil {
		b.Fatal(err)
	}
	after := settle(b, child, s.i, before.datagrams[s.i]+uint64(sent))
	s.cpu += after.cpu - before.cpu
	s.got += after.datagrams[s.i] - before.datagrams[s.i]
	s.receives += after.receives[s.i] - before.receives[s.i]
	s.sent += uint64(sent)
}

// BenchmarkLoadL3 is L3 paired: the recvmmsg reader against itself, the
// calibration of the P4a gate, whose B — GRO in datagramSource — lands with
// P4a (l3Sources). It runs over the veth pair scripts/ci/net-veth.sh sets up,
// or is skipped.
func BenchmarkLoadL3(b *testing.B) {
	b.Run("pair=recvmmsg:recvmmsg", func(b *testing.B) { benchPairedL3(b, "recvmmsg", "recvmmsg") })
}

// benchPairedL3 runs one paired round of L3: both engines' sockets in one
// receiver, b.N datagrams in 2×l3Windows windows, A, B, B, A, …. It
// reports each engine's CPU a datagram, datagrams a receive and loss, and the
// ratios of the first two, B over A.
func benchPairedL3(b *testing.B, engineA, engineB string) {
	peer := os.Getenv(l3PeerEnv)
	if peer == "" {
		b.Skip("L3 runs over the veth pair of scripts/ci/net-veth.sh: " + l3PeerEnv + " is unset")
	}
	child := startL3Child(b, os.Getenv(l3ExecEnv), stdnet.JoinHostPort(peer, "0"), engineA, engineB)
	sides := [2]*l3Side{{i: 0}, {i: 1}}
	for _, s := range sides {
		for range l3Flows {
			c, err := dialGSO(os.Getenv(l3LocalEnv), child.addrs[s.i])
			if err != nil {
				b.Fatal(err)
			}
			s.flows = append(s.flows, c)
			b.Cleanup(func() { _ = c.Close() })
		}
	}
	per := max(b.N/(2*l3Windows), l3Flows*l3Segments)
	b.ResetTimer()
	//: A, B, B, A, …: every window of B has one of A beside it.
	for w := range l3Windows {
		first, second := sides[0], sides[1]
		if w%2 == 1 {
			first, second = second, first
		}
		first.window(b, child, per)
		second.window(b, child, per)
	}
	b.StopTimer()
	var cpu, dps [2]float64
	for k, s := range sides {
		if s.got == 0 || s.receives == 0 {
			b.Fatalf("engine %d read no datagram", k)
		}
		cpu[k] = float64(s.cpu) / float64(s.got)
		dps[k] = float64(s.got) / float64(s.receives)
		b.ReportMetric(1-float64(s.got)/float64(s.sent), string("ab"[k])+":lost/op")
	}
	b.ReportMetric(cpu[0], "a:cpu-ns/datagram")
	b.ReportMetric(cpu[1], "b:cpu-ns/datagram")
	b.ReportMetric(cpu[1]/cpu[0], "ratio:cpu-ns/datagram")
	b.ReportMetric(dps[0], "a:datagrams/syscall")
	b.ReportMetric(dps[1], "b:datagrams/syscall")
	b.ReportMetric(dps[1]/dps[0], "ratio:datagrams/syscall")
}

// TestLoadL3_wired runs L3's sender and receiver on loopback: one GSO send of
// one flow must reach the receiver as l3Segments datagrams, counted with the
// receives that read them — the harness wired, its gate not yet required.
func TestLoadL3_wired(t *testing.T) {
	child := startL3Child(t, "", "127.0.0.1:0", "recvmmsg", "recvmmsg")
	//: each socket of the pair reads its own flow's send, and only it.
	for i, addr := range child.addrs {
		flow, err := dialGSO("", addr)
		if err != nil {
			t.Fatal(err)
		}
		before := child.sample(t)
		if sent, err := sendL3([]*stdnet.UDPConn{flow}, l3Segments); err != nil || sent != l3Segments {
			t.Fatalf("sent %d, %v, want one send of %d", sent, err, l3Segments)
		}
		after := settle(t, child, i, before.datagrams[i]+l3Segments)
		_ = flow.Close()
		if got := after.datagrams[i] - before.datagrams[i]; got != l3Segments {
			t.Fatalf("socket %d read %d datagrams of one GSO send, want %d", i, got, l3Segments)
		}
		if other := 1 - i; after.datagrams[other] != before.datagrams[other] {
			t.Fatalf("socket %d read datagrams sent to socket %d", other, i)
		}
		if after.receives[i] == before.receives[i] {
			t.Fatal("datagrams read and no receive counted")
		}
	}
	if l3Segments*l3DatagramSize > 65507 {
		t.Fatalf("a GSO send of %d bytes exceeds UDP's", l3Segments*l3DatagramSize)
	}
}
