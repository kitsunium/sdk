//go:build linux

// L3, the datagram load of the network plan's merge gate (plan P8,
// tools/netgate): UDP datagrams of 1200 bytes from a GSO sender of 54
// segments, 4 flows at a fixed rate, over a veth pair whose far end sits in a
// network namespace (scripts/ci/net-veth.sh). The receiver is a child — this
// binary re-run on TestLoadL3Child, inside the namespace — reading through
// the package's own datagramSource. The metrics are the machine's CPU per
// datagram — every CPU's busy time in /proc/stat over the window, the sender,
// the receiver and the kernel's softirq work between them, which is where GRO
// saves: a GSO send is segmented on receive without it, in softirq charged
// mostly to the sender — and the datagrams each receive returned; the
// receiver's own CPU (rusage) is reported beside, not gated. Engine A is the recvmmsg reader
// with UDP GRO off, one datagram a message; engine B is the same reader with
// GRO on, each message a GSO send's datagrams, split as the read loop splits
// them (P4a, ADR 0192).

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
	// l3Window is how many datagrams one sample sends, whatever -benchtime
	// says. /proc/stat counts in USER_HZ ticks — 100 a second on every Linux
	// ABI, 10 ms each —, so at 100 000 datagrams a tick was 100 ns a
	// datagram, 2.5 % of engine A, and the interval widened by the
	// quantisation alone; at 400 000 it is 25 ns (ADR 0192 §D6). Two seconds
	// a sample at l3Rate.
	l3Window = 400_000
	// l3ReadBuffer is the receive buffer the receiver asks for, so a burst of
	// every flow's segments is queued rather than dropped; the kernel caps it
	// at net.core.rmem_max, and the bench reports what was lost.
	l3ReadBuffer = 8 << 20
)

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

// The engines of L3, as the child is told which to read with.
const (
	// l3EngineA is the recvmmsg reader with GRO off.
	l3EngineA = "recvmmsg"
	// l3EngineB is the recvmmsg reader with GRO on.
	l3EngineB = "gro"
)

// TestLoadL3Child is the receiving half of BenchmarkLoadL3, and runs only in
// the child it starts: it binds, prints its address, reads through
// datagramSource counting datagrams and receives, and answers each line on
// stdin with its CPU, datagrams and receives, until stdin closes. Under GRO a
// message counts the datagrams it carries, as dispatch serves them.
func TestLoadL3Child(t *testing.T) {
	engine, ok := strings.CutPrefix(os.Getenv(l3ChildEnv), "L3:")
	if !ok || (engine != l3EngineA && engine != l3EngineB) {
		t.Skip("the receiving half of BenchmarkLoadL3: run by it, in a child")
	}
	pc, err := stdnet.ListenPacket("udp", os.Getenv(l3BindEnv))
	if err != nil {
		t.Fatal(err)
	}
	if udp, ok := pc.(*stdnet.UDPConn); ok {
		_ = udp.SetReadBuffer(l3ReadBuffer)
	}
	src := newDatagramSource(pc, engine == l3EngineB)
	if r, isBatched := src.(*multiReader); !isBatched || r.gro != (engine == l3EngineB) {
		t.Fatalf("engine %s read through %T, GRO %v", engine, src, isBatched && r.gro)
	}
	var datagrams, receives, truncated atomic.Uint64
	go func() {
		slots := newSlots(l3Slots, src.slotSize(l3DatagramSize))
		for {
			n, rerr := src.readBatch(slots)
			if rerr != nil {
				return
			}
			receives.Add(1)
			for i := range n {
				if slots[i].cut {
					truncated.Add(1)
					continue
				}
				datagrams.Add(uint64(slots[i].count()))
			}
		}
	}()
	fmt.Printf("addr %s\n", pc.LocalAddr())
	l3Answer(os.Stdin, os.Stdout, func() []uint64 {
		return []uint64{datagrams.Load(), receives.Load(), truncated.Load()}
	})
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

// startL3Child runs the receiver reading with engine, bound to bind, under
// prefix when given.
func startL3Child(tb testing.TB, engine, prefix, bind string) *l3Child {
	tb.Helper()
	env := []string{l3ChildEnv + "=L3:" + engine, l3BindEnv + "=" + bind}
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

// sample reads the receiver's CPU, datagrams, receives and truncated messages.
func (c *l3Child) sample(tb testing.TB) [4]uint64 {
	tb.Helper()
	if _, err := c.in.Write([]byte("s\n")); err != nil {
		tb.Fatal(err)
	}
	line, err := c.out.ReadString('\n')
	f := strings.Fields(line)
	if err != nil || len(f) != 4 {
		tb.Fatalf("L3 receiver sample %q, %v", line, err)
	}
	var vs [4]uint64
	for i := range vs {
		v, perr := strconv.ParseUint(f[i], 10, 64)
		if perr != nil {
			tb.Fatalf("L3 receiver sample %q: %v", line, perr)
		}
		vs[i] = v
	}
	return vs
}

// clockTicks is USER_HZ, the unit of /proc/stat's times: 100 on every Linux
// ABI, whatever the kernel's own HZ (proc(5)).
const clockTicks = 100

// machineBusy returns every CPU's busy time so far, in ns: the first line of
// /proc/stat, every field but idle and iowait. /proc/stat is not namespaced, so
// it counts the receiver in its namespace, the sender outside it and the
// softirq work between them.
func machineBusy(tb testing.TB) uint64 {
	tb.Helper()
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		tb.Fatal(err)
	}
	line, _, _ := strings.Cut(string(b), "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		tb.Fatalf("/proc/stat starts %q, want the cpu line", line)
	}
	var busy uint64
	// user nice system idle iowait irq softirq steal …
	for i, v := range f[1:] {
		if i == 3 || i == 4 {
			continue
		}
		n, perr := strconv.ParseUint(v, 10, 64)
		if perr != nil {
			tb.Fatalf("/proc/stat field %d %q: %v", i, v, perr)
		}
		busy += n
	}
	return busy * uint64(time.Second) / clockTicks
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
func settle(tb testing.TB, c *l3Child, want uint64) [4]uint64 {
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

// BenchmarkLoadL3 is L3 on each engine of the P4a gate: the recvmmsg reader
// with GRO off (A) and on (B). It runs over the veth pair scripts/ci/net-veth.sh
// sets up, or is skipped.
func BenchmarkLoadL3(b *testing.B) {
	for _, engine := range []string{l3EngineA, l3EngineB} {
		b.Run("engine="+engine, func(b *testing.B) { benchL3(b, engine) })
	}
}

// benchL3 is one engine's run of L3.
func benchL3(b *testing.B, engine string) {
	peer := os.Getenv(l3PeerEnv)
	if peer == "" {
		b.Skip("L3 runs over the veth pair of scripts/ci/net-veth.sh: " + l3PeerEnv + " is unset")
	}
	child := startL3Child(b, engine, os.Getenv(l3ExecEnv), stdnet.JoinHostPort(peer, "0"))
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
	busyBefore := machineBusy(b)
	b.ResetTimer()
	// The window is the bench's, not b.N: the metrics are a datagram's, and the
	// instrument's resolution sets the window (l3Window).
	sent, err := sendL3(flows, l3Window)
	if err != nil {
		b.Fatal(err)
	}
	after := settle(b, child, before[1]+uint64(sent))
	busyAfter := machineBusy(b)
	b.StopTimer()
	got := after[1] - before[1]
	if got == 0 {
		b.Fatal("the receiver read no datagram")
	}
	if cut := after[3] - before[3]; cut != 0 {
		b.Fatalf("engine %s dropped %d messages cut short (MSG_TRUNC/MSG_CTRUNC)", engine, cut)
	}
	b.ReportMetric(float64(busyAfter-busyBefore)/float64(got), "cpu-ns/datagram")
	b.ReportMetric(float64(after[0]-before[0])/float64(got), "rcv-cpu-ns/datagram")
	b.ReportMetric(float64(got)/float64(after[2]-before[2]), "datagrams/syscall")
	b.ReportMetric(1-float64(got)/float64(sent), "lost/op")
}

// TestLoadL3_wired runs L3's sender and receiver on loopback, on each engine:
// one GSO send of one flow must reach the receiver as l3Segments datagrams,
// counted with the receives that read them, none cut short — on GRO, one
// message the read loop splits, read with its segment size on every call.
func TestLoadL3_wired(t *testing.T) {
	if l3Segments*l3DatagramSize > 65507 {
		t.Fatalf("a GSO send of %d bytes exceeds UDP's", l3Segments*l3DatagramSize)
	}
	for _, engine := range []string{l3EngineA, l3EngineB} {
		t.Run(engine, func(t *testing.T) {
			child := startL3Child(t, engine, "", "127.0.0.1:0")
			flow, err := dialGSO("", child.addr)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = flow.Close() }()
			// Three sends, three reads at least: the segment size must reach
			// every call, not the first only.
			for range 3 {
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
				if cut := after[3] - before[3]; cut != 0 {
					t.Fatalf("%d messages cut short", cut)
				}
				if engine == l3EngineB && after[2]-before[2] != 1 {
					t.Fatalf("GRO read one GSO send in %d receives, want 1", after[2]-before[2])
				}
			}
		})
	}
}
