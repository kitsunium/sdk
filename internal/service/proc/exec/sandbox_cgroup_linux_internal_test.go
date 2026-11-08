//go:build linux && (amd64 || arm64)

package exec

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// liveCgroupRootEnv names a delegated cgroup v2 directory a lane hands to the
// test, as it does to proc/cgroup's and proc/service's live suites: a group
// made there is beside the test's own, not beneath it.
const liveCgroupRootEnv string = "KITSUNIUM_CGROUP_TEST_ROOT"

// placementGroup returns a group to spawn a child into, removed when the test
// ends: one under the root liveCgroupRootEnv names when a lane hands one,
// else one beneath this process's own group (delegatedSubtree). It skips
// when neither may be made.
func placementGroup(t *testing.T) string {
	t.Helper()
	root := os.Getenv(liveCgroupRootEnv)
	if root == "" {
		return delegatedSubtree(t)
	}
	dir := filepath.Join(root, "sdk-exec-sandbox-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Skipf("%s=%s is not delegated to this test: %v", liveCgroupRootEnv, root, err)
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for os.Remove(dir) != nil && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	})
	return dir
}

// TestSandboxCgroupNamespaceRootedAtTheGroup pins the junction of #728 with
// the placement of #929: a sandboxed child asking NamespaceCgroup, spawned
// into a group through Spec.CgroupPath, has that group as its cgroup root
// whichever placed it — the clone (CLONE_INTO_CGROUP), or the trampoline's
// write on a kernel before 5.7 or under a filter refusing clone3. Taken at the
// clone, the namespace was rooted at the supervisor's group on the
// trampoline's path: the child saw its group beneath that root, and a group
// outside it refused the trampoline's write (CGROUP_WRITE_FAILED).
func TestSandboxCgroupNamespaceRootedAtTheGroup(t *testing.T) {
	requireUserNamespace(t)
	for _, c := range []struct {
		name   string
		clones bool
	}{
		{"placed by the clone", true},
		{"placed by the trampoline", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.clones && !cloneIntoCgroupProbe() {
				t.Skip("kernel below 5.7: the clone places nothing")
			}
			group := placementGroup(t)
			restore := cloneIntoCgroupProbe
			cloneIntoCgroupProbe = func() bool { return c.clones }
			t.Cleanup(func() { cloneIntoCgroupProbe = restore })
			out := t.TempDir()
			spec := coreproc.Spec{
				Path: shellPath, Args: []string{shellPath, "-c", `cat /proc/self/cgroup > "$0/cgroup"`, out},
				CgroupPath: group, Sandbox: inUserNS(coreproc.NamespaceCgroup), Stdio: coreproc.StdioNull,
			}
			exit, err := startWait(t, spec)
			if err != nil || exit.Code != 0 {
				t.Fatalf("a sandboxed spawn into %s %s = %+v, %v", group, c.name, exit, err)
			}
			if got := readFile(t, filepath.Join(out, "cgroup")); got != "0::/" {
				t.Fatalf("the child reads its group as %q, want \"0::/\": its cgroup namespace is not rooted at the group it was spawned into", got)
			}
		})
	}
}
