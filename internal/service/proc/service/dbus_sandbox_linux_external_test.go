//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package service_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	procexec "github.com/kitsunium/sdk/internal/service/proc/exec"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// The junction of S0e (#737, Type=dbus) with S2 (#728, Spec.Sandbox), alone
// and in the unit's group (S0g, #734), against a real dbus-daemon the test
// launches under t.TempDir() (testBus). The test skips without dbus-daemon,
// without a delegated root for the group's case, and — after checking the
// refusal is typed — on a host that cannot sandbox a child. Only the test's
// own processes are signalled.

// junctionSandbox is a dbus service's confinement: its own user and PID
// namespaces — root inside mapped to the test's user —, a cgroup namespace
// when it runs in a group, no new privileges, and a filter refusing mkdir.
// The bus is a socket by path: no namespace here hides it.
func junctionSandbox(cgroupNS bool) *coreproc.SandboxValue {
	ns := coreproc.NamespaceUser | coreproc.NamespacePID
	if cgroupNS {
		ns |= coreproc.NamespaceCgroup
	}
	return &coreproc.SandboxValue{
		Namespaces:      ns,
		NoNewPrivileges: true,
		Seccomp:         &coreproc.SeccompValue{Default: coreproc.SeccompAllow, Syscalls: []string{"mkdir", "mkdirat"}, Action: coreproc.SeccompErrno},
	}
}

// requireSandboxOf skips the test on a host that cannot apply sb to a child at
// all — no unit involved —, after checking the refusal is typed: a GOARCH
// with no system call table, a kernel without seccomp, a user namespace
// refused.
func requireSandboxOf(t *testing.T, sb *coreproc.SandboxValue) {
	t.Helper()
	p, err := procexec.Start(context.Background(), coreproc.Spec{Path: "/bin/sh", Args: []string{"/bin/sh", "-c", ":"}, Sandbox: sb, Stdio: coreproc.StdioNull})
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

// statusField returns field's value in /proc/pid/status.
func statusField(t *testing.T, pid int, field string) string {
	t.Helper()
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, field+":"); ok {
			return strings.Join(strings.Fields(v), " ")
		}
	}
	return ""
}

// junctionDirectives is junctionSandbox(false) spelled as the directives of
// systemd.exec(5) a unit's Config carries (#728 wired in): PrivateUsers=,
// PrivatePIDs=, NoNewPrivileges= and SystemCallFilter=~mkdir mkdirat with
// SystemCallErrorNumber=EPERM.
func junctionDirectives(cfg *service.Config) {
	cfg.PrivateUsers, cfg.PrivatePIDs, cfg.NoNewPrivileges = true, true, true
	cfg.SystemCallFilter = coresvc.SystemCallFilterValue{Syscalls: []string{"mkdir", "mkdirat"}, Deny: true}
	cfg.SystemCallErrorNumber = 1
}

// TestLiveTypeDbusSandboxed: a sandboxed Type=dbus unit — its sandbox from
// Config.Spec (S2), alone and in its group (S0g), or from the directives
// resolveSandbox reads (#728 wired in), in its group — is up once its
// confined main process owns its name: the supervisor's watch, outside the
// sandbox, sees the owner the process inside made; the process is confined
// as asked (its user namespace maps root to the test's user, it is PID 1 of
// its own, no new privileges, a filter); the name given back stops the unit.
func TestLiveTypeDbusSandboxed(t *testing.T) {
	cases := []struct {
		name                string
		inGroup, directives bool
	}{
		{"alone", false, false},
		{"in_its_group", true, false},
		{"directives", true, true}, // the name is short for sun_path: t.TempDir() holds the bus's socket
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sb := junctionSandbox(c.inGroup && !c.directives)
			requireSandboxOf(t, sb)
			var (
				g  coreproc.Group
				cg coresvc.CgroupKill
			)
			if c.inGroup {
				g = liveUnitGroup(t)
				cg = liveGroupUnit(t, g)
			}
			address, conn := testBus(t)
			const name = "org.kitsunium.Test.Sandboxed"
			spec, edit := coreproc.Spec{Sandbox: sb}, func(*service.Config) {}
			if c.directives {
				spec, edit = coreproc.Spec{}, junctionDirectives
			}
			u := junctionBusUnit(t, conn, address, name, spec, cg, edit)
			sub, cancelSub := u.Subscribe(256)
			defer cancelSub()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			main := upOnItsName(ctx, t, u, conn, name)
			if got := statusField(t, main, "NSpid"); got != strconv.Itoa(main)+" 1" {
				t.Errorf("NSpid of the main process = %q, want %d 1: no PID namespace", got, main)
			}
			if got := statusField(t, main, "NoNewPrivs"); got != "1" {
				t.Errorf("NoNewPrivs = %q, want 1", got)
			}
			if got := statusField(t, main, "Seccomp"); got != "2" {
				t.Errorf("Seccomp = %q, want 2 (a filter)", got)
			}
			uidMap, err := os.ReadFile("/proc/" + strconv.Itoa(main) + "/uid_map")
			if want := "0 " + strconv.Itoa(os.Getuid()) + " 1"; err != nil || strings.Join(strings.Fields(string(uidMap)), " ") != want {
				t.Errorf("uid_map = %q, %v; want %q", uidMap, err, want)
			}
			if c.inGroup {
				if in, cerr := cg.Contains(main); cerr != nil || !in {
					t.Fatalf("Contains(%d) = %v, %v: not spawned into the unit's group", main, in, cerr)
				}
			}
			stoppedOnTheLoss(t, u, sub, main)
			if c.inGroup {
				emptied(t, g)
			}
		})
	}
}
