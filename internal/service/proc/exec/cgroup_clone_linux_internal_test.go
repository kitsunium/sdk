//go:build linux

package exec

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_releaseClonesIntoCgroup pins the probe's reading of uname's release:
// CLONE_INTO_CGROUP arrived in Linux 5.7.
func Test_releaseClonesIntoCgroup(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"5.6.19":               false,
		"5.7.0":                true,
		"5.10.0-28-amd64":      true,
		"6.12.101+deb13-amd64": true,
		"4.19.0":               false,
		"garbage":              false,
		"":                     false,
	}
	for release, want := range tests {
		if got := releaseClonesIntoCgroup(release); got != want {
			t.Errorf("releaseClonesIntoCgroup(%q) = %v, want %v", release, got, want)
		}
	}
}

// Test_needsTrampoline_cgroupByClone pins #929's routing: a CgroupPath alone
// no longer re-execs the supervisor where the kernel clones the child straight
// into the group, and still does where it cannot; Rlimits and Umask keep the
// trampoline whatever the kernel.
func Test_needsTrampoline_cgroupByClone(t *testing.T) {
	type tc struct {
		name   string
		spec   coreproc.Spec
		clones bool
		want   bool
	}
	cg := "/sys/fs/cgroup/x"
	tests := []tc{
		{"a cgroup alone, kernel clones into it", coreproc.Spec{CgroupPath: cg}, true, false},
		{"a cgroup alone, kernel below 5.7", coreproc.Spec{CgroupPath: cg}, false, true},
		{"a cgroup and a umask, kernel clones", coreproc.Spec{CgroupPath: cg, Umask: new(0o22)}, true, true},
		{"nothing pre-exec", coreproc.Spec{}, true, false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			restore := cloneIntoCgroupProbe
			cloneIntoCgroupProbe = func() bool { return c.clones }
			defer func() { cloneIntoCgroupProbe = restore }()
			if got := needsTrampoline(c.spec); got != c.want {
				t.Errorf("needsTrampoline = %v, want %v", got, c.want)
			}
		})
	}
}

// Test_cloneSpawnError pins how a refused clone into the group is reported: a
// kernel or filter without clone3 (ENOSYS) sends the spawn back through the
// trampoline once, a refusal by the group (not delegated, a domain with
// controllers) is CGROUP_WRITE_FAILED as the trampoline's 'C' byte was, and
// any other fault stays SPAWN_FAILED.
func Test_cloneSpawnError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		err       error
		wantRetry bool
		wantCode  errs.Code
	}
	tests := []tc{
		{"clone3 filtered", syscall.ENOSYS, true, 0},
		{"a group not delegated", syscall.EACCES, false, coreproc.CodeCgroupWriteFailed},
		{"a domain group with controllers", syscall.EBUSY, false, coreproc.CodeCgroupWriteFailed},
		{"the binary is missing", syscall.ENOENT, false, coreproc.CodeSpawnFailed},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var refused atomic.Bool
			retry, err := cloneSpawnError(c.err, "/sys/fs/cgroup/x", "/bin/true", &refused)
			if refused.Load() != c.wantRetry {
				t.Errorf("refusal remembered = %v, want %v", refused.Load(), c.wantRetry)
			}
			if retry != c.wantRetry {
				t.Fatalf("retry = %v, want %v", retry, c.wantRetry)
			}
			if !c.wantRetry && !errs.HasCode(err, c.wantCode) {
				t.Errorf("err = %v, want code %v", err, c.wantCode)
			}
		})
	}
}

// delegatedSubtree creates a child group under this process's own cgroup and
// returns it, or skips when the test (or benchmark) may not: the group is not delegated to
// this user, or cgroup v2 is absent. It is removed when the test ends.
func delegatedSubtree(tb testing.TB) string {
	tb.Helper()
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		tb.Skipf("no /proc/self/cgroup: %v", err)
	}
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, "0::") {
		tb.Skip("not a cgroup v2 unified hierarchy")
	}
	sub := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(line, "0::"), "sdk-exec-test-"+filepath.Base(tb.TempDir()))
	if err := os.Mkdir(sub, 0o755); err != nil {
		tb.Skipf("this process's cgroup is not delegated to it: %v", err)
	}
	tb.Cleanup(func() {
		if err := os.Remove(sub); err != nil {
			tb.Logf("removing %s: %v", sub, err)
		}
	})
	return sub
}

// TestStart_clonesIntoTheCgroup pins #929 where a delegated subtree exists (the
// linux-init lane): a CgroupPath alone spawns directly — no trampoline — and
// the child's first action reads the target group in /proc/self/cgroup.
func TestStart_clonesIntoTheCgroup(t *testing.T) {
	t.Parallel()
	if !cloneIntoCgroupProbe() {
		t.Skip("kernel below 5.7: the trampoline places the child")
	}
	sub := delegatedSubtree(t)
	spec := coreproc.Spec{Path: "/bin/cat", Args: []string{"cat", "/proc/self/cgroup"}, CgroupPath: sub}
	if needsTrampoline(spec) {
		t.Fatal("a CgroupPath alone still routes through the trampoline")
	}
	var out syncBuffer
	spec.Stdio, spec.Stdout = coreproc.StdioCapture, &out
	p, err := Start(t.Context(), spec)
	if err != nil {
		t.Fatalf("Start = %v", err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatalf("Wait = %v", err)
	}
	if !strings.Contains(out.String(), filepath.Base(sub)) {
		t.Errorf("the child read %q, want its group %s", out.String(), sub)
	}
}
