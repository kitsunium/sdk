//go:build !windows

package service

import (
	"errors"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The sandbox wired into the engine (#728 S2 in proc/service): what a "+" and
// a "!" command keep of it, the store's key, MAINPID='s uid-0 exemption, and
// a sandbox's failure at the spawn.

// templateSandbox is a sandbox a unit's template carries.
func templateSandbox() *coreproc.SandboxValue {
	return &coreproc.SandboxValue{
		Namespaces:      coreproc.NamespaceUser | coreproc.NamespaceMount,
		Mounts:          []coreproc.MountValue{{Kind: coreproc.MountTmpfs, Target: "/srv"}},
		NoNewPrivileges: true,
		Seccomp:         &coreproc.SeccompValue{Default: coreproc.SeccompAllow, Syscalls: []string{"mkdirat"}, Action: coreproc.SeccompErrno},
	}
}

// specOf builds the spec of command c of list l, as the shell does.
func specOf(t *testing.T, cfg *Config, s *settings, l list, c coresvc.CommandValue) coreproc.Spec {
	t.Helper()
	return buildSpec(&cfg.Spec, s, &effect{kind: efSpawn, list: l, cmd: c})
}

// TestPrivilegedPrefixesAndTheSandbox: systemd applies no namespacing,
// capability or filter option to a "+" line, and "!" lifts the credentials
// alone (systemd.service(5), Table 2).
func TestPrivilegedPrefixesAndTheSandbox(t *testing.T) {
	cfg := Config{
		Name: "unit", AllowPrivilegedControl: true,
		ExecStartPre:  []coresvc.CommandValue{cmd(t, "+/bin/pre")},
		ExecStart:     []coresvc.CommandValue{cmd(t, "/bin/main")},
		ExecStartPost: []coresvc.CommandValue{cmd(t, "!/bin/post")},
		Spec:          coreproc.Spec{User: "svc", Group: "svc", Sandbox: templateSandbox()},
	}
	sandboxHere(t)
	s, err := resolve(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	full := specOf(t, &cfg, s, listStartPre, cfg.ExecStartPre[0])
	if full.Sandbox != nil {
		t.Errorf(`"+" spawned under a sandbox: %+v`, *full.Sandbox)
	}
	if full.User != "" || full.Group != "" || full.Groups != nil {
		t.Errorf(`"+" kept the credentials: %q %q %q`, full.User, full.Group, full.Groups)
	}
	creds := specOf(t, &cfg, s, listStartPost, cfg.ExecStartPost[0])
	if creds.Sandbox == nil || !creds.Sandbox.NoNewPrivileges || creds.Sandbox.Seccomp == nil || len(creds.Sandbox.Mounts) != 1 {
		t.Errorf(`"!" lost the sandbox: %+v`, creds.Sandbox)
	}
	if creds.User != "" || creds.Group != "" {
		t.Errorf(`"!" kept the credentials: %q %q`, creds.User, creds.Group)
	}
	main := specOf(t, &cfg, s, listStart, cfg.ExecStart[0])
	if main.Sandbox == nil || main.User != "svc" || main.Group != "svc" {
		t.Errorf("the main command lost its sandbox or its credentials: %+v %q %q", main.Sandbox, main.User, main.Group)
	}
}

// storeConfig is a unit with a store, its template under sb.
func storeConfig(t *testing.T, sb *coreproc.SandboxValue) Config {
	storeHere(t)
	return Config{
		Name: "unit", Notify: stubNotify{}, FileDescriptorStoreMax: 4,
		ExecStart: []coresvc.CommandValue{cmd(t, "/bin/main")},
		Spec:      coreproc.Spec{User: "svc", Sandbox: sb},
	}
}

// keyOf is the store key a configuration resolves to.
func keyOf(t *testing.T, cfg Config) string {
	t.Helper()
	sandboxHere(t)
	s, err := resolve(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s.storeKey
}

// TestStoreKeyHoldsTheSandbox: a store is the main process's, and a
// sandboxed main process is another process — the key holds a digest of the
// sandbox, deterministic, so a store filled outside it is never handed in.
func TestStoreKeyHoldsTheSandbox(t *testing.T) {
	bare := keyOf(t, storeConfig(t, nil))
	boxed := keyOf(t, storeConfig(t, templateSandbox()))
	if bare == boxed {
		t.Fatalf("the key ignores the sandbox: %s", bare)
	}
	if again := keyOf(t, storeConfig(t, templateSandbox())); again != boxed {
		t.Errorf("the key of one sandbox changed: %s, then %s", boxed, again)
	}
	if empty := keyOf(t, storeConfig(t, &coreproc.SandboxValue{})); empty == bare {
		t.Errorf("an empty sandbox keyed as none: %s", empty)
	}
	edits := map[string]func(*coreproc.SandboxValue){
		"namespaces": func(sb *coreproc.SandboxValue) { sb.Namespaces |= coreproc.NamespaceNet },
		"mount":      func(sb *coreproc.SandboxValue) { sb.Mounts[0].ReadOnly = true },
		"root":       func(sb *coreproc.SandboxValue) { sb.Root = "/srv/root" },
		"nnp":        func(sb *coreproc.SandboxValue) { sb.NoNewPrivileges = false },
		"seccomp":    func(sb *coreproc.SandboxValue) { sb.Seccomp.Syscalls = []string{"mkdir"} },
		"errno":      func(sb *coreproc.SandboxValue) { sb.Seccomp.Errno = int(syscall.EACCES) },
		"caps":       func(sb *coreproc.SandboxValue) { sb.Capabilities = &coreproc.CapabilitiesValue{} },
		"landlock": func(sb *coreproc.SandboxValue) {
			sb.Landlock = &coreproc.LandlockValue{Handled: coreproc.LandlockWriteFile}
		},
		"uidmap": func(sb *coreproc.SandboxValue) {
			sb.UIDMap = []coreproc.IDMapValue{{Inside: 0, Outside: 1000, Size: 1}}
		},
	}
	for name, edit := range edits {
		sb := templateSandbox()
		edit(sb)
		if k := keyOf(t, storeConfig(t, sb)); k == boxed {
			t.Errorf("%s: a different sandbox keyed the same: %s", name, k)
		}
	}
}

// mainPIDFromRoot feeds a MAINPID= from the main process, sent as uid 0, to a
// unit with no cgroup.
func mainPIDFromRoot(t *testing.T, sb *coreproc.SandboxValue) *harness {
	t.Helper()
	sandboxHere(t)
	h := newHarness(t, Config{
		Type: coresvc.TypeNotify, Notify: stubNotify{}, Attacher: stubAttacher{},
		ExecStart: []coresvc.CommandValue{cmd(t, "/bin/main")},
		Spec:      coreproc.Spec{Sandbox: sb},
	})
	h.start()
	main := h.m.mainPID
	h.feed(event{kind: evNotify, msg: coresvc.NotifyValue{
		Fields: map[string]string{"MAINPID": "4242"}, PID: main, UID: 0, Verified: true,
	}})
	return h
}

// TestMainPIDRootExemptionLostWhenSandboxed: without a cgroup a MAINPID= is
// taken from a privileged sender only; a sandboxed unit's uid 0 is no
// privilege — root in its own user namespace, or confined — so the
// exemption goes (#733).
func TestMainPIDRootExemptionLostWhenSandboxed(t *testing.T) {
	if h := mainPIDFromRoot(t, nil); len(h.attaches) != 1 || h.attaches[0] != 4242 {
		t.Fatalf("an unconfined unit's root lost its exemption: attaches %v", h.attaches)
	}
	h := mainPIDFromRoot(t, templateSandbox())
	if len(h.attaches) != 0 {
		t.Fatalf("a sandboxed unit's uid 0 named its main process: attaches %v", h.attaches)
	}
	if h.m.notifyRefused != 1 {
		t.Errorf("refused %d, want 1", h.m.notifyRefused)
	}
}

// sandboxError is the error proc/exec returns for a sandbox's failure.
func sandboxError(code errs.Code) error {
	if code == coreproc.CodeSandboxFailed {
		return errs.Wrap(syscall.EPERM, errs.WrapParams{
			Code: code, Reason: "SANDBOX_FAILED", Public: "Could not confine the process",
			Private: "test: a step failed",
		}, errs.String("step", "mount"), errs.String("errno", "operation not permitted"))
	}
	return errs.Wrap(syscall.ENOSYS, errs.WrapParams{
		Code: code, Reason: "SANDBOX_UNAVAILABLE", Public: "The kernel cannot apply it",
		Private: "test: no Landlock",
	}, errs.String("feature", "landlock"))
}

// fieldOf is the value of err's field key, "" when absent.
func fieldOf(err error, key string) string {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == key {
			return f.StringValue()
		}
	}
	return ""
}

// TestSandboxSpawnFailureIsTyped: a sandbox that could not be applied is a
// start failure that says so — START_FAILED wrapping the sandbox's code and
// its step or feature — with its result explicit: exit-code for a step that
// failed in the child (systemd's EXIT_* statuses), resources for a part the
// kernel lacks, refused before the fork.
func TestSandboxSpawnFailureIsTyped(t *testing.T) {
	sandboxHere(t)
	cases := []struct {
		code   errs.Code
		result coresvc.Result
		field  string
		value  string
	}{
		{coreproc.CodeSandboxFailed, coresvc.ResultExitCode, "step", "mount"},
		{coreproc.CodeSandboxUnavailable, coresvc.ResultResources, "feature", "landlock"},
	}
	for _, c := range cases {
		h := newHarness(t, Config{ExecStart: []coresvc.CommandValue{cmd(t, "/bin/main")}, Spec: coreproc.Spec{Sandbox: templateSandbox()}})
		h.failSpawn[listStart], h.spawnErr = true, sandboxError(c.code)
		id := h.start()
		h.want(coresvc.StateFailed, coresvc.SubFailed)
		if h.m.result != c.result {
			t.Errorf("%v: result %s, want %s", c.code, h.m.result, c.result)
		}
		h.wantReply(id, coresvc.CodeStartFailed)
		err := h.replies[id]
		if !errs.HasCode(err, c.code) || fieldOf(err, c.field) != c.value || fieldOf(err, "result") != c.result.String() {
			t.Errorf("%v: the start failure does not say the sandbox: %v (fields %v)", c.code, err, errs.FieldsOf(err))
		}
		wantStartFailedOutside(t, err, c.code)
	}
	h := newHarness(t, Config{ExecStart: []coresvc.CommandValue{cmd(t, "/bin/main")}})
	h.failSpawn[listStart] = true
	id := h.start()
	if h.m.result != coresvc.ResultResources || errs.HasCode(h.replies[id], coreproc.CodeSandboxFailed) {
		t.Errorf("another spawn failure changed: result %s, %v", h.m.result, h.replies[id])
	}
}

// wantStartFailedOutside requires a sandbox's start failure to be
// START_FAILED from outside — errors.Is, its code, its reason — with the
// sandbox's failure inside: errors.Is and errs.HasCode find both.
func wantStartFailedOutside(t *testing.T, err error, cause errs.Code) {
	t.Helper()
	outer, ok := errors.AsType[*errs.Error](err)
	if !ok || outer.Code() != coresvc.CodeStartFailed || outer.Reason() != "START_FAILED" {
		t.Errorf("%v: the start's answer is not START_FAILED outside: %v", cause, err)
	}
	if code, _ := errs.CodeOf(err); code != coresvc.CodeStartFailed {
		t.Errorf("%v: CodeOf = %v, want START_FAILED's", cause, code)
	}
	if !errors.Is(err, coresvc.StartFailed) || !errs.HasCode(err, coresvc.CodeStartFailed) {
		t.Errorf("%v: errors.Is(err, StartFailed) false: %v", cause, err)
	}
	if !errors.Is(err, sandboxSentinel(cause)) || !errs.HasCode(err, cause) {
		t.Errorf("%v: the sandbox's failure is not inside: %v", cause, err)
	}
}

// TestSandboxFailureAgainstTable1: the result a sandbox's failure ends a run
// with decides Restart= as Table 1 reads it — exit-code restarts on-failure
// but not on-abnormal, resources both —, and a later start that runs clears
// the cause.
func TestSandboxFailureAgainstTable1(t *testing.T) {
	sandboxHere(t)
	cases := []struct {
		code    errs.Code
		policy  coresvc.Restart
		restart bool
	}{
		{coreproc.CodeSandboxFailed, coresvc.RestartOnFailure, true},
		{coreproc.CodeSandboxFailed, coresvc.RestartOnAbnormal, false},
		{coreproc.CodeSandboxUnavailable, coresvc.RestartOnFailure, true},
		{coreproc.CodeSandboxUnavailable, coresvc.RestartOnAbnormal, true},
	}
	for _, c := range cases {
		h := newHarness(t, Config{Restart: c.policy, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/main")}, Spec: coreproc.Spec{Sandbox: templateSandbox()}})
		h.failSpawn[listStart], h.spawnErr = true, sandboxError(c.code)
		id := h.start()
		h.wantReply(id, c.code)
		if got := h.m.sub == coresvc.SubAutoRestart; got != c.restart {
			t.Errorf("%v under %s: at %s, restart %v", c.code, c.policy, h.m.sub, c.restart)
		}
	}
	h := newHarness(t, Config{Restart: coresvc.RestartOnFailure, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/main")}, Spec: coreproc.Spec{Sandbox: templateSandbox()}})
	h.failSpawn[listStart], h.spawnErr = true, sandboxError(coreproc.CodeSandboxFailed)
	h.start()
	h.failSpawn[listStart] = false
	h.advance(h.deadline(timerState))
	h.want(coresvc.StateActive, coresvc.SubRunning)
	if h.m.sandboxCause != 0 || h.m.sandboxFields != nil {
		t.Errorf("a start that ran kept the last one's cause: %v %v", h.m.sandboxCause, h.m.sandboxFields)
	}
}
