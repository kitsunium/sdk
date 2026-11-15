//go:build linux

// Package server — #977's third criterion on the L3 run: a datagram group
// with Shards(1) is the group without the option, byte for byte, under L3's
// load. L3's sender (paceL3: 1200-byte datagrams from 4 GSO flows of 54
// segments at 200 000 a second) stamps every datagram with its index and
// bytes that depend on it; a receiver — this binary re-run on
// TestShardsL3Child, in the veth pair's namespace on the L3 lane — serves two
// groups through Server.PacketGroup, the base and Shards(1), each checking
// every datagram it reads against the stamp. Both must bind one socket
// without SO_REUSEPORT, report one shard, and read every datagram exactly
// once, byte for byte.
package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	stdnet "net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// shardsL3Datagrams is what each group is sent on the L3 lane: one
	// engine's share of a round there, the bench job's -test.benchtime.
	shardsL3Datagrams = 400_000
	// shardsL3Role is the receiver's role, the prefix of l3ChildEnv's value;
	// the count of datagrams it must expect follows the colon.
	shardsL3Role = "S1:"
)

// shardsL3Groups are the receiver's two groups: the base, without the
// option, and Shards(1), each its name and its options.
var shardsL3Groups = [2]struct {
	name string
	opts []GroupOption
}{{name: "base"}, {name: "shards1", opts: []GroupOption{Shards(1)}}}

// stampL3 writes datagram seq's bytes into d: seq, big-endian, then bytes
// that depend on seq and on their place, so a byte out of place, cut short or
// from another datagram shows.
func stampL3(d []byte, seq uint32) {
	binary.BigEndian.PutUint32(d, seq)
	for k := 4; k < len(d); k++ {
		d[k] = byte(seq) ^ byte(seq>>8) ^ byte(k)
	}
}

// stampSends is paceL3's fill for stamped sends: send i carries datagrams
// i×l3Segments to (i+1)×l3Segments-1.
func stampSends(payload []byte, send int) {
	for s := range l3Segments {
		stampL3(payload[s*l3DatagramSize:(s+1)*l3DatagramSize], uint32(send*l3Segments+s))
	}
}

// l3Tally is what one group read: datagrams whose bytes are those sent,
// those that are not, and those read twice.
type l3Tally struct {
	mu      sync.Mutex
	seen    []bool
	scratch []byte
	exact   atomic.Uint64
	wrong   atomic.Uint64
	twice   atomic.Uint64
}

// newL3Tally expects datagrams 0 to n-1.
func newL3Tally(n int) *l3Tally {
	return &l3Tally{seen: make([]bool, n), scratch: make([]byte, l3DatagramSize)}
}

// note checks one datagram read.
func (y *l3Tally) note(p []byte) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if len(p) != l3DatagramSize {
		y.wrong.Add(1)
		return
	}
	seq := binary.BigEndian.Uint32(p)
	if int(seq) >= len(y.seen) {
		y.wrong.Add(1)
		return
	}
	stampL3(y.scratch, seq)
	if !bytes.Equal(p, y.scratch) {
		y.wrong.Add(1)
		return
	}
	if y.seen[seq] {
		y.twice.Add(1)
		return
	}
	y.seen[seq] = true
	y.exact.Add(1)
}

// TestShardsL3Child is the receiving half of runShardsL3, and runs only in
// the child it starts: it serves the base and Shards(1) on the address
// l3BindEnv names, prints each group's address, its sockets, how many of them
// have SO_REUSEPORT and the shards State reports, then answers each line on
// stdin with its CPU and each group's exact, wrong and repeated counts, until
// stdin closes. It starts no child.
func TestShardsL3Child(t *testing.T) {
	expect, ok := strings.CutPrefix(os.Getenv(l3ChildEnv), shardsL3Role)
	if !ok {
		t.Skip("the receiving half of TestShardsL3_oneSocketIsTheBase: run by it, in a child")
	}
	n, err := strconv.Atoi(expect)
	if err != nil || n <= 0 {
		t.Fatalf("%s=%q: want the count of datagrams", l3ChildEnv, os.Getenv(l3ChildEnv))
	}
	srv := New()
	var tallies [2]*l3Tally
	for i, g := range shardsL3Groups {
		y := newL3Tally(n)
		tallies[i] = y
		opts := append([]GroupOption{Listen("udp", os.Getenv(l3BindEnv)), ReceiveBuffer(l3ReadBuffer)}, g.opts...)
		srv.PacketGroup(g.name, opts...).HandleFunc(func(_ context.Context, p corenet.Packet) error {
			y.note(p.Data())
			return nil
		})
	}
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { swallowErr(srv.Close()) }()
	fmt.Println(shardsL3Layout(t, srv))
	l3Answer(os.Stdin, os.Stdout, func() []uint64 {
		var vs []uint64
		for _, y := range tallies {
			vs = append(vs, y.exact.Load(), y.wrong.Load(), y.twice.Load())
		}
		return vs
	})
}

// shardsL3Layout is the receiver's first line: "addr", each group's address,
// then for each group its sockets, those with SO_REUSEPORT, and the shards
// its State row reports.
func shardsL3Layout(t *testing.T, srv *Server) string {
	t.Helper()
	var addrs, sockets, reuse, rows []string
	for _, g := range shardsL3Groups {
		n, r := 0, 0
		for _, b := range shardSockets(srv) {
			if b.group != g.name {
				continue
			}
			n++
			if packetReusePort(t, b.pc) != 0 {
				r++
			}
		}
		row := -1
		addr := ""
		for _, l := range srv.State().Listeners {
			if l.Group == g.name {
				row, addr = l.Shards, l.Address
			}
		}
		addrs = append(addrs, addr)
		sockets, reuse, rows = append(sockets, strconv.Itoa(n)), append(reuse, strconv.Itoa(r)), append(rows, strconv.Itoa(row))
	}
	return "addr " + strings.Join(addrs, " ") + " sockets " + strings.Join(sockets, " ") +
		" reuseport " + strings.Join(reuse, " ") + " shards " + strings.Join(rows, " ")
}

// packetReusePort reads SO_REUSEPORT back from pc's socket.
func packetReusePort(t *testing.T, pc stdnet.PacketConn) int {
	t.Helper()
	raw, err := packetSocket(pc).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var value int
	var gerr error
	if cerr := raw.Control(func(fd uintptr) { value, gerr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort) }); cerr != nil || gerr != nil {
		t.Fatalf("getsockopt SO_REUSEPORT: %v %v", cerr, gerr)
	}
	return value
}

// TestShardsL3_oneSocketIsTheBase is #977's third criterion on the L3 run:
// over the veth pair of scripts/ci/net-veth.sh, L3's load, every datagram
// stamped, is sent to a group with Shards(1) and to the same group without
// the option, both in the receiver's namespace; each binds one socket without
// SO_REUSEPORT, reports one shard, and reads all 400 000 datagrams exactly
// once, byte for byte — Shards(1) as the base does. Its lane is
// net-kernel-gate.yml's bench job, which runs it after the L3 rounds, on the
// pair as `up` leaves it, and fails on a skip; anywhere else it skips, and
// TestShardsL3_wired runs the same receiver over loopback.
func TestShardsL3_oneSocketIsTheBase(t *testing.T) {
	peer := os.Getenv(l3PeerEnv)
	if peer == "" {
		t.Skip("the L3 run's check runs over the veth pair of scripts/ci/net-veth.sh: " + l3PeerEnv + " is unset (its lane is net-kernel-gate's bench job)")
	}
	runShardsL3(t, os.Getenv(l3ExecEnv), stdnet.JoinHostPort(peer, "0"), os.Getenv(l3LocalEnv), shardsL3Datagrams)
}

// TestShardsL3_wired runs TestShardsL3_oneSocketIsTheBase's receiver and
// sender over loopback, one send of each flow a group: the stamp, the checks
// and the layout, wherever Linux runs.
func TestShardsL3_wired(t *testing.T) {
	runShardsL3(t, "", "127.0.0.1:0", "", l3Flows*l3Segments)
}

// runShardsL3 starts the receiver bound to bind under prefix, sends datagrams
// stamped datagrams to each group in turn from local, and holds both groups
// to the criterion.
func runShardsL3(t *testing.T, prefix, bind, local string, datagrams int) {
	t.Helper()
	total := l3Sends(l3Flows, datagrams) * l3Flows * l3Segments
	child := startReceiver(t, prefix, "^TestShardsL3Child$", shardsL3Role+strconv.Itoa(total), bind)
	line, err := child.out.ReadString('\n')
	f := strings.Fields(line)
	if err != nil || len(f) != 12 || f[0] != "addr" || f[3] != "sockets" || f[6] != "reuseport" || f[9] != "shards" {
		t.Fatalf("receiver printed %q, %v", line, err)
	}
	child.addrs = f[1:3]
	t.Logf("receiver: %s", strings.TrimSpace(line))
	for i, g := range shardsL3Groups {
		if f[4+i] != "1" || f[7+i] != "0" || f[10+i] != "1" {
			t.Errorf("%s: %s sockets, %s with SO_REUSEPORT, State's shards %s; want one socket, none, 1", g.name, f[4+i], f[7+i], f[10+i])
		}
	}
	var sent [2]int
	for i := range shardsL3Groups {
		var flows []*stdnet.UDPConn
		for range l3Flows {
			c, err := dialGSO(local, child.addrs[i])
			if err != nil {
				t.Fatal(err)
			}
			flows = append(flows, c)
			t.Cleanup(func() { _ = c.Close() })
		}
		sent[i], err = paceL3(flows, datagrams, stampSends)
		if err != nil {
			t.Fatalf("%s: send: %v", shardsL3Groups[i].name, err)
		}
		shardsL3Settle(t, child, i, uint64(sent[i]))
	}
	got := shardsL3Sample(t, child)
	for i, g := range shardsL3Groups {
		exact, wrong, twice := got[3*i], got[3*i+1], got[3*i+2]
		t.Logf("%s: %d datagrams sent, %d read exact, %d differing, %d read twice", g.name, sent[i], exact, wrong, twice)
		if exact != uint64(sent[i]) || wrong != 0 || twice != 0 {
			t.Errorf("%s read %d of %d datagrams exact, %d differing, %d twice: want every one once, byte for byte", g.name, exact, sent[i], wrong, twice)
		}
	}
	if got[0] != got[3] || got[1] != got[4] || got[2] != got[5] {
		t.Errorf("Shards(1) read %v, the base %v: want the base's", got[3:6], got[0:3])
	}
}

// shardsL3Sample reads each group's exact, wrong and repeated counts; the
// receiver's CPU, which leads its answer, is dropped.
func shardsL3Sample(t *testing.T, c *l3Child) []uint64 {
	t.Helper()
	if _, err := c.in.Write([]byte("s\n")); err != nil {
		t.Fatal(err)
	}
	line, err := c.out.ReadString('\n')
	f := strings.Fields(line)
	if err != nil || len(f) != 1+3*len(shardsL3Groups) {
		t.Fatalf("receiver sample %q, %v", line, err)
	}
	vs := make([]uint64, 0, len(f)-1)
	for _, s := range f[1:] {
		v, perr := strconv.ParseUint(s, 10, 64)
		if perr != nil {
			t.Fatalf("receiver sample %q: %v", line, perr)
		}
		vs = append(vs, v)
	}
	return vs
}

// shardsL3Settle waits until group i read want datagrams, or its counts stop
// moving, within five seconds.
func shardsL3Settle(t *testing.T, c *l3Child, i int, want uint64) {
	t.Helper()
	last := shardsL3Sample(t, c)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		s := shardsL3Sample(t, c)
		read := s[3*i] + s[3*i+1] + s[3*i+2]
		if read >= want || read == last[3*i]+last[3*i+1]+last[3*i+2] {
			return
		}
		last = s
	}
}
