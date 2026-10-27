//go:build linux

// L3, the datagram load of the network plan's merge gate (plan P8,
// tools/netgate): UDP datagrams of 1200 bytes from a GSO sender of 54
// segments, 4 flows at a fixed rate, over a veth pair whose far end sits in a
// network namespace (scripts/ci/net-veth.sh). The receiver is a child — this
// binary re-run on TestLoadL3Child, inside the namespace — reading through
// the package's own datagramSource. The gated metric is the CPU a datagram
// costs (l3CPU): the receiver's rusage, the sender's — this process's —, and
// the irq and softirq time /proc/stat accounts apart from any task, which is
// where GRO saves: a GSO send is segmented on receive without it, in softirq.
// The datagrams each receive returned and each part of the CPU are reported
// beside, not gated (ADR 0192 §D6, §D7). Engine A is the recvmmsg reader with
// UDP GRO off, one datagram a message; engine B is the same reader with GRO
// on, each message a GSO send's datagrams, split as the read loop splits them
// (P4a, ADR 0192).
//
// The measure is paired, as L1's and L2's (load_gate_linux_test.go, ADR 0190):
// one receiver reads both engines' sockets, each through its own source, and a
// round sends to them in windows A, B, B, A, … at the one fixed rate. Every
// window is metered the same way whichever engine it feeds (l3Round), so a
// round's ratio, B over A, is one paired sample, whose interval tools/netgate
// judges.

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
// L1's and L2's pairedWindows: A, B, then B, A, repeated. Each engine is sent
// b.N datagrams a round, not half of it as on L1 and L2: /proc/stat counts in
// USER_HZ ticks, 10 ms each, and the job's 400 000 an engine keep a tick at
// 25 ns a datagram (ADR 0192 §D6).
const l3Windows = 8

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
// the child it starts: it binds one socket an engine, prints their addresses,
// reads each through its engine's source counting datagrams, receives and
// messages cut short, and answers each line on stdin with its CPU and those
// counts, until stdin closes. Under GRO a message counts the datagrams it
// carries, as dispatch serves them.
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
			vs = append(vs, r.datagrams.Load(), r.receives.Load(), r.truncated.Load())
		}
		return vs
	})
}

// l3Sources builds each L3 engine's reader over its socket, by label, with
// whether that reader must coalesce: the one table an engine joins.
var l3Sources = map[string]struct {
	build func(stdnet.PacketConn) datagramSource
	gro   bool
}{
	l3EngineA: {build: func(pc stdnet.PacketConn) datagramSource { return newDatagramSource(pc, false) }},
	l3EngineB: {build: func(pc stdnet.PacketConn) datagramSource { return newDatagramSource(pc, true) }, gro: true},
}

// l3Reader is one engine's socket in the receiver, and what it counted.
type l3Reader struct {
	pc                             stdnet.PacketConn
	datagrams, receives, truncated atomic.Uint64
}

// listenL3 binds a socket for engine and reads it through the engine's
// source, its slots as large as the source asks for, counting datagrams —
// every one a coalesced message carries —, receives and messages cut short,
// until the socket closes.
//
// Goroutine lifecycle: it launches the reader, which returns once the socket
// is closed.
func listenL3(engine, bind string) (*l3Reader, error) {
	source, ok := l3Sources[engine]
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
	src := source.build(pc)
	if mr, batched := src.(*multiReader); !batched || mr.gro != source.gro {
		_ = pc.Close()
		return nil, fmt.Errorf("engine %s read through %T, GRO %v", engine, src, batched && mr.gro)
	}
	r := &l3Reader{pc: pc}
	go func() {
		slots := newSlots(l3Slots, src.slotSize(l3DatagramSize))
		for {
			n, rerr := src.readBatch(slots)
			if rerr != nil {
				return
			}
			r.receives.Add(1)
			for i := range n {
				if slots[i].cut {
					r.truncated.Add(1)
					continue
				}
				r.datagrams.Add(uint64(slots[i].count()))
			}
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

// l3Sample is the receiver's CPU, then each socket's datagrams, receives and
// messages cut short, in the order of the engines it was started with.
type l3Sample struct {
	cpu                            uint64
	datagrams, receives, truncated []uint64
}

// sample reads the receiver's CPU and each socket's counts.
func (c *l3Child) sample(tb testing.TB) l3Sample {
	tb.Helper()
	if _, err := c.in.Write([]byte("s\n")); err != nil {
		tb.Fatal(err)
	}
	line, err := c.out.ReadString('\n')
	f := strings.Fields(line)
	if err != nil || len(f) != 1+3*len(c.addrs) {
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
		s.datagrams = append(s.datagrams, vs[1+3*i])
		s.receives = append(s.receives, vs[2+3*i])
		s.truncated = append(s.truncated, vs[3+3*i])
	}
	return s
}

// clockTicks is USER_HZ, the unit of /proc/stat's times: 100 on every Linux
// ABI, whatever the kernel's own HZ (proc(5)).
const clockTicks = 100

// l3CPU is the CPU L3 cost, in ns, by part: one window's, or the sum of an
// engine's windows.
type l3CPU struct {
	// receiver and sender are the two processes' user+system rusage.
	receiver, sender uint64
	// irq is the irq and softirq time of every CPU in /proc/stat. With
	// CONFIG_IRQ_TIME_ACCOUNTING the kernel charges it to no task, so it adds
	// to the two rusages without overlap; without it, a tick that lands in
	// softirq is charged here and to the task it interrupted, so that time
	// counts twice when the task is the receiver or the sender (the bench
	// job records which accounting the runner has; ADR 0192 §D6).
	irq uint64
}

// total is the CPU, the gated metric.
func (c l3CPU) total() uint64 { return c.receiver + c.sender + c.irq }

// plus is c and d, part by part.
func (c l3CPU) plus(d l3CPU) l3CPU {
	return l3CPU{receiver: c.receiver + d.receiver, sender: c.sender + d.sender, irq: c.irq + d.irq}
}

// errInconsistentCPU reports a gated CPU below what the receiver alone
// spent, which no sound measure of the whole can give: the first measure,
// /proc/stat's busy time, did on one runner in two (ADR 0192 §D6).
var errInconsistentCPU = errors.New("the gated CPU is below the receiver's own")

// check refuses a window whose gated CPU is below the receiver's own: a part
// that went backwards wraps, and the sum falls below it.
func (c l3CPU) check() error {
	if c.total() < c.receiver {
		return fmt.Errorf("%w: %d < %d ns", errInconsistentCPU, c.total(), c.receiver)
	}
	return nil
}

// processCPU is this process's user+system time so far, in ns.
func processCPU(tb testing.TB) uint64 {
	tb.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		tb.Fatal(err)
	}
	return uint64(ru.Utime.Nano() + ru.Stime.Nano())
}

// irqTime returns every CPU's irq and softirq time so far, in ns: the first
// line of /proc/stat, its sixth and seventh fields. /proc/stat is not
// namespaced, so it counts the softirq work of both ends of the pair.
func irqTime(tb testing.TB) uint64 {
	tb.Helper()
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		tb.Fatal(err)
	}
	line, _, _ := strings.Cut(string(b), "\n")
	f := strings.Fields(line)
	// cpu user nice system idle iowait irq softirq …
	if len(f) < 8 || f[0] != "cpu" {
		tb.Fatalf("/proc/stat starts %q, want the cpu line", line)
	}
	var ticks uint64
	for _, v := range f[6:8] {
		n, perr := strconv.ParseUint(v, 10, 64)
		if perr != nil {
			tb.Fatalf("/proc/stat field %q: %v", v, perr)
		}
		ticks += n
	}
	return ticks * uint64(time.Second) / clockTicks
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

// l3Count is what one window, or the sum of an engine's windows, counted.
type l3Count struct {
	cpu                                 l3CPU
	got, sent, receives, truncated, win uint64
}

// plus is c and d, field by field.
func (c l3Count) plus(d l3Count) l3Count {
	return l3Count{
		cpu: c.cpu.plus(d.cpu), got: c.got + d.got, sent: c.sent + d.sent,
		receives: c.receives + d.receives, truncated: c.truncated + d.truncated, win: c.win + d.win,
	}
}

// l3Window sends n datagrams to engine i's socket and meters them: the one
// function a window of either engine goes through, so A and B are measured
// identically.
type l3Window func(tb testing.TB, i, n int) l3Count

// l3Round runs one paired round, 2×l3Windows windows of per datagrams in the
// order A, B, B, A, A, B, …, through window, and sums each engine's. It
// refuses a window whose CPU is inconsistent (l3CPU.check).
func l3Round(tb testing.TB, window l3Window, per int) [2]l3Count {
	tb.Helper()
	var sides [2]l3Count
	//: A, B, B, A, …: every window of B has one of A beside it.
	for w := range l3Windows {
		order := [2]int{0, 1}
		if w%2 == 1 {
			order = [2]int{1, 0}
		}
		for _, i := range order {
			c := window(tb, i, per)
			if err := c.cpu.check(); err != nil {
				tb.Fatalf("engine %c, window %d: %v", "ab"[i], w, err)
			}
			sides[i] = sides[i].plus(c)
		}
	}
	return sides
}

// l3Metrics is a round's metrics as BenchmarkLoadL3 reports them: each
// engine's CPU a datagram and its parts, datagrams a receive and loss, and
// the ratios, B over A, of the two netgate judges. A message cut short on
// either engine refuses the round.
//
// The CPU is divided by the datagrams SENT, the same count for both engines
// (l3Round), not by those received: a datagram the receive queue dropped still
// cost the sender its send and the kernel its softirq work up to the drop, and
// the engine that drops more — A, whose one-datagram buffers each charge a
// whole truesize against rmem_max — would be charged that work over fewer
// datagrams. Over the same sent count the ratio is the two engines' CPU for the
// same work (ADR 0192 §D6); datagrams a receive stays a count of those read.
func l3Metrics(sides [2]l3Count) (map[string]float64, error) {
	m := make(map[string]float64)
	var cpu, dps [2]float64
	for k, s := range sides {
		p := string("ab"[k]) + ":"
		if s.got == 0 || s.receives == 0 {
			return nil, fmt.Errorf("engine %c read no datagram", "ab"[k])
		}
		if s.truncated != 0 {
			return nil, fmt.Errorf("engine %c dropped %d messages cut short (MSG_TRUNC/MSG_CTRUNC)", "ab"[k], s.truncated)
		}
		got, sent := float64(s.got), float64(s.sent)
		cpu[k] = float64(s.cpu.total()) / sent
		dps[k] = got / float64(s.receives)
		m[p+"cpu-ns/datagram"] = cpu[k]
		m[p+"rcv-cpu-ns/datagram"] = float64(s.cpu.receiver) / sent
		m[p+"snd-cpu-ns/datagram"] = float64(s.cpu.sender) / sent
		m[p+"irq-cpu-ns/datagram"] = float64(s.cpu.irq) / sent
		m[p+"datagrams/syscall"] = dps[k]
		m[p+"lost/op"] = 1 - got/sent
	}
	m["ratio:cpu-ns/datagram"] = cpu[1] / cpu[0]
	m["ratio:datagrams/syscall"] = dps[1] / dps[0]
	return m, nil
}

// BenchmarkLoadL3 is L3 paired: the recvmmsg reader against itself, the
// calibration of the P4a gate, and against itself with GRO, P4a's gate. It
// runs over the veth pair scripts/ci/net-veth.sh sets up, or is skipped.
func BenchmarkLoadL3(b *testing.B) {
	for _, p := range [][2]string{{l3EngineA, l3EngineA}, {l3EngineA, l3EngineB}} {
		b.Run("pair="+p[0]+":"+p[1], func(b *testing.B) { benchPairedL3(b, p[0], p[1]) })
	}
}

// benchPairedL3 runs one paired round of L3: both engines' sockets in one
// receiver, b.N datagrams an engine in l3Windows windows each, A, B, B, A, …,
// every window metered by l3Meter.
func benchPairedL3(b *testing.B, engineA, engineB string) {
	peer := os.Getenv(l3PeerEnv)
	if peer == "" {
		b.Skip("L3 runs over the veth pair of scripts/ci/net-veth.sh: " + l3PeerEnv + " is unset")
	}
	child := startL3Child(b, os.Getenv(l3ExecEnv), stdnet.JoinHostPort(peer, "0"), engineA, engineB)
	var flows [2][]*stdnet.UDPConn
	for i := range flows {
		for range l3Flows {
			c, err := dialGSO(os.Getenv(l3LocalEnv), child.addrs[i])
			if err != nil {
				b.Fatal(err)
			}
			flows[i] = append(flows[i], c)
			b.Cleanup(func() { _ = c.Close() })
		}
	}
	per := max(b.N/l3Windows, l3Flows*l3Segments)
	b.ResetTimer()
	sides := l3Round(b, l3Meter(child, flows), per)
	b.StopTimer()
	m, err := l3Metrics(sides)
	if err != nil {
		b.Fatal(err)
	}
	for unit, v := range m {
		b.ReportMetric(v, unit)
	}
}

// l3Meter is the window of a running receiver: it reads the receiver's
// sample, this process's rusage and the irq time, sends to engine i's flows,
// waits for the receiver to settle, and reads the three again.
func l3Meter(child *l3Child, flows [2][]*stdnet.UDPConn) l3Window {
	return func(tb testing.TB, i, n int) l3Count {
		tb.Helper()
		before := child.sample(tb)
		snd, irq := processCPU(tb), irqTime(tb)
		sent, err := sendL3(flows[i], n)
		if err != nil {
			tb.Fatal(err)
		}
		after := settle(tb, child, i, before.datagrams[i]+uint64(sent))
		cpu := l3CPU{receiver: after.cpu - before.cpu, sender: processCPU(tb) - snd, irq: irqTime(tb) - irq}
		return l3Count{
			cpu: cpu, got: after.datagrams[i] - before.datagrams[i], sent: uint64(sent),
			receives: after.receives[i] - before.receives[i], truncated: after.truncated[i] - before.truncated[i], win: 1,
		}
	}
}

// TestLoadL3_wired runs L3's sender and receiver on loopback, both engines in
// one receiver: one GSO send of one flow must reach its socket, and only it,
// as l3Segments datagrams, counted with the receives that read them, none cut
// short — on GRO, one message the read loop splits, read with its segment
// size on every call.
func TestLoadL3_wired(t *testing.T) {
	if l3Segments*l3DatagramSize > 65507 {
		t.Fatalf("a GSO send of %d bytes exceeds UDP's", l3Segments*l3DatagramSize)
	}
	engines := []string{l3EngineA, l3EngineB}
	child := startL3Child(t, "", "127.0.0.1:0", engines...)
	for i, addr := range child.addrs {
		flow, err := dialGSO("", addr)
		if err != nil {
			t.Fatal(err)
		}
		// Three sends, three reads at least: the segment size must reach
		// every call, not the first only.
		for range 3 {
			before := child.sample(t)
			if sent, err := sendL3([]*stdnet.UDPConn{flow}, l3Segments); err != nil || sent != l3Segments {
				t.Fatalf("sent %d, %v, want one send of %d", sent, err, l3Segments)
			}
			after := settle(t, child, i, before.datagrams[i]+l3Segments)
			if got := after.datagrams[i] - before.datagrams[i]; got != l3Segments {
				t.Fatalf("%s read %d datagrams of one GSO send, want %d", engines[i], got, l3Segments)
			}
			if other := 1 - i; after.datagrams[other] != before.datagrams[other] {
				t.Fatalf("%s read datagrams sent to %s", engines[other], engines[i])
			}
			receives := after.receives[i] - before.receives[i]
			if receives == 0 {
				t.Fatal("datagrams read and no receive counted")
			}
			if cut := after.truncated[i] - before.truncated[i]; cut != 0 {
				t.Fatalf("%s: %d messages cut short", engines[i], cut)
			}
			if engines[i] == l3EngineB && receives != 1 {
				t.Fatalf("GRO read one GSO send in %d receives, want 1", receives)
			}
		}
		_ = flow.Close()
	}
}

// TestL3CPU_check pins the consistency guard: a gated CPU below the receiver's
// own fails the run, as /proc/stat's busy time did on one runner in two; the
// sum of the parts never does.
func TestL3CPU_check(t *testing.T) {
	for _, c := range []struct {
		name string
		cpu  l3CPU
		fail bool
	}{
		{name: "the parts", cpu: l3CPU{receiver: 345, sender: 200, irq: 50}},
		{name: "no irq time", cpu: l3CPU{receiver: 345, sender: 200}},
		{name: "a part that went backwards and wrapped", cpu: l3CPU{receiver: 345, sender: ^uint64(0) - 100}, fail: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := c.cpu.check(); (err != nil) != c.fail || (c.fail && !errors.Is(err, errInconsistentCPU)) {
				t.Fatalf("check = %v, want failing %v", err, c.fail)
			}
		})
	}
}

// TestL3Round_symmetric pins the paired measure on a scripted window: A and B
// get as many windows and datagrams, in the order A, B, B, A, …; the CPU a
// datagram is the sum of its three parts, each non-negative; two engines of
// one cost give a ratio of 1 whatever the runner adds to every window; B at
// 60 % of A on every part gives 0.6; and a message cut short refuses the
// round.
func TestL3Round_symmetric(t *testing.T) {
	const per = 1000
	var order []int
	// The runner's drift: every window costs more than the one before,
	// whichever engine it feeds.
	scripted := func(cost func(i int) l3CPU) l3Window {
		return func(_ testing.TB, i, n int) l3Count {
			order = append(order, i)
			drift := uint64(len(order))
			c := cost(i)
			c.receiver += drift
			c.irq += drift
			return l3Count{cpu: c, got: uint64(n), sent: uint64(n), receives: uint64(n / l3Segments), win: 1}
		}
	}
	same := func(int) l3CPU { return l3CPU{receiver: 300 * per, sender: 200 * per, irq: 100 * per} }
	sides := l3Round(t, scripted(same), per)
	want := []int{0, 1, 1, 0, 0, 1, 1, 0, 0, 1, 1, 0, 0, 1, 1, 0}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("windows ran %v, want %v", order, want)
	}
	for k, s := range sides {
		if s.win != l3Windows || s.got != l3Windows*per || s.sent != s.got {
			t.Fatalf("engine %c: %d windows, %d datagrams, want %d of %d", "ab"[k], s.win, s.got, l3Windows, per)
		}
	}
	m, err := l3Metrics(sides)
	if err != nil {
		t.Fatal(err)
	}
	// The drift summed over A's windows (1, 4, 5, 8, …) and B's (2, 3, 6, 7, …)
	// is the same: the order cancels it.
	if r, d := m["ratio:cpu-ns/datagram"], m["ratio:datagrams/syscall"]; r != 1 || d != 1 {
		t.Fatalf("two engines of one cost gave ratios %g and %g, want 1", r, d)
	}
	for unit, v := range m {
		if v < 0 {
			t.Fatalf("%s = %g, want non-negative", unit, v)
		}
	}
	for _, p := range []string{"a:", "b:"} {
		sum := m[p+"rcv-cpu-ns/datagram"] + m[p+"snd-cpu-ns/datagram"] + m[p+"irq-cpu-ns/datagram"]
		if d := sum - m[p+"cpu-ns/datagram"]; d > 1e-9 || d < -1e-9 {
			t.Fatalf("%s parts sum to %g, the CPU is %g", p, sum, m[p+"cpu-ns/datagram"])
		}
	}
	order = nil
	cheaper := func(i int) l3CPU {
		c := same(i)
		if i == 1 {
			c = l3CPU{receiver: c.receiver * 6 / 10, sender: c.sender * 6 / 10, irq: c.irq * 6 / 10}
		}
		return c
	}
	if m, err = l3Metrics(l3Round(t, scripted(cheaper), per)); err != nil {
		t.Fatal(err)
	}
	if r := m["ratio:cpu-ns/datagram"]; r < 0.599 || r > 0.601 {
		t.Fatalf("B at 60 %% of A gave a ratio of %g", r)
	}
	// A losing a tenth of what it was sent, at the same CPU: the ratio is 1,
	// the loss charged to neither engine.
	lossy := sides
	lossy[0].got = lossy[0].sent * 9 / 10
	if m, err = l3Metrics(lossy); err != nil {
		t.Fatal(err)
	}
	if r := m["ratio:cpu-ns/datagram"]; r != 1 {
		t.Fatalf("A losing more at the same CPU gave a ratio of %g, want 1", r)
	}
	cut := [2]l3Count{{got: 1, receives: 1, sent: 1}, {got: 1, receives: 1, sent: 1, truncated: 1}}
	if _, err := l3Metrics(cut); err == nil {
		t.Fatal("a round with a message cut short gave metrics")
	}
}
