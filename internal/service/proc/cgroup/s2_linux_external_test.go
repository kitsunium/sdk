//go:build linux

// The junction of #728 (S2, Spec.Sandbox) with the placement of a spawn in a
// group (#929, CLONE_INTO_CGROUP) and with S0g's kill by passes (#734), on a
// real cgroup v2 hierarchy: a sandboxed child lands in the group it is
// spawned into, confined, and the group a kill by passes emptied takes the
// next spawn. Each test works in a group it makes under a delegated root
// (liveTree) and skips without one; it skips too, after the refusal, on a
// host that cannot sandbox. Only children of the test are signalled; the
// child writes nowhere but a directory under t.TempDir().
package cgroup_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
	procexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// sandboxWrites are the Landlock rights the junction's sandbox handles: a
// file is written, made, truncated or removed only beneath its rule's path.
const sandboxWrites = coreproc.LandlockWriteFile | coreproc.LandlockMakeReg | coreproc.LandlockTruncate | coreproc.LandlockRemoveFile

// junctionSandbox is the sandbox of the junction: its own user, PID and
// cgroup namespaces — root inside mapped to the test's user —, no new
// privileges, Landlock's write rights granted under allowed alone, and a
// filter refusing mkdir with EPERM.
func junctionSandbox(allowed string) *coreproc.SandboxValue {
	return &coreproc.SandboxValue{
		Namespaces:      coreproc.NamespaceUser | coreproc.NamespacePID | coreproc.NamespaceCgroup,
		NoNewPrivileges: true,
		Landlock: &coreproc.LandlockValue{
			Handled:    sandboxWrites,
			Rules:      []coreproc.LandlockRuleValue{{Path: allowed, Access: sandboxWrites}},
			BestEffort: coreproc.LandlockTruncate,
		},
		Seccomp: &coreproc.SeccompValue{Default: coreproc.SeccompAllow, Syscalls: []string{"mkdir", "mkdirat"}, Action: coreproc.SeccompErrno},
	}
}

// requireSandbox skips the test on a host that cannot apply junctionSandbox
// at all — no group involved —, after checking the refusal is typed: a
// GOARCH with no system call table, a kernel without Landlock or seccomp, a
// user namespace refused or stripped of its capabilities.
func requireSandbox(t *testing.T, shell string) {
	t.Helper()
	if _, err := procexec.LandlockABI(); err != nil {
		t.Skipf("no Landlock here: %v", err)
	}
	sb := junctionSandbox(t.TempDir())
	p, err := procexec.Start(context.Background(), coreproc.Spec{Path: shell, Args: []string{shell, "-c", ":"}, Sandbox: sb, Stdio: coreproc.StdioNull})
	if err == nil {
		_, err = p.Wait()
	}
	if err == nil {
		return
	}
	for _, typed := range []error{coreproc.UnsupportedPlatform, coreproc.SandboxUnavailable, coreproc.SandboxFailed, coreproc.SpawnFailed} {
		if errors.Is(err, typed) {
			t.Skipf("this host cannot sandbox a child: %v", err)
		}
	}
	t.Fatalf("the sandbox probe failed untyped: %v", err)
}

// junctionFile returns dir/name's content without its trailing newline.
func junctionFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("the child left no %s: %v", name, err)
	}
	return strings.TrimRight(string(b), "\n")
}

// statusLine returns field's value in a /proc/<pid>/status dump.
func statusLine(status, field string) string {
	for line := range strings.SplitSeq(status, "\n") {
		if v, ok := strings.CutPrefix(line, field+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// awaitFile waits up to ten seconds for path to exist, failing after.
func awaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear within ten seconds", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// junctionScript is what the sandboxed child runs, $0 the directory its rule
// grants and $1 a witness beside it: it records who it is, its namespaces'
// view of its group and its own status — read by the shell itself, not by a
// child whose /proc/self would be its own —, tries a write into the witness
// (Landlock) and a mkdir (seccomp), says it is ready, then sleeps as the
// group's member.
const junctionScript = `id -u > "$0/uid"
cat /proc/self/uid_map > "$0/uid_map"
cat /proc/self/cgroup > "$0/cgroup"
while IFS= read -r line; do echo "$line"; done < /proc/self/status > "$0/status"
if echo escaped > "$1/f" 2>/dev/null; then echo escaped; else echo denied; fi > "$0/landlock"
if mkdir "$0/made" 2>/dev/null; then echo made; else echo refused; fi > "$0/seccomp"
: > "$0/ready"
exec sleep 30`

// TestLiveSandboxedSpawnLandsInTheGroup pins the junction of #728 with the
// placement by clone (#929): a child spawned under a sandbox — user, PID and
// cgroup namespaces, no_new_privs, Landlock, seccomp — into a group through
// Spec.CgroupPath is a member of that group from the supervisor's side
// (Contains, Members), and every restriction holds in it. Its cgroup
// namespace is rooted at that group: the clone placed it before the
// namespace was taken, and the trampoline's own write of its PID (1 in its
// PID namespace) into the group's cgroup.procs passed.
func TestLiveSandboxedSpawnLandsInTheGroup(t *testing.T) {
	tree := liveTree(t)
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh: %v", err)
	}
	requireSandbox(t, shell)
	g, err := cgroup.Create("unit", cgroup.WithRoot(tree))
	if err != nil {
		t.Fatalf("Create(unit) = %v", err)
	}
	path := g.(coreproc.GroupTree).Path()
	allowed, witness := t.TempDir(), t.TempDir()
	child, err := procexec.Start(t.Context(), coreproc.Spec{
		Path: shell, Args: []string{shell, "-c", junctionScript, allowed, witness},
		Sandbox: junctionSandbox(allowed), CgroupPath: path, Stdio: coreproc.StdioNull,
	})
	if err != nil {
		t.Fatalf("a sandboxed Start into %s = %v", path, err)
	}
	t.Cleanup(func() {
		_ = child.Signal(coreproc.Signal(syscall.SIGKILL))
		_, _ = child.Wait()
	})
	awaitFile(t, filepath.Join(allowed, "ready"))
	signaler := g.(coreproc.GroupSignaler)
	if in, err := signaler.Contains(child.PID()); err != nil || !in {
		t.Fatalf("Contains(%d) = %v, %v: the sandboxed child is not in the group it was spawned into", child.PID(), in, err)
	}
	if members, err := g.(coreproc.GroupReader).Members(); err != nil || len(members) != 1 || members[0] != child.PID() {
		t.Fatalf("Members = %v, %v; want the sandboxed child %d alone", members, err, child.PID())
	}
	status := junctionFile(t, allowed, "status")
	want := map[string]string{
		"uid":      "0",
		"uid_map":  "0 " + strconv.Itoa(os.Geteuid()) + " 1",
		"cgroup":   "0::/",
		"landlock": "denied",
		"seccomp":  "refused",
	}
	for name, w := range want {
		got := junctionFile(t, allowed, name)
		if name == "uid_map" {
			got = strings.Join(strings.Fields(got), " ")
		}
		if got != w {
			t.Errorf("%s in the sandboxed child = %q, want %q", name, got, w)
		}
	}
	for field, w := range map[string]string{"NoNewPrivs": "1", "Seccomp": "2"} {
		if got := statusLine(status, field); got != w {
			t.Errorf("%s in the sandboxed child = %q, want %q", field, got, w)
		}
	}
	if nspid := strings.Fields(statusLine(status, "NSpid")); len(nspid) != 2 || nspid[0] != strconv.Itoa(child.PID()) || nspid[1] != "1" {
		t.Errorf("NSpid in the sandboxed child = %v, want [%d 1]", nspid, child.PID())
	}
	if entries, _ := os.ReadDir(witness); len(entries) != 0 {
		t.Errorf("the witness holds %d entries: Landlock let the write through", len(entries))
	}
	if _, err := os.Stat(filepath.Join(allowed, "made")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the filtered mkdir made its directory: %v", err)
	}
}

// TestLiveSandboxedGroupReusedAfterSignalPasses pins the junction of #728
// with S0g's kill (#734): the group of a sandboxed child, emptied by SIGKILL
// through Signal's passes — never cgroup.kill, which on Linux 6.12 kills
// every child cloned into the group after it —, takes the next spawn, a
// sandboxed one and a plain one: each runs to its own exit, in the group.
func TestLiveSandboxedGroupReusedAfterSignalPasses(t *testing.T) {
	tree := liveTree(t)
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh: %v", err)
	}
	requireSandbox(t, shell)
	g, err := cgroup.Create("unit", cgroup.WithRoot(tree))
	if err != nil {
		t.Fatalf("Create(unit) = %v", err)
	}
	path := g.(coreproc.GroupTree).Path()
	membership, err := cgroup.Membership(path)
	if err != nil {
		t.Fatalf("Membership(%s) = %v", path, err)
	}
	allowed, witness := t.TempDir(), t.TempDir()
	first, err := procexec.Start(t.Context(), coreproc.Spec{
		Path: shell, Args: []string{shell, "-c", junctionScript, allowed, witness},
		Sandbox: junctionSandbox(allowed), CgroupPath: path, Stdio: coreproc.StdioNull,
	})
	if err != nil {
		t.Fatalf("a sandboxed Start into %s = %v", path, err)
	}
	t.Cleanup(func() {
		_ = first.Signal(coreproc.Signal(syscall.SIGKILL))
		_, _ = first.Wait()
	})
	awaitFile(t, filepath.Join(allowed, "ready"))
	if err := g.(coreproc.GroupSignaler).Signal(coreproc.Signal(syscall.SIGKILL)); err != nil {
		t.Fatalf("Signal(SIGKILL) = %v", err)
	}
	if exit, _ := first.Wait(); !exit.Signaled || exit.Signal != coreproc.Signal(syscall.SIGKILL) {
		t.Fatalf("the sandboxed child ended with %+v, want SIGKILL from the group's passes", exit)
	}
	if members, err := g.(coreproc.GroupReader).Members(); err != nil || len(members) != 0 {
		t.Fatalf("Members after the kill = %v, %v; want none", members, err)
	}
	for _, c := range []struct {
		name    string
		sandbox bool
		want    string
	}{
		{"sandboxed", true, "0::/"},
		{"plain", false, "0::" + membership},
	} {
		out := t.TempDir()
		spec := coreproc.Spec{Path: shell, Args: []string{shell, "-c", `cat /proc/self/cgroup > "$0/cgroup"`, out}, CgroupPath: path, Stdio: coreproc.StdioNull}
		if c.sandbox {
			spec.Sandbox = junctionSandbox(out)
		}
		next, err := procexec.Start(t.Context(), spec)
		if err != nil {
			t.Fatalf("%s Start into the killed group = %v", c.name, err)
		}
		exit, err := next.Wait()
		if err != nil || exit.Signaled || exit.Code != 0 {
			t.Fatalf("the %s child spawned into the killed group ended with %+v, %v; want its own exit 0", c.name, exit, err)
		}
		if got := junctionFile(t, out, "cgroup"); got != c.want {
			t.Errorf("the %s child read its group as %q, want %q", c.name, got, c.want)
		}
	}
}
