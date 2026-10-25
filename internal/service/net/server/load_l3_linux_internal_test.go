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
	if os.Getenv(l3ChildEnv) != "L3:recvmmsg" {
		t.Skip("the receiving half of BenchmarkLoadL3: run by it, in a child")
	}
	pc, err := stdnet.ListenPacket("udp", os.Getenv(l3BindEnv))
	if err != nil {
		t.Fatal(err)
	}
	if udp, ok := pc.(*stdnet.UDPConn); ok {
		_ = udp.SetReadBuffer(l3ReadBuffer)
	}
	src := newDatagramSource(pc)
	var datagrams, receives atomic.Uint64
	go func() {
		slots := newSlots(l3Slots, l3DatagramSize)
		for {
			n, rerr := src.readBatch(slots)
			if rerr != nil {
				return
			}
			receives.Add(1)
			datagrams.Add(uint64(n))
		}
	}()
	fmt.Printf("addr %s\n", pc.LocalAddr())
	l3Answer(os.Stdin, os.Stdout, func() []uint64 { return []uint64{datagrams.Load(), receives.Load()} })
	_ = pc.Close()
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
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	addr string
}

// startL3Child runs the receiver bound to bind, under prefix when given.
func startL3Child(tb testing.TB, prefix, bind string) *l3Child {
	tb.Helper()
	env := []string{l3ChildEnv + "=L3:recvmmsg", l3BindEnv + "=" + bind}
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
	addr, ok := strings.CutPrefix(strings.TrimSpace(line), "addr ")
	if err != nil || !ok {
		tb.Fatalf("L3 receiver printed %q, %v, want its address", line, err)
	}
	c.addr = addr
	return c
}

// sample reads the receiver's CPU, datagrams and receives.
func (c *l3Child) sample(tb testing.TB) [3]uint64 {
	tb.Helper()
	if _, err := c.in.Write([]byte("s\n")); err != nil {
		tb.Fatal(err)
	}
	line, err := c.out.ReadString('\n')
	f := strings.Fields(line)
	if err != nil || len(f) != 3 {
		tb.Fatalf("L3 receiver sample %q, %v", line, err)
	}
	var vs [3]uint64
	for i := range vs {
		v, perr := strconv.ParseUint(f[i], 10, 64)
		if perr != nil {
			tb.Fatalf("L3 receiver sample %q: %v", line, perr)
		}
		vs[i] = v
	}
	return vs
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

// settle waits until the receiver's datagram count stops moving, or reaches
// want, within two seconds.
func settle(tb testing.TB, c *l3Child, want uint64) [3]uint64 {
	tb.Helper()
	last := c.sample(tb)
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		s := c.sample(tb)
		if s[1] >= want || s[1] == last[1] {
			return s
		}
		last = s
	}
	return last
}

// BenchmarkLoadL3 is L3 on today's recvmmsg reader, the P4a gate's engine A.
// It runs over the veth pair scripts/ci/net-veth.sh sets up, or is skipped.
func BenchmarkLoadL3(b *testing.B) {
	b.Run("engine=recvmmsg", func(b *testing.B) {
		peer := os.Getenv(l3PeerEnv)
		if peer == "" {
			b.Skip("L3 runs over the veth pair of scripts/ci/net-veth.sh: " + l3PeerEnv + " is unset")
		}
		child := startL3Child(b, os.Getenv(l3ExecEnv), stdnet.JoinHostPort(peer, "0"))
		flows := make([]*stdnet.UDPConn, l3Flows)
		for i := range flows {
			c, err := dialGSO(os.Getenv(l3LocalEnv), child.addr)
			if err != nil {
				b.Fatal(err)
			}
			flows[i] = c
			b.Cleanup(func() { _ = c.Close() })
		}
		before := child.sample(b)
		b.ResetTimer()
		sent, err := sendL3(flows, b.N)
		if err != nil {
			b.Fatal(err)
		}
		after := settle(b, child, before[1]+uint64(sent))
		b.StopTimer()
		got := after[1] - before[1]
		if got == 0 {
			b.Fatal("the receiver read no datagram")
		}
		b.ReportMetric(float64(after[0]-before[0])/float64(got), "cpu-ns/datagram")
		b.ReportMetric(float64(got)/float64(after[2]-before[2]), "datagrams/syscall")
		b.ReportMetric(1-float64(got)/float64(sent), "lost/op")
	})
}

// TestLoadL3_wired runs L3's sender and receiver on loopback: one GSO send of
// one flow must reach the receiver as l3Segments datagrams, counted with the
// receives that read them — the harness wired, its gate not yet required.
func TestLoadL3_wired(t *testing.T) {
	child := startL3Child(t, "", "127.0.0.1:0")
	flow, err := dialGSO("", child.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = flow.Close() }()
	before := child.sample(t)
	if sent, err := sendL3([]*stdnet.UDPConn{flow}, l3Segments); err != nil || sent != l3Segments {
		t.Fatalf("sent %d, %v, want one send of %d", sent, err, l3Segments)
	}
	after := settle(t, child, before[1]+l3Segments)
	if got := after[1] - before[1]; got != l3Segments {
		t.Fatalf("the receiver read %d datagrams of one GSO send, want %d", got, l3Segments)
	}
	if after[2] == before[2] {
		t.Fatal("datagrams read and no receive counted")
	}
	if l3Segments*l3DatagramSize > 65507 {
		t.Fatalf("a GSO send of %d bytes exceeds UDP's", l3Segments*l3DatagramSize)
	}
}
