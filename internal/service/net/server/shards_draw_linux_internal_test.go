//go:build linux

// Package server — the port a sharded datagram group asked with port 0 is
// drawn (#1585). Linux's UDP draw, udp_lib_get_port, skips only the ports
// whose holders the new socket could not share: a socket that asks for the
// draw with SO_REUSEPORT set is given, among the others, a port a reuseport
// socket of the same user holds, and joins its group (udp_lib_lport_inuse,
// udp_reuseport_add_sock, net/ipv4/udp.c, v6.12). The group's first shard is
// therefore bound without SO_REUSEPORT, then given it, and the others join it.
package server

import (
	"context"
	"fmt"
	stdnet "net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// drawChildEnv carries the narrowed range a child of
	// TestShards_portZeroNarrowRange sets, "low high": it is the child's role,
	// and a child never starts another.
	drawChildEnv = "NET_SERVER_TEST_DRAW_RANGE"
	// drawChildLifetime bounds a child's life, whatever became of the test.
	drawChildLifetime = 20 * time.Second
	// drawPort is the first port of a child's narrowed range.
	drawPort = 40000
	// spreadSenders is how many source ports send to a reuseport pair: the
	// kernel's hash of the four-tuple sends all of them to one socket with a
	// probability of 2^-(spreadSenders-1).
	spreadSenders = 64
)

// foreignSocket binds a UDP socket on addr with SO_REUSEPORT set before its
// bind: the foreign socket of #1585's scenario, another library's or another
// server's of the same user.
func foreignSocket(ctx context.Context, addr string) (stdnet.PacketConn, error) {
	lc := stdnet.ListenConfig{Control: reusePortControl}
	//: the kernel's bind, port 0 drawn.
	return lc.ListenPacket(ctx, "udp4", addr)
}

// localPortRange is the host's ip_local_port_range, the ports a draw picks
// among.
func localPortRange(t *testing.T) (low, high int) {
	t.Helper()
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		t.Fatalf("ip_local_port_range: %v", err)
	}
	f := strings.Fields(string(raw))
	if len(f) != 2 {
		t.Fatalf("ip_local_port_range = %q", raw)
	}
	low, lerr := strconv.Atoi(f[0])
	high, herr := strconv.Atoi(f[1])
	if lerr != nil || herr != nil {
		t.Fatalf("ip_local_port_range = %q", raw)
	}
	return low, high
}

// startDrawGroup starts a server of one Shards(4) datagram group on addr and
// returns it with the address State reports, or the refusal of its Start.
func startDrawGroup(ctx context.Context, addr string) (*Server, string, error) {
	srv := New()
	srv.PacketGroup("p", Listen("udp4", addr), Shards(4)).
		HandleFunc(func(context.Context, corenet.Packet) error { return nil })
	if err := srv.Start(ctx); err != nil {
		return nil, "", err
	}
	return srv, srv.State().Listeners[0].Address, nil
}

// receivedBy sends one datagram to addr from each of spreadSenders source
// sockets and counts, per socket of pair, the datagrams it read.
func receivedBy(t *testing.T, addr string, pair ...stdnet.PacketConn) []int {
	t.Helper()
	for i := range spreadSenders {
		c, err := stdnet.Dial("udp4", addr)
		if err != nil {
			t.Fatalf("sender %d: %v", i, err)
		}
		_, werr := c.Write([]byte{byte(i)})
		_ = c.Close()
		if werr != nil {
			t.Fatalf("sender %d: %v", i, werr)
		}
	}
	got := make([]int, len(pair))
	buf := make([]byte, 16)
	for i, pc := range pair {
		for {
			_ = pc.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			//: read until the socket stays quiet.
			if _, _, err := pc.ReadFrom(buf); err != nil {
				break
			}
			got[i]++
		}
	}
	return got
}

// TestReuseport_enabledAfterBindAdmitsJoins pins the kernel's half of the
// remedy, on the running kernel: a UDP socket bound without SO_REUSEPORT and
// given it afterwards admits a later reuseport bind of the same user on its
// port, and both sockets then share the port's traffic — the group is created
// when the second joins (reuseport_add_sock allocates it for the first,
// net/core/sock_reuseport.c) —; never given the option, it refuses that bind.
func TestReuseport_enabledAfterBindAdmitsJoins(t *testing.T) {
	type tc struct {
		name string
		// enable sets SO_REUSEPORT on the first socket after its bind.
		enable bool
	}
	cases := []tc{
		{name: "enabled after bind", enable: true},
		{name: "never enabled", enable: false},
	}
	runCase := func(t *testing.T, c tc) {
		var lc stdnet.ListenConfig
		first, err := lc.ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("first bind: %v", err)
		}
		defer func() { _ = first.Close() }()
		//: the option, set on a bound socket.
		if c.enable {
			if err := reuseAfterBind(first); err != nil {
				t.Fatalf("SO_REUSEPORT after bind: %v", err)
			}
		}
		addr := first.LocalAddr().String()
		second, err := foreignSocket(t.Context(), addr)
		//: never enabled: the kernel refuses the join.
		if !c.enable {
			if err == nil {
				_ = second.Close()
				t.Fatalf("a reuseport bind on %s joined a socket that never set SO_REUSEPORT", addr)
			}
			return
		}
		if err != nil {
			t.Fatalf("a reuseport bind on %s, held by a socket given SO_REUSEPORT after its bind: %v", addr, err)
		}
		defer func() { _ = second.Close() }()
		got := receivedBy(t, addr, first, second)
		if got[0]+got[1] != spreadSenders || got[0] == 0 || got[1] == 0 {
			t.Fatalf("datagrams read %v by the first and the second socket, want %d spread over both: one reuseport group of two", got, spreadSenders)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestShards_portZeroDrawsAPortNobodyHolds is #1585 on the host: a foreign
// reuseport holder of this process keeps sockets on an eighth of the
// ephemeral range, and no Shards(4) group asked for port 0 — in any of the
// spellings Go binds as port 0: "0", "" and "00" — may be drawn one of their
// ports. With its first shard drawn with SO_REUSEPORT set, each start lands on
// a held port — and joins its holder's group — with a probability of about
// one in eight, so all 96 starts miss with a probability below 3e-6; drawn
// without the option, never.
func TestShards_portZeroDrawsAPortNobodyHolds(t *testing.T) {
	type tc struct {
		name string
		// addr is the group's address, its port one spelling of port 0.
		addr string
		// fraction is the share of the ephemeral range the holder keeps.
		fraction int
		// rounds is how many groups are started, one after the other.
		rounds int
	}
	cases := []tc{
		{name: "port 0, an eighth of the range held, 96 starts", addr: "127.0.0.1:0", fraction: 8, rounds: 96},
		{name: "an empty port, an eighth of the range held, 96 starts", addr: "127.0.0.1:", fraction: 8, rounds: 96},
		{name: "port 00, an eighth of the range held, 96 starts", addr: "127.0.0.1:00", fraction: 8, rounds: 96},
	}
	runCase := func(t *testing.T, c tc) {
		low, high := localPortRange(t)
		held := map[int]bool{}
		for i := range (high - low + 1) / c.fraction {
			pc, err := foreignSocket(t.Context(), "127.0.0.1:0")
			if err != nil {
				t.Fatalf("holder socket %d: %v", i, err)
			}
			t.Cleanup(func() { _ = pc.Close() })
			held[portOf(t, pc.LocalAddr().String())] = true
		}
		joined := 0
		for i := range c.rounds {
			srv, addr, err := startDrawGroup(t.Context(), c.addr)
			if err != nil {
				t.Fatalf("start %d: %v", i, err)
			}
			port := portOf(t, addr)
			swallowErr(srv.Close())
			//: a port the holder keeps: the group joined its reuseport group.
			if held[port] {
				joined++
			}
		}
		t.Logf("%s: %d of %d starts drawn a port among the %d the holder keeps in [%d, %d]", c.addr, joined, c.rounds, len(held), low, high)
		//: a draw that joined a foreign group.
		if joined != 0 {
			t.Fatalf("%d of %d Shards(4) groups asked for %q were drawn a port a foreign reuseport socket holds, and joined its group (#1585)", joined, c.rounds, c.addr)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestShards_portZeroNarrowRange is #1585 made certain: in a network namespace
// of its own, whose ephemeral range is narrowed to one or two ports, a foreign
// reuseport socket holds the first; a Shards(4) group asked for 0.0.0.0:0 is
// drawn the other, or refused AddressInUse when there is none — never the held
// one. It runs in a child of this binary in a user and network namespace of
// its own, and skips where the host refuses one (an AppArmor restriction of
// unprivileged user namespaces); TestShards_portZeroDrawsAPortNobodyHolds
// holds the same rule there.
func TestShards_portZeroNarrowRange(t *testing.T) {
	type tc struct {
		name string
		// high is the narrowed range's last port, drawPort its first.
		high int
		// want is the child's verdict.
		want string
	}
	cases := []tc{
		{name: "one port, held", high: drawPort, want: "refused AddressInUse"},
		{name: "two ports, the first held", high: drawPort + 1, want: "bound " + strconv.Itoa(drawPort+1)},
	}
	//: a child never starts another.
	if os.Getenv(drawChildEnv) != "" {
		t.Fatal("a draw child ran the parent test")
	}
	runCase := func(t *testing.T, c tc) {
		ctx, cancel := context.WithTimeout(t.Context(), drawChildLifetime+5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestShards_portZeroNarrowRangeChild$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
		//: the role is set once, below, never inherited.
		env := slices.DeleteFunc(unshardedEnv(os.Environ()), func(kv string) bool { return strings.HasPrefix(kv, drawChildEnv+"=") })
		cmd.Env = append(env, fmt.Sprintf("%s=%d %d", drawChildEnv, drawPort, c.high))
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
			UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
			GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
			Pdeathsig:   syscall.SIGKILL,
		}
		out, err := cmd.CombinedOutput()
		//: the host refuses the namespaces: the child never ran.
		if err != nil && len(out) == 0 {
			t.Skipf("no user and network namespace of our own: %v", err)
		}
		verdict := ""
		for line := range strings.Lines(string(out)) {
			//: the child's one verdict line.
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "draw: "); ok {
				verdict = v
			}
		}
		//: the namespace refused the narrowed range.
		if reason, ok := strings.CutPrefix(verdict, "skip "); ok {
			t.Skipf("the child's namespace: %s", reason)
		}
		if err != nil || verdict != c.want {
			t.Fatalf("child: %q (%v), want %q; output:\n%s", verdict, err, c.want, out)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestShards_portZeroNarrowRangeChild is the child of
// TestShards_portZeroNarrowRange: it narrows its namespace's range, holds the
// range's first port with a reuseport socket, starts a Shards(4) group on
// 0.0.0.0:0 and prints its verdict. It skips unless started as that child.
func TestShards_portZeroNarrowRangeChild(t *testing.T) {
	spec := os.Getenv(drawChildEnv)
	//: run by the parent alone.
	if spec == "" {
		t.Skip("run by TestShards_portZeroNarrowRange")
	}
	kill := time.AfterFunc(drawChildLifetime, func() { os.Exit(3) })
	defer kill.Stop()
	//: the namespace's own sysctl, refused where the user namespace is
	//: stripped of its capabilities.
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_local_port_range", []byte(spec), 0o644); err != nil {
		fmt.Printf("draw: skip ip_local_port_range: %v\n", err)
		return
	}
	held, err := foreignSocket(t.Context(), "0.0.0.0:"+strconv.Itoa(drawPort))
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	defer func() { _ = held.Close() }()
	srv, addr, err := startDrawGroup(t.Context(), "0.0.0.0:0")
	//: no port left that nobody holds.
	if err != nil {
		//: the refusal a held address gets.
		if errs.HasCode(err, corenet.CodeAddressInUse) {
			fmt.Println("draw: refused AddressInUse")
			return
		}
		fmt.Printf("draw: refused %v\n", err)
		return
	}
	defer swallowErr(srv.Close())
	fmt.Printf("draw: bound %d\n", portOf(t, addr))
}
