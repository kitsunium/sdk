//go:build linux && (amd64 || arm64)

package exec

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// capDACOverride is CAP_DAC_OVERRIDE's number.
const capDACOverride = 1

// withHost runs the test with h as the kernel the sandbox reads, restoring the
// real one after. The tests that call it are not parallel: the override is the
// package's.
func withHost(t *testing.T, edit func(h *sandboxHost)) {
	t.Helper()
	h := hostKernel
	edit(&h)
	sandboxHostOverride.Store(&h)
	t.Cleanup(func() { sandboxHostOverride.Store(nil) })
}

// sandboxed starts shellPath -c script with args under sb, waits for it, and
// returns its exit and the error Start or Wait gave.
func sandboxed(t *testing.T, sb *coreproc.SandboxValue, script string, args ...string) (coreproc.ExitValue, error) {
	t.Helper()
	spec := coreproc.Spec{Path: shellPath, Args: append([]string{shellPath, "-c", script}, args...), Sandbox: sb, Stdio: coreproc.StdioNull}
	return startWait(t, spec)
}

// startWait starts spec and waits for it.
func startWait(t *testing.T, spec coreproc.Spec) (coreproc.ExitValue, error) {
	t.Helper()
	p, err := Start(context.Background(), spec)
	if err != nil {
		return coreproc.ExitValue{}, err
	}
	return p.Wait()
}

// readFile returns path's content without its trailing newline, empty when absent.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return strings.TrimRight(string(b), "\n")
}

// userNS holds the answer, once per test binary, to whether this host gives an
// unprivileged user namespace the capabilities it needs to mount and cut its
// bounding set: some refuse the namespace (EPERM, ENOSPC), others — Ubuntu's
// AppArmor restrict_unprivileged_userns — grant it and strip its capabilities.
var userNS struct {
	once sync.Once
	err  error
}

// probeUserNamespace spawns /bin/true in a user and a mount namespace with a
// tmpfs on dir and its bounding set cut, and returns what Start or Wait said.
func probeUserNamespace(dir string) error {
	sb := &coreproc.SandboxValue{
		Namespaces:   coreproc.NamespaceUser | coreproc.NamespaceMount,
		Mounts:       []coreproc.MountValue{{Kind: coreproc.MountTmpfs, Target: dir}},
		Capabilities: &coreproc.CapabilitiesValue{Bounding: 1 << capDACOverride},
	}
	p, err := Start(context.Background(), coreproc.Spec{Path: "/bin/true", Sandbox: sb, Stdio: coreproc.StdioNull})
	if err != nil {
		return err
	}
	_, err = p.Wait()
	return err
}

// requireUserNamespace skips the test on a host where an unprivileged user
// namespace cannot mount nor drop a capability, saying why; the refusal is
// still the typed SpawnFailed or SandboxFailed.
func requireUserNamespace(t *testing.T) {
	t.Helper()
	userNS.once.Do(func() { userNS.err = probeUserNamespace(t.TempDir()) })
	err := userNS.err
	if err == nil {
		return
	}
	if !errors.Is(err, coreproc.SpawnFailed) && !errors.Is(err, coreproc.SandboxFailed) {
		t.Fatalf("the user namespace probe failed untyped: %v", err)
	}
	t.Skipf("this host gives an unprivileged user namespace no capability: %v", err)
}

// inUserNS is a sandbox in its own user namespace, root inside mapped to the
// test's user, with the extra namespaces given.
func inUserNS(extra coreproc.Namespace) *coreproc.SandboxValue {
	return &coreproc.SandboxValue{Namespaces: coreproc.NamespaceUser | extra}
}

// statusField returns the value of field in a /proc/<pid>/status dump.
func statusField(status, field string) string {
	for line := range strings.SplitSeq(status, "\n") {
		if v, ok := strings.CutPrefix(line, field+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// TestSandboxRefusedWithoutProcfs is #728's "sans /proc": a sandboxed spawn
// on a host where /proc/self/exe cannot be reached is refused before the fork
// with SandboxUnavailable naming procfs — no os.Executable fallback — and its
// program never runs.
func TestSandboxRefusedWithoutProcfs(t *testing.T) {
	withHost(t, func(h *sandboxHost) { h.procSelfExe = filepath.Join(t.TempDir(), "no-proc", "self", "exe") })
	marker := filepath.Join(t.TempDir(), "ran")
	_, err := sandboxed(t, &coreproc.SandboxValue{NoNewPrivileges: true}, `: > "$0"`, marker)
	if !errors.Is(err, coreproc.SandboxUnavailable) || fieldOf(err, "feature") != "procfs" {
		t.Fatalf("Start without procfs = %v, want SandboxUnavailable naming procfs", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("the refused program ran (marker: %v)", statErr)
	}
}

// Test_checkSandboxKernel pins the refusals of a kernel lacking a part of the
// sandbox, each naming the function: Landlock absent (ENOSYS typed), a
// Landlock right newer than the ABI, a namespace kind, mount_setattr, a
// capability past cap_last_cap — and that a right named best-effort is
// dropped, not refused.
func Test_checkSandboxKernel(t *testing.T) {
	ll := &coreproc.LandlockValue{
		Handled: coreproc.LandlockWriteFile | coreproc.LandlockRefer,
		Rules:   []coreproc.LandlockRuleValue{{Path: "/tmp", Access: coreproc.LandlockRefer}},
	}
	cases := []struct {
		name    string
		edit    func(h *sandboxHost)
		sb      *coreproc.SandboxValue
		feature string
	}{
		{
			"no landlock", func(h *sandboxHost) { h.landlockABI = func() (int, syscall.Errno) { return 0, syscall.ENOSYS } },
			&coreproc.SandboxValue{Landlock: ll}, "landlock",
		},
		{
			"a right newer than the ABI", func(h *sandboxHost) { h.landlockABI = func() (int, syscall.Errno) { return 1, 0 } },
			&coreproc.SandboxValue{Landlock: ll}, "landlock:refer",
		},
		{
			"no seccomp", func(h *sandboxHost) { h.seccomp = func() syscall.Errno { return syscall.ENOSYS } },
			&coreproc.SandboxValue{Seccomp: &coreproc.SeccompValue{Default: coreproc.SeccompAllow, Syscalls: []string{"uname"}}}, "seccomp",
		},
		{
			"no mount_setattr", func(h *sandboxHost) { h.mountSetattr = func() syscall.Errno { return syscall.ENOSYS } },
			&coreproc.SandboxValue{Namespaces: coreproc.NamespaceMount, Mounts: []coreproc.MountValue{{Target: "/usr"}}}, "mount_setattr",
		},
		{
			"no user namespace", func(h *sandboxHost) { h.nsDir = t.TempDir() },
			inUserNS(0), "namespace:user",
		},
		{"a capability past cap_last_cap", func(h *sandboxHost) {
			h.capLastCap = filepath.Join(t.TempDir(), "cap_last_cap")
			if err := os.WriteFile(h.capLastCap, []byte("40\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, &coreproc.SandboxValue{Capabilities: &coreproc.CapabilitiesValue{Bounding: 1 << 41, Ambient: 1 << 41}}, "capability"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withHost(t, c.edit)
			err := checkSandbox(coreproc.Spec{Path: shellPath, Sandbox: c.sb})
			if !errors.Is(err, coreproc.SandboxUnavailable) || fieldOf(err, "feature") != c.feature {
				t.Fatalf("checkSandbox = %v, want SandboxUnavailable naming %s", err, c.feature)
			}
		})
	}
	t.Run("a right named best-effort", func(t *testing.T) {
		withHost(t, func(h *sandboxHost) { h.landlockABI = func() (int, syscall.Errno) { return 1, 0 } })
		best := *ll
		best.BestEffort = coreproc.LandlockRefer
		handled, rules, err := landlockPlan(&best, currentHost())
		if err != nil || handled != uint64(coreproc.LandlockWriteFile) || len(rules) != 0 {
			t.Fatalf("landlockPlan = (%#x, %v, %v), want WRITE_FILE alone and the rule granting only REFER dropped", handled, rules, err)
		}
	})
}

// Test_checkSandboxShape pins the InvalidSpec refusals: what no kernel could
// apply is refused before anything is resolved.
func Test_checkSandboxShape(t *testing.T) {
	t.Parallel()
	mnt := coreproc.NamespaceUser | coreproc.NamespaceMount
	cases := map[string]coreproc.Spec{
		"inherited descriptors":     {Path: shellPath, FDs: coreproc.FDsInherited, Sandbox: &coreproc.SandboxValue{}},
		"an unknown namespace":      {Path: shellPath, Sandbox: &coreproc.SandboxValue{Namespaces: 1 << 9}},
		"maps without a user ns":    {Path: shellPath, Sandbox: &coreproc.SandboxValue{UIDMap: []coreproc.IDMapValue{{Size: 1}}}},
		"a map of nothing":          {Path: shellPath, Sandbox: &coreproc.SandboxValue{Namespaces: coreproc.NamespaceUser, UIDMap: []coreproc.IDMapValue{{}}}},
		"groups in a user ns":       {Path: shellPath, Groups: []string{"0"}, Sandbox: inUserNS(0)},
		"a root without mount ns":   {Path: shellPath, Sandbox: &coreproc.SandboxValue{Root: "/srv"}},
		"a relative root":           {Path: shellPath, Sandbox: &coreproc.SandboxValue{Namespaces: mnt, Root: "srv"}},
		"a bare name in a root":     {Path: "sh", Sandbox: &coreproc.SandboxValue{Namespaces: mnt, Root: "/srv"}},
		"a mount without mount ns":  {Path: shellPath, Sandbox: &coreproc.SandboxValue{Mounts: []coreproc.MountValue{{Target: "/usr"}}}},
		"a relative target":         {Path: shellPath, Sandbox: &coreproc.SandboxValue{Namespaces: mnt, Mounts: []coreproc.MountValue{{Target: "usr"}}}},
		"a bind without source":     {Path: shellPath, Sandbox: &coreproc.SandboxValue{Namespaces: mnt, Mounts: []coreproc.MountValue{{Kind: coreproc.MountBind, Target: "/usr"}}}},
		"a tmpfs with a source":     {Path: shellPath, Sandbox: &coreproc.SandboxValue{Namespaces: mnt, Mounts: []coreproc.MountValue{{Kind: coreproc.MountTmpfs, Source: "/x", Target: "/usr"}}}},
		"an unknown mount kind":     {Path: shellPath, Sandbox: &coreproc.SandboxValue{Namespaces: mnt, Mounts: []coreproc.MountValue{{Kind: 7, Target: "/usr"}}}},
		"ambient outside bounding":  {Path: shellPath, Sandbox: &coreproc.SandboxValue{Capabilities: &coreproc.CapabilitiesValue{Ambient: 1}}},
		"landlock handling nothing": {Path: shellPath, Sandbox: &coreproc.SandboxValue{Landlock: &coreproc.LandlockValue{}}},
		"a landlock rule beyond what is handled": {Path: shellPath, Sandbox: &coreproc.SandboxValue{Landlock: &coreproc.LandlockValue{
			Handled: coreproc.LandlockWriteFile, Rules: []coreproc.LandlockRuleValue{{Path: "/tmp", Access: coreproc.LandlockReadFile}},
		}}},
		"an inert seccomp filter": {Path: shellPath, Sandbox: &coreproc.SandboxValue{Seccomp: &coreproc.SeccompValue{Default: coreproc.SeccompAllow, Action: coreproc.SeccompKill}}},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := checkSandbox(spec); !errors.Is(err, coreproc.InvalidSpec) {
				t.Fatalf("checkSandbox = %v, want InvalidSpec", err)
			}
		})
	}
}

// TestLandlockABI pins that the effective ABI is returned: a version from 1 up
// on a kernel with Landlock, SandboxUnavailable naming landlock without it.
func TestLandlockABI(t *testing.T) {
	t.Parallel()
	abi, err := LandlockABI()
	if err != nil {
		if !errors.Is(err, coreproc.SandboxUnavailable) || fieldOf(err, "feature") != "landlock" {
			t.Fatalf("LandlockABI() = %v, want SandboxUnavailable naming landlock", err)
		}
		t.Skipf("this kernel has no Landlock: %v", err)
	}
	if abi < 1 {
		t.Fatalf("LandlockABI() = %d, want 1 or more", abi)
	}
	t.Logf("Landlock ABI %d", abi)
}

// TestSandboxNoNewPrivileges proves no_new_privs in the child: its own
// /proc/self/status says so.
func TestSandboxNoNewPrivileges(t *testing.T) {
	dir := t.TempDir()
	_, err := sandboxed(t, &coreproc.SandboxValue{NoNewPrivileges: true}, `cat /proc/self/status > "$0/status"`, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusField(readFile(t, filepath.Join(dir, "status")), "NoNewPrivs"); got != "1" {
		t.Fatalf("NoNewPrivs in the child = %q, want 1", got)
	}
}

// TestSandboxLandlock proves Landlock in the child: a file is created in the
// directory a rule grants, and the write into a witness directory beside it is
// refused — the witness stays empty.
func TestSandboxLandlock(t *testing.T) {
	abi, err := LandlockABI()
	if err != nil {
		t.Skipf("no Landlock here: %v", err)
	}
	allowed, witness := t.TempDir(), t.TempDir()
	rights := coreproc.LandlockWriteFile | coreproc.LandlockMakeReg | coreproc.LandlockTruncate | coreproc.LandlockRemoveFile
	sb := &coreproc.SandboxValue{Landlock: &coreproc.LandlockValue{
		Handled:    rights,
		Rules:      []coreproc.LandlockRuleValue{{Path: allowed, Access: rights}},
		BestEffort: coreproc.LandlockTruncate,
	}}
	script := `echo ok > "$0/f"; if echo escaped > "$1/f" 2>/dev/null; then echo escaped; else echo denied; fi > "$0/verdict"`
	if _, err := sandboxed(t, sb, script, allowed, witness); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(allowed, "f")); got != "ok" {
		t.Fatalf("the granted directory holds %q, want ok", got)
	}
	if got := readFile(t, filepath.Join(allowed, "verdict")); got != "denied" {
		t.Fatalf("the write outside the rule was %q (ABI %d), want denied", got, abi)
	}
	if entries, _ := os.ReadDir(witness); len(entries) != 0 {
		t.Fatalf("the witness directory holds %d entries, want none", len(entries))
	}
}

// seccompProbeEnv makes the test binary run as the probe child of
// TestSandboxSeccompErrno: it makes the filtered calls under the directory the
// variable names and reports their errno. The role half returns before
// anything is spawned, and the parent hands the probe an environment of its
// own making, so a probe never starts another.
const seccompProbeEnv = "SDK_EXEC_SECCOMP_PROBE"

// seccompProbeWait bounds the probe: two system calls, then its exit.
const seccompProbeWait = 30 * time.Second

// TestSandboxSeccompErrno proves the filter in the child: mkdir(2) and
// mkdirat(2), as this architecture numbers them, fail with the errno the
// filter returns — EPERM when SeccompValue.Errno is zero, the one it names
// otherwise —, and no directory is made. The child is this test binary, which
// makes the calls itself and reports each errno as a number: the verdict reads
// the kernel's answer, not a message a userland mkdir(1) words its own way
// (uutils' says "Permission denied" for EPERM).
func TestSandboxSeccompErrno(t *testing.T) {
	//: the probe half: make the calls, report, leave.
	if dir, ok := os.LookupEnv(seccompProbeEnv); ok {
		runSeccompProbe(t, dir)
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Skipf("no path to re-exec: %v", err)
	}
	type tc struct {
		name  string
		errno syscall.Errno
		want  syscall.Errno
	}
	tests := []tc{
		{"EPERM by default", 0, syscall.EPERM},
		{"the errno named", syscall.EROFS, syscall.EROFS},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			sb := &coreproc.SandboxValue{Seccomp: &coreproc.SeccompValue{
				Default: coreproc.SeccompAllow, Syscalls: []string{"mkdir", "mkdirat"},
				Action: coreproc.SeccompErrno, Errno: int(c.errno),
			}}
			got := seccompProbe(t, self, sb, dir)
			want := "mkdirat " + strconv.Itoa(int(c.want))
			//: mkdir(2) exists on amd64 alone; arm64 has mkdirat(2) only.
			if _, ok := syscallNumbers()["mkdir"]; ok {
				want = "mkdir " + strconv.Itoa(int(c.want)) + "\n" + want
			}
			if got != want {
				t.Fatalf("the calls under the filter answered %q, want %q", got, want)
			}
			if _, err := os.Stat(filepath.Join(dir, "made")); !os.IsNotExist(err) {
				t.Fatalf("the directory was made: %v", err)
			}
		})
	}
}

// seccompProbe runs this test binary as the probe under sb, bounded by
// seccompProbeWait, and returns the report it wrote under dir.
func seccompProbe(t *testing.T, self string, sb *coreproc.SandboxValue, dir string) string {
	t.Helper()
	p, err := Start(t.Context(), coreproc.Spec{
		Path: self, Args: []string{self, "-test.run=^TestSandboxSeccompErrno$", "-test.count=1"},
		//: a race-enabled probe would sleep a second at its exit otherwise.
		Env:     []string{seccompProbeEnv + "=" + dir, "GORACE=atexit_sleep_ms=0"},
		Sandbox: sb, Stdio: coreproc.StdioNull,
	})
	if err != nil {
		t.Fatalf("Start(probe) = %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), seccompProbeWait)
	defer cancel()
	exit, err := p.(coreproc.ContextWaiter).WaitContext(ctx)
	//: past its bound: killed, collected, and the test fails.
	if errors.Is(err, context.DeadlineExceeded) {
		_ = p.Signal(coreproc.Signal(syscall.SIGKILL))
		_, _ = p.Wait()
		t.Fatalf("the probe ran past %v", seccompProbeWait)
	}
	report := readFile(t, filepath.Join(dir, "report"))
	if err != nil || exit.Code != 0 {
		t.Fatalf("the probe exited %+v, %v; it reported %q", exit, err, report)
	}
	return report
}

// runSeccompProbe makes mkdir(2) — where this architecture has it — and
// mkdirat(2) for dir/made, and writes each call's errno, 0 for a success, to
// dir/report: its standard output is the testing package's.
func runSeccompProbe(t *testing.T, dir string) {
	t.Helper()
	path, err := syscall.BytePtrFromString(filepath.Join(dir, "made"))
	if err != nil {
		t.Fatalf("the probe's path: %v", err)
	}
	atFDCWD := -100 // AT_FDCWD
	var report strings.Builder
	if nr, ok := syscallNumbers()["mkdir"]; ok {
		_, _, errno := syscall.Syscall(uintptr(nr), uintptr(unsafe.Pointer(path)), 0o700, 0)
		report.WriteString("mkdir " + strconv.Itoa(int(errno)) + "\n")
	}
	_, _, errno := syscall.Syscall(uintptr(syscallNumbers()["mkdirat"]), uintptr(atFDCWD), uintptr(unsafe.Pointer(path)), 0o700)
	report.WriteString("mkdirat " + strconv.Itoa(int(errno)) + "\n")
	if err := os.WriteFile(filepath.Join(dir, "report"), []byte(report.String()), 0o600); err != nil {
		t.Fatalf("writing the report: %v", err)
	}
}

// TestSandboxSeccompKill proves SeccompKill: the program that makes the call
// is killed by SIGSYS.
func TestSandboxSeccompKill(t *testing.T) {
	sb := &coreproc.SandboxValue{Seccomp: &coreproc.SeccompValue{Default: coreproc.SeccompAllow, Syscalls: []string{"uname"}, Action: coreproc.SeccompKill}}
	exit, err := startWait(t, coreproc.Spec{Path: "/bin/uname", Sandbox: sb, Stdio: coreproc.StdioNull})
	if err != nil {
		t.Fatal(err)
	}
	if !exit.Signaled || exit.Signal != coreproc.Signal(syscall.SIGSYS) {
		t.Fatalf("uname under a filter killing uname(2) exited %+v, want killed by SIGSYS", exit)
	}
}

// TestSandboxCapabilities proves the bounding set in the child: root of its own
// user namespace writes a mode-000 file through CAP_DAC_OVERRIDE, and with the
// bounding set emptied the same write is refused and the file is unchanged.
func TestSandboxCapabilities(t *testing.T) {
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.WriteFile(locked, []byte("before\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	script := `if echo after > "$0/locked" 2>/dev/null; then echo wrote; else echo refused; fi > "$0/verdict"; cat /proc/self/status > "$0/status"`
	requireUserNamespace(t)
	_, err := sandboxed(t, inUserNS(0), script, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "verdict")); got != "wrote" {
		t.Fatalf("root of the namespace, every capability kept, %q the mode-000 file: the control failed", got)
	}
	//: the control wrote the file: put it back, mode 000.
	for _, step := range []func() error{
		func() error { return os.Chmod(locked, 0o600) },
		func() error { return os.WriteFile(locked, []byte("before\n"), 0o600) },
		func() error { return os.Chmod(locked, 0o000) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	sb := inUserNS(0)
	sb.Capabilities = &coreproc.CapabilitiesValue{}
	if _, err := sandboxed(t, sb, script, dir); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "verdict")); got != "refused" {
		t.Fatalf("with no capability the write was %q, want refused", got)
	}
	status := readFile(t, filepath.Join(dir, "status"))
	if got := statusField(status, "CapBnd"); got != "0000000000000000" {
		t.Errorf("CapBnd in the child = %q, want empty", got)
	}
	if err := os.Chmod(locked, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, locked); got != "before" {
		t.Fatalf("the file holds %q after the refused write", got)
	}
}

// TestSandboxAmbientCapabilities proves the ambient set: the child execs with
// CAP_DAC_OVERRIDE ambient and nothing else in its bounding set.
func TestSandboxAmbientCapabilities(t *testing.T) {
	requireUserNamespace(t)
	dir := t.TempDir()
	sb := inUserNS(0)
	sb.Capabilities = &coreproc.CapabilitiesValue{Bounding: 1 << capDACOverride, Ambient: 1 << capDACOverride}
	_, err := sandboxed(t, sb, `cat /proc/self/status > "$0/status"`, dir)
	if err != nil {
		t.Fatal(err)
	}
	status := readFile(t, filepath.Join(dir, "status"))
	for field, want := range map[string]string{"CapAmb": "0000000000000002", "CapBnd": "0000000000000002", "CapInh": "0000000000000002"} {
		if got := statusField(status, field); got != want {
			t.Errorf("%s in the child = %q, want %q", field, got, want)
		}
	}
}

// TestSandboxMounts proves the mounts, in the child's own mount namespace: a
// tmpfs the child writes into leaves the directory beneath it empty — mode
// 0755, or 1777 for a sticky one —, a tree made read-only refuses the child's
// write, a bind shows the source at the target — and nothing propagates back
// to the test's namespace.
func TestSandboxMounts(t *testing.T) {
	requireUserNamespace(t)
	dir := t.TempDir()
	for _, d := range []string{"tmp", "sticky", "ro", "src", "dst", "out"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "seen"), []byte("from src\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := inUserNS(coreproc.NamespaceMount)
	sb.Mounts = []coreproc.MountValue{
		{Kind: coreproc.MountTmpfs, Target: filepath.Join(dir, "tmp")},
		{Kind: coreproc.MountTmpfsSticky, Target: filepath.Join(dir, "sticky")},
		{Kind: coreproc.MountReadOnly, Target: filepath.Join(dir, "ro")},
		{Kind: coreproc.MountBind, Source: filepath.Join(dir, "src"), Target: filepath.Join(dir, "dst"), ReadOnly: true},
	}
	script := `echo in-tmpfs > "$0/tmp/f" && echo tmpfs-ok > "$0/out/tmpfs"
echo $(stat -c %a "$0/tmp" "$0/sticky") > "$0/out/modes"
if echo x > "$0/ro/f" 2>/dev/null; then echo wrote; else echo refused; fi > "$0/out/ro"
cat "$0/dst/seen" > "$0/out/bind"
if echo x > "$0/dst/new" 2>/dev/null; then echo wrote; else echo refused; fi > "$0/out/bindro"`
	_, err := sandboxed(t, sb, script, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"tmpfs": "tmpfs-ok", "modes": "755 1777", "ro": "refused", "bind": "from src", "bindro": "refused"}
	for name, w := range want {
		if got := readFile(t, filepath.Join(dir, "out", name)); got != w {
			t.Errorf("%s: the child saw %q, want %q", name, got, w)
		}
	}
	for _, d := range []string{"tmp", "sticky", "ro", "dst"} {
		if entries, _ := os.ReadDir(filepath.Join(dir, d)); len(entries) != 0 {
			t.Errorf("%s holds %d entries in the test's namespace: a mount propagated", d, len(entries))
		}
	}
}

// TestSandboxPIDAndNetworkNamespaces proves the namespaces: the child is PID 1
// of its own, and its network namespace has the loopback alone.
func TestSandboxPIDAndNetworkNamespaces(t *testing.T) {
	requireUserNamespace(t)
	dir := t.TempDir()
	_, err := sandboxed(t, inUserNS(coreproc.NamespacePID|coreproc.NamespaceNet), `echo $$ > "$0/pid"; cat /proc/self/net/dev > "$0/net"`, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "pid")); got != "1" {
		t.Errorf("the child's pid in its namespace is %q, want 1", got)
	}
	var ifaces []string
	for line := range strings.SplitSeq(readFile(t, filepath.Join(dir, "net")), "\n") {
		if name, _, ok := strings.Cut(line, ":"); ok && !strings.Contains(name, "|") {
			ifaces = append(ifaces, strings.TrimSpace(name))
		}
	}
	if !slices.Equal(ifaces, []string{"lo"}) {
		t.Errorf("the child's interfaces are %q, want lo alone", ifaces)
	}
}

// TestSandboxPivotRoot proves the new root: the child, pivoted into a tree of
// the test's own holding /usr bound read-only and an output directory, lists
// that tree as its / — the host's root is out of its reach.
func TestSandboxPivotRoot(t *testing.T) {
	requireUserNamespace(t)
	root, out := t.TempDir(), t.TempDir()
	for _, d := range []string{"usr", "out"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var binds []coreproc.MountValue
	for _, l := range []string{"bin", "lib", "lib64"} {
		target, err := os.Readlink("/" + l)
		//: a merged /usr: the same link in the new root.
		if err == nil {
			if err := os.Symlink(target, filepath.Join(root, l)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		//: a real directory (Alpine's /bin and /lib): bound read-only, the new
		//: root's /bin/sh and its loader are there.
		if info, statErr := os.Stat("/" + l); statErr == nil && info.IsDir() {
			if err := os.Mkdir(filepath.Join(root, l), 0o755); err != nil {
				t.Fatal(err)
			}
			binds = append(binds, coreproc.MountValue{Kind: coreproc.MountBind, Source: "/" + l, Target: filepath.Join(root, l), ReadOnly: true})
		}
	}
	sb := inUserNS(coreproc.NamespaceMount)
	sb.Mounts = append([]coreproc.MountValue{
		{Kind: coreproc.MountBind, Source: "/usr", Target: filepath.Join(root, "usr"), ReadOnly: true},
		{Kind: coreproc.MountBind, Source: out, Target: filepath.Join(root, "out")},
	}, binds...)
	sb.Root = root
	spec := coreproc.Spec{Path: "/bin/sh", Args: []string{"sh", "-c", `ls / > /out/ls; pwd > /out/pwd`}, Dir: "/out", Sandbox: sb, Stdio: coreproc.StdioNull}
	_, err := startWait(t, spec)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	var want []string
	for _, e := range entries {
		want = append(want, e.Name())
	}
	if got := strings.Fields(readFile(t, filepath.Join(out, "ls"))); !slices.Equal(got, want) {
		t.Fatalf("the child's / lists %q, want the new root's %q", got, want)
	}
	if got := readFile(t, filepath.Join(out, "pwd")); got != "/out" {
		t.Errorf("the child's working directory is %q, want /out inside the root", got)
	}
}

// TestSandboxDescriptors proves the close-on-exec sweep: the child's program
// holds its stdio, its ExtraFiles and the descriptor ls itself opens — never
// the payload's memfd, the handshake or a Landlock ruleset.
func TestSandboxDescriptors(t *testing.T) {
	dir := t.TempDir()
	extra, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	sb := &coreproc.SandboxValue{Landlock: &coreproc.LandlockValue{Handled: coreproc.LandlockMakeDir}, NoNewPrivileges: true}
	if _, abiErr := LandlockABI(); abiErr != nil {
		sb.Landlock = nil
	}
	spec := coreproc.Spec{Path: shellPath, Args: []string{shellPath, "-c", `ls /proc/self/fd > "$0/fds"`, dir}, Sandbox: sb, ExtraFiles: []*os.File{extra}, Stdio: coreproc.StdioNull}
	if _, err := startWait(t, spec); err != nil {
		t.Fatal(err)
	}
	//: 0-2 stdio, 3 the ExtraFile, 4 the directory ls opens to list.
	if got := strings.Fields(readFile(t, filepath.Join(dir, "fds"))); !slices.Equal(got, []string{"0", "1", "2", "3", "4"}) {
		t.Fatalf("the program holds descriptors %q, want 0 to 4", got)
	}
}

// TestSandboxFailClosedAtEveryStep is #728's fail-closed criterion, by
// injection: a failure at each step of the second stage is SandboxFailed
// naming that step, and the program never runs — its witness is never made.
func TestSandboxFailClosedAtEveryStep(t *testing.T) {
	for step := stepCgroupNamespace; step <= stepSeccomp; step++ {
		t.Run(sandboxStepName(step), func(t *testing.T) {
			withHost(t, func(h *sandboxHost) { h.failStep = step })
			marker := filepath.Join(t.TempDir(), "ran")
			_, err := sandboxed(t, &coreproc.SandboxValue{NoNewPrivileges: true}, `: > "$0"`, marker)
			if !errors.Is(err, coreproc.SandboxFailed) || fieldOf(err, "step") != sandboxStepName(step) {
				t.Fatalf("Start with step %d failing = %v, want SandboxFailed naming %s", step, err, sandboxStepName(step))
			}
			if !errors.Is(err, stageInjectedErrno) {
				t.Errorf("the failure does not carry the injected errno: %v", err)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatalf("the program ran after step %s failed", sandboxStepName(step))
			}
		})
	}
}

// TestSandboxOrderOnOneThread is #728's order criterion: an instrumented child
// records each step and the thread that runs it. The steps come in the
// kernel's order — the cgroup namespace, mounts, pivot_root, the Landlock paths, the bounding set,
// securebits, the identity, the capabilities, the descriptor sweep,
// no_new_privs, landlock_restrict_self, seccomp — every one on the main
// thread, the one that becomes the program; and the program finds in its own
// /proc/self/status the per-thread state the steps set — no_new_privs, the
// filter, the bounding set — which it would not hold had the execve run on
// another thread than theirs.
func TestSandboxOrderOnOneThread(t *testing.T) {
	requireUserNamespace(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	withHost(t, func(h *sandboxHost) { h.traceFD = 3 })
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	sb := inUserNS(coreproc.NamespaceMount)
	sb.Mounts = []coreproc.MountValue{{Kind: coreproc.MountTmpfs, Target: filepath.Join(dir, "tmp")}}
	sb.Capabilities = &coreproc.CapabilitiesValue{Bounding: 1 << capDACOverride}
	sb.Seccomp = &coreproc.SeccompValue{Default: coreproc.SeccompAllow, Syscalls: []string{"mkdirat"}, Action: coreproc.SeccompErrno}
	if _, abiErr := LandlockABI(); abiErr == nil {
		sb.Landlock = &coreproc.LandlockValue{Handled: coreproc.LandlockMakeDir}
	}
	spec := coreproc.Spec{
		Path: shellPath, Args: []string{shellPath, "-c", `cat /proc/self/status > "$0/status"`, dir},
		Sandbox: sb, ExtraFiles: []*os.File{w}, Stdio: coreproc.StdioNull,
	}
	p, err := Start(context.Background(), spec)
	w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	trace, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace)%traceRecordLen != 0 {
		t.Fatalf("a trace of %d bytes is not whole records", len(trace))
	}
	var steps []uint8
	for rec := range slices.Chunk(trace, traceRecordLen) {
		steps = append(steps, rec[0])
		if tid := binary.LittleEndian.Uint32(rec[1:]); int(tid) != p.PID() {
			t.Errorf("step %s ran on thread %d, not the main thread %d", sandboxStepName(rec[0]), tid, p.PID())
		}
	}
	want := []uint8{
		stepCgroupNamespace, stepMounts, stepRoot, stepLandlockPaths, stepBounding, stepSecureBits, stepIdentity,
		stepCapabilities, stepFDSweep, stepNoNewPrivs, stepLandlock, stepSeccomp,
	}
	if !slices.Equal(steps, want) {
		t.Fatalf("the steps ran in the order %v, want %v", steps, want)
	}
	status := readFile(t, filepath.Join(dir, "status"))
	for field, w := range map[string]string{"NoNewPrivs": "1", "Seccomp": "2", "CapBnd": "0000000000000002"} {
		if got := statusField(status, field); got != w {
			t.Errorf("the program's %s = %q, want %q: the execve ran without the stage's state", field, got, w)
		}
	}
}
