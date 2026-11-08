//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package service_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// The junction of S0c (#735) with S1 (#727): a Type=forking unit whose
// Config.Cgroup is the group proc/cgroup makes on Linux, its commands spawned
// straight into that group (Spec.CgroupPath, CLONE_INTO_CGROUP), its daemon
// guessed among the group's real members. Every test works inside a group it
// makes under a root delegated to it, and removes it; with no such root it
// skips. Nothing is written to a group the test did not make, and only
// processes the test started are signalled.

// liveCgroupRootEnv names a delegated cgroup v2 directory a lane hands to the
// test, as proc/cgroup's live suite reads it.
const liveCgroupRootEnv string = "KITSUNIUM_CGROUP_TEST_ROOT"

// liveCgroupRoot returns a cgroup v2 root delegated to this test, or skips:
// the one KITSUNIUM_CGROUP_TEST_ROOT names; the mount when Available (root on
// the linux-init lanes); this process's own group when Probe says it is
// delegated (`systemd-run --user --scope -p Delegate=yes go test …`).
func liveCgroupRoot(t *testing.T) string {
	t.Helper()
	if root := os.Getenv(liveCgroupRootEnv); root != "" {
		if p, err := cgroup.Probe(cgroup.WithRoot(root)); err == nil && p.Delegated {
			return root
		}
		t.Skipf("%s=%s is not a delegated cgroup v2 directory", liveCgroupRootEnv, root)
	}
	if cgroup.Available() {
		return "/sys/fs/cgroup"
	}
	if p, err := cgroup.Probe(); err == nil && p.OwnGroup != "" {
		if own, oerr := cgroup.Probe(cgroup.WithRoot(p.OwnGroup)); oerr == nil && own.Delegated {
			return p.OwnGroup
		}
	}
	t.Skip("no cgroup v2 subtree is delegated to this test")
	return ""
}

// liveUnitGroup makes the test's own group under the live root and, at the
// end, kills what is left in it — processes the test started, nothing else
// can be there — and removes it once empty.
func liveUnitGroup(t *testing.T) coreproc.Group {
	t.Helper()
	root := liveCgroupRoot(t)
	name := "sdk-svc-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	g, err := cgroup.Create(name, cgroup.WithRoot(root))
	if err != nil {
		t.Fatalf("making the test's group under %s: %v", root, err)
	}
	t.Cleanup(func() {
		_ = g.Kill()
		lister, _ := g.(coreproc.GroupReader)
		deadline := time.Now().Add(5 * time.Second)
		for lister != nil && time.Now().Before(deadline) {
			if pids, lerr := lister.Members(); lerr != nil || len(pids) == 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if derr := g.Delete(); derr != nil {
			t.Errorf("removing the test's group: %v", derr)
		}
	})
	return g
}

// TestLiveForkingGuessInARealCgroup: the Linux group cgroup.Create returns is
// both a CgroupKill and a CgroupMembers, so a Type=forking unit without
// PIDFile= takes it as its Cgroup; its command, spawned into the group, forks
// a daemon with two workers and exits; the unit guesses the daemon — the one
// member whose parent is outside the group — and the group's Contains says
// it is a member. The group's Kill then ends the daemon and its workers: the
// unit sees its main process end, and the group empties.
func TestLiveForkingGuessInARealCgroup(t *testing.T) {
	g := liveUnitGroup(t)
	cg, ok := g.(coresvc.CgroupKill)
	if !ok {
		t.Fatalf("the Linux group %T is no coresvc.CgroupKill", g)
	}
	if _, lists := g.(coresvc.CgroupMembers); !lists {
		t.Fatalf("the Linux group %T is no coresvc.CgroupMembers: GuessMainPID= could never guess", g)
	}
	tree, ok := g.(coreproc.GroupTree)
	if !ok {
		t.Fatalf("the Linux group %T is no coreproc.GroupTree: no path to spawn into", g)
	}
	mark := markOf(t)
	file := filepath.Join(t.TempDir(), "daemon.pid")
	body := `sleep 60 & sleep 60 & echo $$ > "` + file + `"; wait`
	script := `( setsid sh -c '` + body + `' & ); while [ ! -s '` + file + `' ]; do sleep 0.01; done; exit 0`
	u := realUnit(t, service.Config{
		Type:     coresvc.TypeForking,
		Attacher: service.NewAttacher(coreproc.AttachValue{UIDs: []int{os.Getuid()}}),
		Cgroup:   cg,
		Spec: coreproc.Spec{
			Env:        []string{"PATH=/usr/bin:/bin", mark},
			CgroupPath: tree.Path(),
		},
		ExecStart: []coresvc.CommandValue{command(t, ":/bin/sh", "-c", script)},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	daemon := readPID(t, file)
	if st := u.State(); st.State != coresvc.StateActive || st.MainPID != daemon {
		t.Fatalf("after Start: %s, main %d, want active with the guessed daemon %d", st.State, st.MainPID, daemon)
	}
	if in, err := cg.Contains(daemon); err != nil || !in {
		t.Fatalf("Contains(%d) = %v, %v: the daemon is not in the group its command was spawned into", daemon, in, err)
	}
	members, err := g.(coresvc.CgroupMembers).Members()
	if err != nil || !slices.Contains(members, daemon) || len(members) < 3 {
		t.Fatalf("Members = %v, %v: want the daemon %d and its two workers", members, err, daemon)
	}
	if err := cg.Kill(); err != nil {
		t.Fatalf("Kill = %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for u.State().State == coresvc.StateActive {
		if time.Now().After(deadline) {
			t.Fatalf("the unit is still active ten seconds after its group's Kill: %+v", u.State())
		}
		time.Sleep(10 * time.Millisecond)
	}
	for {
		left, lerr := g.(coresvc.CgroupMembers).Members()
		if lerr == nil && len(left) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the group still lists %v, %v after its Kill", left, lerr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
