//go:build linux && (amd64 || arm64)

package service_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/exec"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// The sandbox wired into the engine (#728, S2): the spec each command is
// spawned with over the fake spawner, then real units whose sandbox the
// directives build, each restriction proved by a failure inside the child —
// against witness directories under t.TempDir(), never anything else.

// specs are the specs the fake spawner was handed, in order.
func (s *fakeSpawner) specsSoFar() []coreproc.Spec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]coreproc.Spec(nil), s.specs...)
}

// TestSandboxedCommandsOverTheShell: the shell hands every command the
// unit's sandbox — the directives', the credentials kept —, a "+" command
// none and the credentials neither, a "!" command the sandbox without the
// credentials.
func TestSandboxedCommandsOverTheShell(t *testing.T) {
	u, sp, _ := newUnit(t, service.Config{
		AllowPrivilegedControl: true, NoNewPrivileges: true, PrivateNetwork: true,
		SystemCallFilter: coresvc.SystemCallFilterValue{Syscalls: []string{"mkdirat"}, Deny: true},
		Spec:             coreproc.Spec{User: "svc"},
		ExecStartPre:     []coresvc.CommandValue{command(t, "+/bin/true")},
		ExecStart:        []coresvc.CommandValue{command(t, "/bin/daemon")},
		ExecStartPost:    []coresvc.CommandValue{command(t, "!/bin/true")},
		ExecStop:         []coresvc.CommandValue{command(t, "/bin/true")},
	})
	ctx := context.Background()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := u.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	specs := sp.specsSoFar()
	if len(specs) != 4 {
		t.Fatalf("%d spawns, want 4", len(specs))
	}
	confined := func(sb *coreproc.SandboxValue) bool {
		return sb != nil && sb.NoNewPrivileges && sb.Namespaces&coreproc.NamespaceNet != 0 && sb.Seccomp != nil
	}
	pre, main, post, stop := specs[0], specs[1], specs[2], specs[3]
	if pre.Sandbox != nil || pre.User != "" {
		t.Errorf(`"+" ExecStartPre=: sandbox %+v, user %q`, pre.Sandbox, pre.User)
	}
	if !confined(main.Sandbox) || main.User != "svc" {
		t.Errorf("ExecStart=: sandbox %+v, user %q", main.Sandbox, main.User)
	}
	if !confined(post.Sandbox) || post.User != "" {
		t.Errorf(`"!" ExecStartPost=: sandbox %+v, user %q`, post.Sandbox, post.User)
	}
	if !confined(stop.Sandbox) || stop.User != "svc" {
		t.Errorf("ExecStop=: sandbox %+v, user %q", stop.Sandbox, stop.User)
	}
}

// requireSandbox skips unless this host spawns a sandboxed child: an
// unprivileged user namespace with a mount namespace and a tmpfs, the
// bounding set cut, no_new_privs and a seccomp filter.
func requireSandbox(t *testing.T) {
	t.Helper()
	sb := &coreproc.SandboxValue{
		Namespaces:      coreproc.NamespaceUser | coreproc.NamespaceMount | coreproc.NamespaceNet,
		Mounts:          []coreproc.MountValue{{Kind: coreproc.MountTmpfsSticky, Target: t.TempDir()}},
		Capabilities:    &coreproc.CapabilitiesValue{Bounding: 1},
		NoNewPrivileges: true,
		Seccomp:         &coreproc.SeccompValue{Default: coreproc.SeccompAllow, Syscalls: []string{"mkdirat"}, Action: coreproc.SeccompErrno},
	}
	p, err := exec.Start(context.Background(), coreproc.Spec{Path: "/bin/true", Stdio: coreproc.StdioNull, Sandbox: sb})
	if err == nil {
		var ev coreproc.ExitValue
		if ev, err = p.Wait(); err == nil && ev.Code != 0 {
			t.Skipf("a sandboxed /bin/true ended %+v", ev)
		}
	}
	if err != nil {
		t.Skipf("this host cannot sandbox a child: %v", err)
	}
}

// writeRights are the Landlock rights the real test restricts.
const writeRights = coreproc.LandlockWriteFile | coreproc.LandlockMakeReg

// sandboxScript is what the real unit's child runs, $0 the test's directory
// and $1 an entry of the host's /tmp (or "-"): each restriction is tried and
// its outcome written under $0/out.
const sandboxScript = `out=$0/out
grep '^NoNewPrivs:' /proc/self/status | tr -d ' \t' > $out/nnp
if mkdir $out/made 2>$out/mkdir.err; then echo made; else echo refused; fi > $out/mkdir
if echo x 2>$out/ro.err > $0/ro/f; then echo written; else echo refused; fi > $out/ro
if echo x 2>$out/land.err > $0/land/f; then echo written; else echo refused; fi > $out/land
if [ -e $0/hide/witness ]; then echo seen; else echo hidden; fi > $out/hide
stat -f -c %T /tmp > $out/tmp.fs
stat -c %a /tmp /var/tmp | tr '\n' ' ' > $out/tmp.mode
ls -A /tmp | wc -l | tr -d ' ' > $out/tmp.entries
if [ "$1" != - ] && [ -e "/tmp/$1" ]; then echo seen; else echo absent; fi > $out/tmp.host
echo private > /tmp/mine && cat /tmp/mine > $out/tmp.write
tail -n +3 /proc/net/dev | cut -d: -f1 | tr -d ' ' | tr '\n' ' ' > $out/net
awk '$5 == "/usr" { print $6 }' /proc/self/mountinfo | tail -n 1 > $out/usr
exit 0`

// TestRealSandboxFromDirectives: a oneshot whose sandbox the directives build
// — NoNewPrivileges=, a deny-list SystemCallFilter= with
// SystemCallErrorNumber=, PrivateTmp=, ReadOnlyPaths=, TemporaryFileSystem=,
// PrivateNetwork=, ProtectSystem= — with Landlock from Spec.Sandbox, the
// supervisor unprivileged (its user namespace implied) or root. "+" runs
// unconfined, "!" keeps the sandbox.
func TestRealSandboxFromDirectives(t *testing.T) {
	requireSandbox(t)
	dir := t.TempDir()
	for _, d := range []string{"out", "ro", "land", "hide"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "hide", "witness"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	hostEntry := "-"
	if entries, err := os.ReadDir("/tmp"); err == nil && len(entries) > 0 {
		hostEntry = entries[0].Name()
	}
	var tpl *coreproc.SandboxValue
	_, landlockErr := exec.LandlockABI()
	if landlockErr == nil {
		tpl = &coreproc.SandboxValue{Landlock: &coreproc.LandlockValue{Handled: writeRights, Rules: []coreproc.LandlockRuleValue{
			{Path: filepath.Join(dir, "out"), Access: writeRights},
			{Path: filepath.Join(dir, "ro"), Access: writeRights},
			{Path: "/tmp", Access: writeRights},
			{Path: "/dev", Access: writeRights},
		}}}
	}
	probe := func(prefix, name string) coresvc.CommandValue {
		return command(t, prefix+":/bin/sh", "-c", `grep '^NoNewPrivs:' /proc/self/status | tr -d ' \t' > "$0/out/$1"`, dir, name)
	}
	u := realUnit(t, service.Config{
		Type: coresvc.TypeOneshot, AllowPrivilegedControl: true,
		NoNewPrivileges: true, PrivateNetwork: true, PrivateTmp: coresvc.PrivateTmpDisconnected,
		ProtectSystem:         coresvc.ProtectSystemYes,
		ReadOnlyPaths:         []string{filepath.Join(dir, "ro")},
		TemporaryFileSystem:   []coresvc.TemporaryFileSystemValue{{Path: filepath.Join(dir, "hide")}},
		SystemCallFilter:      coresvc.SystemCallFilterValue{Syscalls: []string{"mkdir", "mkdirat"}, Deny: true},
		SystemCallErrorNumber: int(syscall.EPERM),
		Spec:                  coreproc.Spec{Env: []string{"PATH=/usr/bin:/bin"}, Sandbox: tpl},
		ExecStartPre:          []coresvc.CommandValue{probe("+", "plus")},
		ExecStart:             []coresvc.CommandValue{command(t, ":/bin/sh", "-c", sandboxScript, dir, hostEntry)},
		ExecStartPost:         []coresvc.CommandValue{probe("!", "bang")},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	want := map[string]string{
		"nnp": "NoNewPrivs:1", "plus": "NoNewPrivs:0", "bang": "NoNewPrivs:1",
		"mkdir": "refused", "ro": "refused", "hide": "hidden",
		"tmp.fs": "tmpfs", "tmp.mode": "1777 1777 ", "tmp.entries": "0", "tmp.host": "absent", "tmp.write": "private",
		"net": "lo ",
	}
	if landlockErr == nil {
		want["land"] = "refused"
	}
	for name, value := range want {
		if got := readOut(t, dir, name); got != value {
			t.Errorf("%s: %q, want %q", name, got, value)
		}
	}
	errors := map[string]string{"mkdir.err": "Operation not permitted", "ro.err": "Read-only file system"}
	if landlockErr == nil {
		errors["land.err"] = "Permission denied"
	}
	for name, text := range errors {
		if got := readOut(t, dir, name); !strings.Contains(got, text) {
			t.Errorf("%s: %q, want %q", name, got, text)
		}
	}
	if got := readOut(t, dir, "usr"); !strings.HasPrefix(got, "ro") {
		t.Errorf("/usr mounted %q, want read-only", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "made")); err == nil {
		t.Error("the filter let mkdir through")
	}
}

// readOut is what the child wrote at $0/out/name, trailing newline cut.
func readOut(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "out", name))
	if err != nil {
		t.Fatalf("the child wrote no %s: %v", name, err)
	}
	return strings.TrimSuffix(string(raw), "\n")
}

// TestRealSandboxFailureFailsTheStart: a sandbox whose step fails in the
// child — a bind onto a path that does not exist — fails the start with
// START_FAILED wrapping SANDBOX_FAILED and the result exit-code; the program
// never ran.
func TestRealSandboxFailureFailsTheStart(t *testing.T) {
	requireSandbox(t)
	dir := t.TempDir()
	u := realUnit(t, service.Config{
		Type:      coresvc.TypeOneshot,
		BindPaths: []coresvc.BindPathValue{{Source: dir, Target: filepath.Join(dir, "absent")}},
		ExecStart: []coresvc.CommandValue{command(t, ":/bin/sh", "-c", `echo ran > "$0/ran"`, dir)},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := u.Start(ctx)
	if !errs.HasCode(err, coresvc.CodeStartFailed) || !errs.HasCode(err, coreproc.CodeSandboxFailed) || fieldOf(err, "step") != "mounts" {
		t.Fatalf("Start: %v (fields %v), want START_FAILED wrapping SANDBOX_FAILED at the mounts", err, errs.FieldsOf(err))
	}
	if st := u.State(); st.Result != coresvc.ResultExitCode || st.State != coresvc.StateFailed {
		t.Errorf("after the failure: %+v", st)
	}
	if _, serr := os.Stat(filepath.Join(dir, "ran")); serr == nil {
		t.Error("the program ran outside its sandbox")
	}
}

// TestLiveSandboxedUnitInItsGroup: a sandboxed unit in a real delegated
// group (S0g with S2): its main process is spawned into the group, confined
// — no_new_privs, the filter, its private /tmp —, the stop empties the group,
// and the group takes the next start.
func TestLiveSandboxedUnitInItsGroup(t *testing.T) {
	requireSandbox(t)
	g := liveUnitGroup(t)
	cg := liveGroupUnit(t, g)
	dir := t.TempDir()
	u := realUnit(t, service.Config{
		Cgroup: cg, TimeoutStop: 10 * time.Second,
		NoNewPrivileges: true, PrivateTmp: coresvc.PrivateTmpDisconnected,
		SystemCallFilter:      coresvc.SystemCallFilterValue{Syscalls: []string{"mkdir", "mkdirat"}, Deny: true},
		SystemCallErrorNumber: int(syscall.EPERM),
		Spec:                  coreproc.Spec{Env: []string{"PATH=/usr/bin:/bin"}},
		ExecStart: []coresvc.CommandValue{command(t, ":/bin/sh", "-c",
			`mkdir "$0/made" 2>"$0/mkdir.err"; stat -c %a /tmp > "$0/tmp.mode"; exec sleep 600`, dir)},
	})
	for round := range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := u.Start(ctx); err != nil {
			cancel()
			t.Fatalf("start %d: %v", round, err)
		}
		main := u.State().MainPID
		waitFile(t, filepath.Join(dir, "tmp.mode"))
		if in, err := cg.Contains(main); err != nil || !in {
			t.Errorf("start %d: main process %d outside the unit's group (%v)", round, main, err)
		}
		status, err := os.ReadFile("/proc/" + strconv.Itoa(main) + "/status")
		if err != nil || !strings.Contains(string(status), "NoNewPrivs:\t1") || !strings.Contains(string(status), "Seccomp:\t2") {
			t.Errorf("start %d: main process not confined: %v\n%s", round, err, status)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "mkdir.err")); !strings.Contains(string(got), "Operation not permitted") {
			t.Errorf("start %d: mkdir said %q", round, got)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "tmp.mode")); strings.TrimSpace(string(got)) != "1777" {
			t.Errorf("start %d: /tmp mode %q", round, got)
		}
		if err := u.Stop(ctx); err != nil {
			cancel()
			t.Fatalf("stop %d: %v", round, err)
		}
		cancel()
		if members, err := g.(coreproc.GroupReader).Members(); err != nil || len(members) != 0 {
			t.Errorf("stop %d: the group holds %v (%v)", round, members, err)
		}
		if err := os.Remove(filepath.Join(dir, "tmp.mode")); err != nil {
			t.Fatal(err)
		}
	}
}
