//go:build linux && (amd64 || arm64)

package cgroup_test

import (
	"bufio"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
	svcbpf "github.com/kitsunium/sdk/internal/service/proc/bpf"
)

// The live suite loads real programs, attaches them to a group it creates,
// spawns a child of its own binary into that group and watches the kernel
// refuse what the policy refuses. It needs CAP_BPF and CAP_NET_ADMIN (or
// root) and a cgroup v2 directory it may create a group under:
// KITSUNIUM_CGROUP_TEST_ROOT, else its own group. Without either it skips,
// after checking the refusal is LOAD_REFUSED. It never attaches anything
// but to the group it created, which it removes: never to a hierarchy's
// root, never to its own group. CI's linux-init lane runs it as root in a
// delegated unit (e2e-cross.yml).

// helperEnv tells a child of the test binary which operation to try.
const helperEnv = "KITSUNIUM_BPF_HELPER"

// A helper's exit codes.
const (
	helperDone    = 0
	helperDenied  = 3
	helperFailed  = 4
	helperUsage   = 5
	udpPayload    = 100
	ipv4UDPHeader = 28
)

// TestHelperProcess is the child the live suite spawns into its group: it
// tries one operation and exits helperDone, helperDenied on EPERM, or
// helperFailed. Run without its environment it does nothing.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnv)
	//: not a child of the live suite.
	if mode == "" {
		t.Skip("run by the live suite only, as a child in its group")
	}
	os.Exit(runHelper(mode))
}

// runHelper performs mode — open:<path>, udp:<host:port>:<count>,
// bind:<network>:<host:port> — and returns the exit code.
func runHelper(mode string) int {
	kind, arg, _ := strings.Cut(mode, ":")
	var err error
	//: by kind.
	switch kind {
	case "open":
		var f *os.File
		//: a device open the cgroup_device program judges.
		if f, err = os.OpenFile(arg, os.O_RDWR, 0); err == nil {
			_ = f.Close()
		}
	case "udp":
		err = sendUDP(arg)
	case "bind":
		err = bindOnce(arg)
	default:
		return helperUsage
	}
	//: what happened.
	switch {
	case err == nil:
		return helperDone
	case errors.Is(err, syscall.EPERM):
		return helperDenied
	default:
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		return helperFailed
	}
}

// sendUDP sends count datagrams of udpPayload bytes to host:port.
func sendUDP(arg string) error {
	i := strings.LastIndexByte(arg, ':')
	addr, countText := arg[:i], arg[i+1:]
	count, err := strconv.Atoi(countText)
	//: a malformed mode.
	if err != nil {
		return err
	}
	conn, err := net.Dial("udp", addr)
	//: no socket.
	if err != nil {
		return err
	}
	defer conn.Close()
	payload := make([]byte, udpPayload)
	//: each datagram; a drop on egress is EPERM to the sender.
	for range count {
		//: the first failure.
		if _, err := conn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// bindOnce binds network:host:port and lets it go.
func bindOnce(arg string) error {
	network, addr, _ := strings.Cut(arg, ":")
	//: UDP binds without listening.
	if strings.HasPrefix(network, "udp") {
		c, err := net.ListenPacket(network, addr)
		//: bound.
		if err == nil {
			_ = c.Close()
		}
		return err
	}
	l, err := net.Listen(network, addr)
	//: bound.
	if err == nil {
		_ = l.Close()
	}
	return err
}

// liveGroup returns a group created for the test under the delegated root,
// removed when the test ends, or skips when the host cannot load programs
// or has no root to create a group under.
func liveGroup(t *testing.T) string {
	t.Helper()
	//: the privilege first: without it, the refusal is checked and the suite skips.
	if err := svcbpf.Probe(); err != nil {
		//: a typed refusal, nothing else.
		if !errs.HasCode(err, corebpf.CodeLoadRefused) {
			t.Fatalf("Probe = %v, want nil or LOAD_REFUSED", err)
		}
		t.Skipf("this process may not load cgroup programs: %v", err)
	}
	root := os.Getenv("KITSUNIUM_CGROUP_TEST_ROOT")
	//: no lane root: the process's own group, as a parent only.
	if root == "" {
		root = ownGroup(t)
	}
	dir, err := os.MkdirTemp(root, "kitsunium-bpf-")
	//: no group can be created there.
	if err != nil {
		t.Skipf("no cgroup v2 directory to create a group under (%s): %v", root, err)
	}
	t.Cleanup(func() { removeGroup(t, dir) })
	return dir
}

// ownGroup is this process's cgroup v2 directory.
func ownGroup(t *testing.T) string {
	t.Helper()
	f, err := os.Open("/proc/self/cgroup")
	//: no procfs.
	if err != nil {
		t.Skipf("no /proc/self/cgroup: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	//: the unified hierarchy's line, "0::/path".
	for sc.Scan() {
		//: the v2 line.
		if rest, ok := strings.CutPrefix(sc.Text(), "0::"); ok {
			return filepath.Join("/sys/fs/cgroup", rest)
		}
	}
	t.Skip("this process is in no cgroup v2 group")
	return ""
}

// removeGroup removes the test's group once its children are gone.
func removeGroup(t *testing.T, dir string) {
	t.Helper()
	//: a child's exit can lag its wait by a moment.
	for range 100 {
		err := os.Remove(dir)
		//: removed, or never there.
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("the test group %s could not be removed", dir)
}

// runIn spawns the test binary into group as a helper for mode and returns
// its exit code.
func runIn(t *testing.T, group, mode string) int {
	t.Helper()
	dir, err := os.Open(group)
	//: the group's directory, for CLONE_INTO_CGROUP.
	if err != nil {
		t.Fatalf("open %s: %v", group, err)
	}
	defer dir.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperEnv+"="+mode)
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(dir.Fd())}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	//: the child ran: its code.
	if err == nil || errors.As(err, &exit) {
		code := cmd.ProcessState.ExitCode()
		//: a failure that is not the policy's is the test's to see.
		if code == helperFailed || code == helperUsage {
			t.Fatalf("helper %q failed: %s", mode, out)
		}
		return code
	}
	t.Fatalf("spawn of %q into %s: %v", mode, group, err)
	return 0
}

// deny13 is a device policy denying /dev/null (char 1:3) and allowing the
// rest.
var deny13 = corebpf.DevicePolicyValue{Default: corebpf.Allow, Rules: []corebpf.DeviceRuleValue{
	{Type: corebpf.DeviceChar, Major: 1, Minor: 3, Access: corebpf.AccessRead | corebpf.AccessWrite, Verdict: corebpf.Deny},
}}

// TestLiveDeviceProgramRefusesTheDevice: /dev/null opens in the group, then
// is refused once a policy denying char 1:3 is attached, then opens again
// once the program is closed.
func TestLiveDeviceProgramRefusesTheDevice(t *testing.T) {
	g := liveGroup(t)
	//: before: allowed.
	if code := runIn(t, g, "open:/dev/null"); code != helperDone {
		t.Fatalf("before the program: exit %d", code)
	}
	p, err := svcbpf.AttachDevice(g, deny13)
	//: attached.
	if err != nil {
		t.Fatalf("AttachDevice: %v", err)
	}
	//: during: EPERM.
	if code := runIn(t, g, "open:/dev/null"); code != helperDenied {
		_ = p.Close()
		t.Fatalf("with the program: exit %d, want %d (EPERM)", code, helperDenied)
	}
	//: detached.
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	//: after: allowed again.
	if code := runIn(t, g, "open:/dev/null"); code != helperDone {
		t.Fatalf("after Close: exit %d", code)
	}
}

// TestLiveAllowMultiRunsEveryProgram attaches two device programs to one
// group: one allowing everything, one denying /dev/null. Both are attached
// at once (BPF_F_ALLOW_MULTI) and both run: the deny wins; closing it
// leaves the other attached, and the device opens again.
func TestLiveAllowMultiRunsEveryProgram(t *testing.T) {
	g := liveGroup(t)
	a, err := svcbpf.AttachDevice(g, corebpf.DevicePolicyValue{Default: corebpf.Allow})
	//: the first program.
	if err != nil {
		t.Fatalf("AttachDevice(allow): %v", err)
	}
	defer a.Close()
	b, err := svcbpf.AttachDevice(g, deny13)
	//: the second, beside it: a single-program attachment would refuse or replace.
	if err != nil {
		t.Fatalf("AttachDevice(deny) beside another program: %v", err)
	}
	//: both run: the deny decides.
	if code := runIn(t, g, "open:/dev/null"); code != helperDenied {
		_ = b.Close()
		t.Fatalf("two programs: exit %d, want %d (EPERM)", code, helperDenied)
	}
	//: the deny leaves.
	if err := b.Close(); err != nil {
		t.Fatalf("Close(deny): %v", err)
	}
	//: the allow alone.
	if code := runIn(t, g, "open:/dev/null"); code != helperDone {
		t.Fatalf("the allow alone: exit %d", code)
	}
}

// TestLiveSKBProgramCountsAndDrops attaches an IP policy denying 127.0.0.2:
// datagrams to a listener on 127.0.0.1 pass and are counted on egress with
// their IP and UDP headers; one to 127.0.0.2 is refused with EPERM and
// counted as dropped.
func TestLiveSKBProgramCountsAndDrops(t *testing.T) {
	g := liveGroup(t)
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	//: a listener outside the group.
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	p, err := svcbpf.AttachSKB(g, corebpf.SKBPolicyValue{Default: corebpf.Allow, Rules: []corebpf.PrefixRuleValue{
		{Prefix: netip.MustParsePrefix("127.0.0.2/32"), Verdict: corebpf.Deny},
	}})
	//: attached on both hooks.
	if err != nil {
		t.Fatalf("AttachSKB: %v", err)
	}
	defer p.Close()
	const sent = 5
	//: allowed datagrams.
	if code := runIn(t, g, "udp:"+ln.LocalAddr().String()+":"+strconv.Itoa(sent)); code != helperDone {
		t.Fatalf("send to 127.0.0.1: exit %d", code)
	}
	c, err := p.Counters()
	//: counted, none dropped.
	if err != nil || c.Egress.Packets < sent || c.Egress.Bytes < sent*(udpPayload+ipv4UDPHeader) || c.Egress.DroppedPackets != 0 {
		t.Fatalf("after 5 datagrams: %+v, %v", c, err)
	}
	_, port, _ := net.SplitHostPort(ln.LocalAddr().String())
	//: a denied datagram.
	if code := runIn(t, g, "udp:127.0.0.2:"+port+":1"); code != helperDenied {
		t.Fatalf("send to 127.0.0.2: exit %d, want %d (EPERM)", code, helperDenied)
	}
	c, err = p.Counters()
	//: counted as dropped.
	if err != nil || c.Egress.DroppedPackets < 1 || c.Egress.DroppedBytes < udpPayload {
		t.Fatalf("after the denied datagram: %+v, %v", c, err)
	}
	//: closed: detached, and its counters gone with it.
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	//: PROGRAM_FAILED after Close.
	if _, err := p.Counters(); !errs.HasCode(err, corebpf.CodeProgramFailed) {
		t.Fatalf("Counters after Close = %v, want PROGRAM_FAILED", err)
	}
}

// TestLiveBindProgramRefusesThePort denies TCP binds of one free port:
// binding it in the group is EPERM, binding a kernel-chosen port or the
// same port over UDP is not, and the port binds again once the program is
// closed.
func TestLiveBindProgramRefusesThePort(t *testing.T) {
	g := liveGroup(t)
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	//: a free port.
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	_ = l.Close()
	n, _ := strconv.Atoi(port)
	p, err := svcbpf.AttachBind(g, corebpf.BindPolicyValue{Default: corebpf.Allow, Rules: []corebpf.BindRuleValue{
		{Family: corebpf.FamilyAny, Transport: corebpf.TransportTCP, Ports: corebpf.PortRangeValue{First: uint16(n), Last: uint16(n)}, Verdict: corebpf.Deny},
	}})
	//: attached on both bind hooks.
	if err != nil {
		t.Fatalf("AttachBind: %v", err)
	}
	cases := []struct {
		mode string
		want int
	}{
		{"bind:tcp4:127.0.0.1:" + port, helperDenied},
		{"bind:tcp4:127.0.0.1:0", helperDone},
		{"bind:udp4:127.0.0.1:" + port, helperDone},
	}
	//: each bind.
	for _, c := range cases {
		//: the policy's answer.
		if code := runIn(t, g, c.mode); code != c.want {
			_ = p.Close()
			t.Fatalf("%s: exit %d, want %d", c.mode, code, c.want)
		}
	}
	//: detached.
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	//: the port binds again.
	if code := runIn(t, g, "bind:tcp4:127.0.0.1:"+port); code != helperDone {
		t.Fatalf("after Close: exit %d", code)
	}
}

// TestLiveTheHierarchyRootIsRefused asks for an attachment to the cgroup v2
// mount's root: ATTACH_FAILED, refused before any program is loaded. The
// policy allows everything, so even a defect here would bind nothing.
func TestLiveTheHierarchyRootIsRefused(t *testing.T) {
	liveGroup(t)
	p, err := svcbpf.AttachDevice("/sys/fs/cgroup", corebpf.DevicePolicyValue{Default: corebpf.Allow})
	//: attached to the root: a defect, undone at once.
	if err == nil {
		_ = p.Close()
		t.Fatal("attached to the hierarchy's root")
	}
	//: ATTACH_FAILED.
	if !errs.HasCode(err, corebpf.CodeAttachFailed) {
		t.Fatalf("AttachDevice(root) = %v, want ATTACH_FAILED", err)
	}
}
