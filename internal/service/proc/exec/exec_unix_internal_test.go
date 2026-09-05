//go:build unix

// Package exec — white-box tests for the Unix spawn assembly. Every helper here
// builds one part of what os.StartProcess receives, and each has a default that
// is load-bearing rather than convenient.
package exec

import (
	"os"
	"slices"
	"strconv"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// shellPath is the POSIX shell used where a real child is needed.
const shellPath string = "/bin/sh"

// Test_buildArgv pins the argv[0] default. A child started with an empty argv
// gets an empty argv[0], which breaks ps output, breaks anything that
// re-executes itself by name, and confuses every log line the child writes.
func Test_buildArgv(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		spec coreproc.Spec
		want []string
	}
	tests := []tc{
		{"no args defaults to the path", coreproc.Spec{Path: "/bin/true"}, []string{"/bin/true"}},
		{"an empty arg slice defaults too", coreproc.Spec{Path: "/bin/true", Args: []string{}}, []string{"/bin/true"}},
		{
			//: a caller-supplied argv is used verbatim, argv[0] included —
			//: setting a different argv[0] is a legitimate thing to want.
			"a full argv is used verbatim",
			coreproc.Spec{Path: "/bin/sh", Args: []string{"login-shell", "-c", "true"}},
			[]string{"login-shell", "-c", "true"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := buildArgv(c.spec)
		if !slices.Equal(got, c.want) {
			t.Errorf("buildArgv(%s) = %v, want %v", c.name, got, c.want)
		}
		//: an empty argv would leave the child with no argv[0] at all.
		if len(got) == 0 {
			t.Errorf("buildArgv(%s) produced an empty argv", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_appendExtraFiles pins the fd numbering socket activation depends on: the
// three std streams, then the caller's extras at fd 3+. It must not alias either
// input, because the caller's Spec is documented as immutable and the std slice
// is reused across the spawn.
func Test_appendExtraFiles(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		extra int
	}
	tests := []tc{
		{"no extras", 0},
		{"one extra", 1},
		{"several extras", 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		std := []*os.File{os.Stdin, os.Stdout, os.Stderr}
		extra := make([]*os.File, c.extra)
		for i := range extra {
			extra[i] = os.Stdout
		}
		stdBefore := slices.Clone(std)

		got := appendExtraFiles(std, extra)

		if len(got) != len(std)+c.extra {
			t.Fatalf("the table has %d entries, want %d", len(got), len(std)+c.extra)
		}
		//: fd 0-2 stay the std streams whatever else is inherited.
		for i := range std {
			if got[i] != stdBefore[i] {
				t.Errorf("fd %d is not the std stream it was", i)
			}
		}
		//: the extras land at fd 3+, which is what sd_listen_fds(3) reads.
		for i := range extra {
			if got[len(std)+i] != extra[i] {
				t.Errorf("extra %d did not land at fd %d", i, len(std)+i)
			}
		}
		//: neither input may be mutated.
		if !slices.Equal(std, stdBefore) {
			t.Error("appendExtraFiles mutated the std slice")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_childFileTable pins the ordering decision that makes socket activation
// and the trampoline handshake coexist.
//
// The extras MUST keep fd 3.., because that is the contract sd_listen_fds(3)
// reads. So the handshake pipe goes LAST, and its real descriptor number travels
// to the child in an environment variable — a fixed fd 3 would silently shadow
// the first activation socket.
func Test_childFileTable(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		extra      int
		withPipe   bool
		wantAddon  bool
		wantHSAtFD int
	}
	tests := []tc{
		{name: "no extras and no pipe"},
		{name: "extras and no pipe", extra: 2},
		{name: "a pipe and no extras", withPipe: true, wantAddon: true, wantHSAtFD: 3},
		//: the case the addon exists for: the pipe lands past the extras.
		{name: "a pipe behind two extras", extra: 2, withPipe: true, wantAddon: true, wantHSAtFD: 5},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		stdio := []*os.File{os.Stdin, os.Stdout, os.Stderr}
		extra := make([]*os.File, c.extra)
		for i := range extra {
			extra[i] = os.Stdout
		}
		var hs *handshake
		if c.withPipe {
			created, err := newHandshake()
			if err != nil {
				t.Fatalf("newHandshake = %v, want nil", err)
			}
			defer created.closeBoth()
			hs = created
		}

		files, addon := childFileTable(stdio, extra, hs)

		want := len(stdio) + c.extra
		if c.withPipe {
			want++
		}
		if len(files) != want {
			t.Fatalf("the table has %d entries, want %d", len(files), want)
		}
		//: the extras keep fd 3.. whether or not a pipe follows them.
		for i := range extra {
			if files[len(stdio)+i] != extra[i] {
				t.Errorf("extra %d did not land at fd %d", i, len(stdio)+i)
			}
		}
		if !c.wantAddon {
			if len(addon) != 0 {
				t.Errorf("a direct spawn produced the env addon %v", addon)
			}
			return
		}
		//: the pipe is last, and the addon names exactly that descriptor.
		if files[len(files)-1] != hs.childFile() {
			t.Error("the handshake pipe is not the last entry")
		}
		wantEntry := trampolineHsFdEnv + "=" + strconv.Itoa(c.wantHSAtFD)
		if len(addon) != 1 || addon[0] != wantEntry {
			t.Errorf("the addon is %v, want [%s]", addon, wantEntry)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_resolveSpawn pins the routing between a direct spawn and the trampoline.
//
// The trampoline exists because Go exposes no hook to run setrlimit(2) or
// umask(2) in the child between fork and exec. Taking it when it is not needed
// costs an extra exec on every spawn; skipping it when it IS needed silently
// drops the limits, so the predicate has to be exact in both directions.
func Test_resolveSpawn(t *testing.T) {
	t.Parallel()
	umask := 0o027

	type tc struct {
		name        string
		spec        coreproc.Spec
		wantSelf    bool
		wantEnvAddi bool
	}
	tests := []tc{
		{name: "a plain spec spawns the target", spec: coreproc.Spec{Path: "/bin/true"}},
		{
			name: "an empty rlimit map is still a plain spec",
			spec: coreproc.Spec{Path: "/bin/true", Rlimits: map[coreproc.Resource]coreproc.LimitValue{}},
		},
		{
			name:        "a umask needs the trampoline",
			spec:        coreproc.Spec{Path: "/bin/true", Umask: &umask},
			wantSelf:    true,
			wantEnvAddi: true,
		},
		{
			name: "an rlimit needs the trampoline",
			spec: coreproc.Spec{Path: "/bin/true", Rlimits: map[coreproc.Resource]coreproc.LimitValue{
				coreproc.ResourceNoFile: {Soft: 64, Hard: 64},
			}},
			wantSelf:    true,
			wantEnvAddi: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		path, argv, addon, err := resolveSpawn(c.spec)
		if err != nil {
			t.Fatalf("resolveSpawn(%s) = %v, want nil", c.name, err)
		}
		if !c.wantSelf {
			//: a direct spawn runs the target with its own argv and nothing
			//: added to the environment.
			if path != c.spec.Path {
				t.Errorf("path = %q, want the target %q", path, c.spec.Path)
			}
			if len(addon) != 0 {
				t.Errorf("a direct spawn produced the env addon %v", addon)
			}
			return
		}
		//: a trampolined spawn re-execs THIS binary, with the target as argv[1]
		//: so the trampoline knows what to exec once the limits are on.
		self, serr := os.Executable()
		if serr != nil {
			t.Fatalf("resolving the test binary: %v", serr)
		}
		if path != self {
			t.Errorf("path = %q, want this binary %q", path, self)
		}
		if len(argv) < trampolineMinArgs || argv[1] != c.spec.Path {
			t.Errorf("argv = %v, want the target at index 1", argv)
		}
		if c.wantEnvAddi && len(addon) == 0 {
			t.Error("a trampolined spawn produced no environment addon")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_buildProcAttr pins the environment guarantee, which is the one thing here
// that is a security property rather than a convenience.
//
// os.StartProcess inherits the PARENT's environment when Env is nil, so a child
// spawned from a Spec with no Env would receive every secret the supervisor
// holds. The attr therefore always carries a non-nil slice, even an empty one.
func Test_buildProcAttr(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		spec     coreproc.Spec
		addon    []string
		wantEnv  []string
		wantCode errs.Code
	}
	tests := []tc{
		{
			name:    "a nil environment becomes an empty one, never inherited",
			spec:    coreproc.Spec{Path: "/bin/true"},
			wantEnv: []string{},
		},
		{
			name:    "an explicit environment is carried",
			spec:    coreproc.Spec{Path: "/bin/true", Env: []string{"A=1", "B=2"}},
			wantEnv: []string{"A=1", "B=2"},
		},
		{
			name:    "the trampoline addon follows the caller's entries",
			spec:    coreproc.Spec{Path: "/bin/true", Env: []string{"A=1"}},
			addon:   []string{trampolineEnv + "=x"},
			wantEnv: []string{"A=1", trampolineEnv + "=x"},
		},
		{
			name:     "an unresolvable user aborts the assembly",
			spec:     coreproc.Spec{Path: "/bin/true", User: absentName},
			wantCode: coreproc.CodeUnknownUser,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: keep a copy of the caller's Env: building the attr must not mutate
		//: the Spec, which is documented immutable.
		specEnv := slices.Clone(c.spec.Env)
		files := []*os.File{os.Stdin, os.Stdout, os.Stderr}

		attr, err := buildProcAttr(c.spec, files, c.addon)

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("buildProcAttr(%s) = %v, want code %v", c.name, err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("buildProcAttr(%s) = %v, want nil", c.name, err)
		}
		//: nil would make os.StartProcess inherit the supervisor's environment.
		if attr.Env == nil {
			t.Fatal("the attr carries a nil Env, which inherits the parent's")
		}
		if !slices.Equal(attr.Env, c.wantEnv) {
			t.Errorf("Env = %v, want %v", attr.Env, c.wantEnv)
		}
		if !slices.Equal(c.spec.Env, specEnv) {
			t.Errorf("buildProcAttr mutated the caller's Env: %v", c.spec.Env)
		}
		if attr.Dir != c.spec.Dir {
			t.Errorf("Dir = %q, want %q", attr.Dir, c.spec.Dir)
		}
		sys := attr.Sys
		if sys == nil {
			t.Fatal("the attr carries no SysProcAttr, so the topology is lost")
		}
		if sys.Setpgid != c.spec.Setpgid || sys.Setsid != c.spec.Setsid {
			t.Errorf("topology = (setpgid %v, setsid %v), want (%v, %v)",
				sys.Setpgid, sys.Setsid, c.spec.Setpgid, c.spec.Setsid)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_spawnFiles pins that the handshake pipe is created only for a
// trampolined spawn. Creating one every time would burn two descriptors per
// spawn for nothing; never creating one would leave the trampoline unable to
// report, which is the gap the pipe exists to close.
func Test_spawnFiles(t *testing.T) {
	t.Parallel()
	umask := 0o027

	type tc struct {
		name     string
		spec     coreproc.Spec
		wantPipe bool
	}
	tests := []tc{
		{name: "a direct spawn", spec: coreproc.Spec{Path: "/bin/true"}},
		{name: "a trampolined spawn", spec: coreproc.Spec{Path: "/bin/true", Umask: &umask}, wantPipe: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sio, err := buildStdio(c.spec)
		if err != nil {
			t.Fatalf("buildStdio = %v, want nil", err)
		}
		defer sio.closeAll()

		files, hs, err := spawnFiles(c.spec, sio)
		if err != nil {
			t.Fatalf("spawnFiles(%s) = %v, want nil", c.name, err)
		}
		if hs != nil {
			defer hs.closeBoth()
		}
		//: the three std streams are always wired.
		if len(files) != len(sio.files) {
			t.Errorf("the file table has %d entries, want %d", len(files), len(sio.files))
		}
		if (hs != nil) != c.wantPipe {
			t.Errorf("a handshake pipe was created = %v, want %v", hs != nil, c.wantPipe)
		}
		//: the pipe is deliberately NOT in the table here; spawn places it
		//: after the caller's ExtraFiles.
		for _, f := range files {
			if hs != nil && f == hs.childFile() {
				t.Error("spawnFiles placed the handshake pipe in the table")
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_applyPostStart pins the ordering and the abort. Both attributes can only
// be set on a live pid, and the first refusal must stop the rest: a child that
// got its scheduling priority but not its OOM bias is half-configured, which is
// the state the caller least expects.
func Test_applyPostStart(t *testing.T) {
	t.Parallel()
	nice := 5
	badOOM := 5000

	type tc struct {
		name    string
		pid     int
		spec    coreproc.Spec
		wantErr bool
	}
	tests := []tc{
		{name: "nothing requested", pid: os.Getpid()},
		{name: "a niceness alone", pid: os.Getpid(), spec: coreproc.Spec{Nice: &nice}},
		{name: "a process that does not exist", pid: absentPID, spec: coreproc.Spec{Nice: &nice}, wantErr: true},
		{name: "an out-of-range OOM bias", pid: os.Getpid(), spec: coreproc.Spec{OOMScoreAdj: &badOOM}, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := applyPostStart(c.pid, c.spec)
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeRlimitFailed) {
				t.Fatalf("applyPostStart(%s) = %v, want RLIMIT_FAILED", c.name, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("applyPostStart(%s) = %v, want nil", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_spawn pins the fork/exec itself, including that a failure releases the
// stdio descriptors. A spawn that leaked two pipes per attempt would exhaust the
// supervisor's descriptor table under any retry loop.
func Test_spawn(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		spec    coreproc.Spec
		wantErr bool
	}
	dir := t.TempDir()
	tests := []tc{
		{name: "a real binary", spec: coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", "exit 0"}}},
		{name: "a path that does not exist", spec: coreproc.Spec{Path: "/definitely/not/here"}, wantErr: true},
		{name: "a directory as the target", spec: coreproc.Spec{Path: dir}, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sio, err := buildStdio(c.spec)
		if err != nil {
			t.Fatalf("buildStdio = %v, want nil", err)
		}

		started, serr := spawn(c.spec, sio)
		if c.wantErr {
			if !errs.HasCode(serr, coreproc.CodeSpawnFailed) {
				sio.closeAll()
				t.Fatalf("spawn(%s) = %v, want SPAWN_FAILED", c.name, serr)
			}
			//: a failed spawn must hand back no process at all.
			if started != nil {
				t.Errorf("spawn(%s) returned a process beside the error", c.name)
			}
			return
		}
		if serr != nil {
			sio.closeAll()
			t.Fatalf("spawn(%s) = %v, want nil", c.name, serr)
		}
		defer sio.closeAll()
		if started == nil {
			t.Fatal("spawn returned no process and no error")
		}
		if started.Pid <= 0 {
			t.Errorf("the spawned pid is %d, want a positive one", started.Pid)
		}
		//: reap it so the test leaves no zombie behind.
		if _, werr := started.Wait(); werr != nil {
			t.Logf("reaping the child: %v", werr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_teardown pins the cleanup a post-start failure runs: the child is killed
// and reaped, so a refused attribute never leaves a live process or a zombie.
func Test_teardown(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		setpgid bool
	}
	tests := []tc{
		{"a child in its own process group", true},
		{"a child in ours", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		spec := coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", "sleep 30"}, Setpgid: c.setpgid}
		sio, err := buildStdio(spec)
		if err != nil {
			t.Fatalf("buildStdio = %v, want nil", err)
		}
		defer sio.closeAll()
		started, serr := spawn(spec, sio)
		if serr != nil {
			t.Fatalf("spawn = %v, want nil", serr)
		}
		live := newHandle(started, c.setpgid, sio)

		teardown(live)

		//: the child is gone: signalling it now must report that, rather than
		//: reaching a process that outlived its supervisor.
		if err := live.Signal(coreproc.Signal(syscall.Signal(0))); err == nil {
			t.Error("the child survived teardown")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
