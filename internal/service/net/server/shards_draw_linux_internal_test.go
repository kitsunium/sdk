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
	"errors"
	"fmt"
	stdnet "net"
	"os"
	"os/exec"
	"runtime"
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
	// drawChildEnv carries the case a child of TestShards_portZeroNarrowRange
	// runs, "1" or "2" (drawRanges): it is the child's role, and a child never
	// starts another. The range itself is never read from the environment.
	drawChildEnv = "NET_SERVER_TEST_DRAW_RANGE"
	// drawNetnsEnv carries the parent's network namespace, "dev:ino" of
	// /proc/thread-self/ns/net: a child in that namespace refuses to write.
	drawNetnsEnv = "NET_SERVER_TEST_DRAW_NETNS"
	// netnsPath is the calling thread's network namespace.
	netnsPath = "/proc/thread-self/ns/net"
	// usernsPath is the calling thread's user namespace.
	usernsPath = "/proc/thread-self/ns/user"
	// nsGetUserns is NS_GET_USERNS, _IO(0xb7, 0x1) (include/uapi/linux/nsfs.h):
	// on a namespace's descriptor, a descriptor of the user namespace that owns
	// it, EPERM when that owner is outside the caller's user namespace.
	nsGetUserns = 0xb701
	// initialUIDMap is the initial user namespace's uid_map, the full
	// identity: a child that reads it is on the host.
	initialUIDMap = "0 0 4294967295"
	// drawChildLifetime bounds a child's life, whatever became of the test.
	drawChildLifetime = 20 * time.Second
	// drawPort is the first port of a child's narrowed range.
	drawPort = 40000
	// spreadSenders is how many source ports send to a reuseport pair: the
	// kernel's hash of the four-tuple sends all of them to one socket with a
	// probability of 2^-(spreadSenders-1).
	spreadSenders = 64
)

// drawRanges are the narrowed ranges a child writes, by case: one port, held,
// and two ports, the first held. They are built from drawPort alone.
var drawRanges = map[string]string{
	"1": fmt.Sprintf("%d %d", drawPort, drawPort),
	"2": fmt.Sprintf("%d %d", drawPort, drawPort+1),
}

// netnsID is a namespace's identity, its nsfs device and inode: a network
// namespace's, or a user namespace's.
type netnsID struct{ dev, ino uint64 }

// String writes the identity as drawNetnsEnv carries it.
func (n netnsID) String() string { return fmt.Sprintf("%d:%d", n.dev, n.ino) }

// parseNetnsID reads an identity drawNetnsEnv carries.
func parseNetnsID(raw string) (netnsID, bool) {
	dev, ino, ok := strings.Cut(raw, ":")
	d, derr := strconv.ParseUint(dev, 10, 64)
	i, ierr := strconv.ParseUint(ino, 10, 64)
	//: both halves, both numbers.
	if !ok || derr != nil || ierr != nil {
		return netnsID{}, false
	}
	return netnsID{dev: d, ino: i}, true
}

// threadNetns stats the calling thread's network namespace; the caller holds
// its OS thread so the answer is the namespace its next calls run in.
func threadNetns() (netnsID, error) {
	//: the thread's own namespace, not the leader's.
	return statNamespace(netnsPath)
}

// threadUserns stats the calling thread's user namespace.
func threadUserns() (netnsID, error) {
	//: the thread's own, the one its writes are judged in.
	return statNamespace(usernsPath)
}

// statNamespace stats the namespace path names, following the magic link to
// its nsfs inode.
func statNamespace(path string) (netnsID, error) {
	var st syscall.Stat_t
	//: the namespace, read.
	if err := syscall.Stat(path, &st); err != nil {
		return netnsID{}, err
	}
	return netnsID{dev: uint64(st.Dev), ino: st.Ino}, nil
}

// threadNetnsOwner returns the user namespace that owns the calling thread's
// network namespace (NS_GET_USERNS): the child's own when both were created
// together by CLONE_NEWUSER|CLONE_NEWNET, the initial one for the host's —
// which a child in a user namespace of its own cannot name: EPERM.
func threadNetnsOwner() (netnsID, error) {
	f, err := os.Open(netnsPath)
	//: the thread's network namespace, opened.
	if err != nil {
		return netnsID{}, err
	}
	defer func() { _ = f.Close() }()
	owner, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), nsGetUserns, 0)
	//: an owner outside this user namespace, or no such ioctl.
	if errno != 0 {
		return netnsID{}, errno
	}
	defer func() { _ = syscall.Close(int(owner)) }()
	var st syscall.Stat_t
	//: the owner's identity.
	if err := syscall.Fstat(int(owner), &st); err != nil {
		return netnsID{}, err
	}
	return netnsID{dev: uint64(st.Dev), ino: st.Ino}, nil
}

// drawGuard is what a draw child knows before it writes its namespace's
// ip_local_port_range.
type drawGuard struct {
	// index is drawChildEnv's value.
	index string
	// parent is drawNetnsEnv's value.
	parent string
	// own is the child's own namespace, read on the thread that writes, and
	// ownErr the failure of that read.
	own    netnsID
	ownErr error
	// uidMap is /proc/self/uid_map, and uidMapErr the failure of its read.
	uidMap    string
	uidMapErr error
	// owner is the user namespace that owns own (NS_GET_USERNS), and
	// ownerErr the failure of that read; userns is the child's own user
	// namespace, and usernsErr the failure of its read.
	owner     netnsID
	ownerErr  error
	userns    netnsID
	usernsErr error
}

// drawChildDecision decides whether a draw child may write its namespace's
// range: it returns the range to write, or the reason it refuses. It refuses
// an unknown case, a parent namespace missing or unreadable, its own namespace
// unread or the parent's, the initial user namespace, and a network namespace
// its own user namespace does not own — the host's, seen from a user
// namespace that maps 0 to 0 without being the identity, with a parent named
// by hand (R5 of #1585's review).
func drawChildDecision(g drawGuard) (spec, refusal string) {
	spec, known := drawRanges[g.index]
	//: only a case the constants build.
	if !known {
		return "", fmt.Sprintf("unknown case %q", g.index)
	}
	parent, ok := parseNetnsID(g.parent)
	//: the parent's namespace, named.
	if !ok {
		return "", fmt.Sprintf("parent network namespace %q unreadable", g.parent)
	}
	//: the child's own namespace, read.
	if g.ownErr != nil {
		return "", fmt.Sprintf("own network namespace: %v", g.ownErr)
	}
	//: a namespace of its own, not the parent's — the host's when the
	//: variables were inherited by accident.
	if g.own == parent {
		return "", "in the parent's network namespace " + parent.String()
	}
	//: the user namespace's map, read.
	if g.uidMapErr != nil {
		return "", fmt.Sprintf("uid_map: %v", g.uidMapErr)
	}
	//: not the initial user namespace.
	if strings.Join(strings.Fields(g.uidMap), " ") == initialUIDMap {
		return "", "in the initial user namespace"
	}
	//: the owner of its network namespace, named — EPERM for an owner
	//: outside its user namespace.
	if g.ownerErr != nil {
		return "", fmt.Sprintf("network namespace's owner: %v", g.ownerErr)
	}
	//: its own user namespace, read.
	if g.usernsErr != nil {
		return "", fmt.Sprintf("own user namespace: %v", g.usernsErr)
	}
	//: a network namespace created with its user namespace, as the parent's
	//: clone creates it.
	if g.owner != g.userns {
		return "", "network namespace " + g.own.String() + " owned by user namespace " + g.owner.String() + ", not its own " + g.userns.String()
	}
	return spec, ""
}

// Test_drawChildDecision pins the draw child's guard without writing a
// sysctl: each refusal, and the one case admitted, a known case in a network
// namespace other than the parent's and a user namespace other than the
// initial one.
func Test_drawChildDecision(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		g    drawGuard
		// spec is the range written, refusal a part of the reason; one of them.
		spec, refusal string
	}
	parent := netnsID{dev: 4, ino: 4026531840}
	own := netnsID{dev: 4, ino: 4026532999}
	const childMap = "         0       1000          1\n"
	userns := netnsID{dev: 4, ino: 4026533000}
	admitted := drawGuard{index: "1", parent: parent.String(), own: own, uidMap: childMap, owner: userns, userns: userns}
	with := func(edit func(*drawGuard)) drawGuard {
		g := admitted
		edit(&g)
		return g
	}
	tests := []tc{
		{name: "case 1 admitted", g: admitted, spec: "40000 40000"},
		{name: "case 2 admitted", g: with(func(g *drawGuard) { g.index = "2" }), spec: "40000 40001"},
		{name: "an unknown case", g: with(func(g *drawGuard) { g.index = "3" }), refusal: "unknown case"},
		{name: "a range in place of a case", g: with(func(g *drawGuard) { g.index = "1 65535" }), refusal: "unknown case"},
		{name: "no parent namespace", g: with(func(g *drawGuard) { g.parent = "" }), refusal: "unreadable"},
		{name: "a parent namespace without its device", g: with(func(g *drawGuard) { g.parent = "4026531840" }), refusal: "unreadable"},
		{name: "a parent namespace not a number", g: with(func(g *drawGuard) { g.parent = "4:net" }), refusal: "unreadable"},
		{name: "its own namespace unread", g: with(func(g *drawGuard) { g.ownErr = syscall.ENOENT }), refusal: "own network namespace"},
		{name: "the parent's namespace", g: with(func(g *drawGuard) { g.own = parent }), refusal: "in the parent's network namespace"},
		{name: "uid_map unread", g: with(func(g *drawGuard) { g.uidMapErr = syscall.EACCES }), refusal: "uid_map"},
		{name: "the initial user namespace", g: with(func(g *drawGuard) { g.uidMap = "         0          0 4294967295\n" }), refusal: "initial user namespace"},
		{name: "the host's network namespace under unshare -U -r", g: with(func(g *drawGuard) {
			g.uidMap, g.owner, g.ownerErr = "         0          0          1\n", netnsID{}, syscall.EPERM
		}), refusal: "network namespace's owner"},
		{name: "a network namespace another user namespace owns", g: with(func(g *drawGuard) {
			g.owner = netnsID{dev: 4, ino: 4026531837}
		}), refusal: "not its own"},
		{name: "its own user namespace unread", g: with(func(g *drawGuard) { g.usernsErr = syscall.ENOENT }), refusal: "own user namespace"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		spec, refusal := drawChildDecision(c.g)
		//: an admitted case writes its constant range, and nothing is refused.
		if c.refusal == "" {
			//: the range of the case, and no reason.
			if spec != c.spec || refusal != "" {
				t.Fatalf("decision = %q, %q; want %q admitted", spec, refusal, c.spec)
			}
			return
		}
		//: a refusal writes nothing, and says why.
		if spec != "" || !strings.Contains(refusal, c.refusal) {
			t.Fatalf("decision = %q, %q; want no range and a refusal naming %q", spec, refusal, c.refusal)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// Test_namespaceReaders pins the guard's readers on this process, reading
// only — no namespace is created, nothing is written: each names an nsfs inode
// on the device of this thread's network namespace, and the owner's read is
// NS_GET_USERNS — EPERM where this process's user namespace does not reach the
// owner, never ENOTTY, which a wrong request number would give.
func Test_namespaceReaders(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		read func() (netnsID, error)
		// mayRefuse is a read EPERM may answer.
		mayRefuse bool
	}
	tests := []tc{
		{name: "the network namespace", read: threadNetns},
		{name: "the user namespace", read: threadUserns},
		{name: "the network namespace's owner", read: threadNetnsOwner, mayRefuse: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		netns, nerr := threadNetns()
		//: no /proc namespaces to read here.
		if nerr != nil {
			t.Skipf("%s: %v", netnsPath, nerr)
		}
		id, err := c.read()
		//: an owner outside this user namespace, refused as the kernel says.
		if c.mayRefuse && errors.Is(err, syscall.EPERM) {
			t.Skipf("%s: %v", c.name, err)
		}
		//: an nsfs inode, on the namespaces' device.
		if err != nil || id.dev != netns.dev || id.ino == 0 {
			t.Fatalf("%s = %v, %v; want an inode on device %d", c.name, id, err, netns.dev)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

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
// one in eight, so all 128 starts miss with a probability of about 1e-7;
// drawn without the option, never. The holders draw with SO_REUSEPORT and
// join each other, so N of them cover a share 1-(1-1/M)^N ≈ 1-e^(-N/M) of the
// M ports, 11.75% for N = M/8; a start misses them with a probability of
// about e^(-1/8), and 128 starts with e^(-16) ≈ 1.1e-7. A host whose
// descriptor limit cannot hold the holders skips.
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
		{name: "port 0, an eighth of the range held, 128 starts", addr: "127.0.0.1:0", fraction: 8, rounds: 128},
		{name: "an empty port, an eighth of the range held, 128 starts", addr: "127.0.0.1:", fraction: 8, rounds: 128},
		{name: "port 00, an eighth of the range held, 128 starts", addr: "127.0.0.1:00", fraction: 8, rounds: 128},
	}
	runCase := func(t *testing.T, c tc) {
		low, high := localPortRange(t)
		held := map[int]bool{}
		for i := range (high - low + 1) / c.fraction {
			pc, err := foreignSocket(t.Context(), "127.0.0.1:0")
			//: the descriptor limit, not the draw: nothing to judge.
			if errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) {
				t.Skipf("holder socket %d of %d: %v: the descriptor limit cannot hold the holders", i, (high-low+1)/c.fraction, err)
			}
			//: any other refusal of a holder.
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
		// index is the case drawRanges builds the child's range from.
		index string
		// want is the child's verdict.
		want string
	}
	cases := []tc{
		{name: "one port, held", index: "1", want: "refused AddressInUse"},
		{name: "two ports, the first held", index: "2", want: "bound " + strconv.Itoa(drawPort+1)},
	}
	//: a child never starts another.
	if os.Getenv(drawChildEnv) != "" || os.Getenv(drawNetnsEnv) != "" {
		t.Fatal("a draw child ran the parent test")
	}
	runtime.LockOSThread()
	parentNetns, nerr := threadNetns()
	runtime.UnlockOSThread()
	//: without its own namespace named, no child could judge its own.
	if nerr != nil {
		t.Skipf("this test's network namespace: %v", nerr)
	}
	runCase := func(t *testing.T, c tc) {
		ctx, cancel := context.WithTimeout(t.Context(), drawChildLifetime+5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestShards_portZeroNarrowRangeChild$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
		//: the role and the namespace are set once, below, never inherited.
		env := slices.DeleteFunc(unshardedEnv(os.Environ()), func(kv string) bool {
			return strings.HasPrefix(kv, drawChildEnv+"=") || strings.HasPrefix(kv, drawNetnsEnv+"=")
		})
		cmd.Env = append(env, drawChildEnv+"="+c.index, drawNetnsEnv+"="+parentNetns.String())
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
// 0.0.0.0:0 and prints its verdict. It skips unless started as that child,
// and writes nothing — printing "draw: skip <reason>" — unless
// drawChildDecision admits it: a known case, and a network namespace and a
// user namespace that are not the parent's or the host's, the first owned by
// the second. A refusal is a skip, the line printed first for the parent. The
// thread that reads its namespace is the one that opens and writes the sysctl,
// held until the test ends.
func TestShards_portZeroNarrowRangeChild(t *testing.T) {
	index := os.Getenv(drawChildEnv)
	//: run by the parent alone.
	if index == "" {
		t.Skip("run by TestShards_portZeroNarrowRange")
	}
	kill := time.AfterFunc(drawChildLifetime, func() { os.Exit(3) })
	defer kill.Stop()
	// Never unlocked: the namespace read is the one the write runs in, and the
	// thread ends with the test's goroutine.
	runtime.LockOSThread()
	own, ownErr := threadNetns()
	uidMap, uidMapErr := os.ReadFile("/proc/self/uid_map")
	owner, ownerErr := threadNetnsOwner()
	userns, usernsErr := threadUserns()
	spec, refusal := drawChildDecision(drawGuard{
		index: index, parent: os.Getenv(drawNetnsEnv),
		own: own, ownErr: ownErr,
		uidMap: string(uidMap), uidMapErr: uidMapErr,
		owner: owner, ownerErr: ownerErr,
		userns: userns, usernsErr: usernsErr,
	})
	//: not a namespace of its own, or not a case of the constants: no write.
	if refusal != "" {
		fmt.Printf("draw: skip %s\n", refusal)
		t.Skip(refusal)
	}
	//: the namespace's own sysctl, refused where the user namespace is
	//: stripped of its capabilities.
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_local_port_range", []byte(spec), 0o644); err != nil {
		fmt.Printf("draw: skip ip_local_port_range: %v\n", err)
		t.Skipf("ip_local_port_range: %v", err)
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
