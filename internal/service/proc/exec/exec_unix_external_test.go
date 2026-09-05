//go:build unix

// Package exec_test — the spawn entry point as a caller reaches it.
package exec_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// shellPath is the POSIX shell every live case spawns.
const shellPath string = "/bin/sh"

// TestStart pins the guard ordering at the top of the spawn: the context, the
// spec, the limits and the cgroup path are all checked BEFORE any OS work, so a
// misconfigured Start costs nothing and leaves nothing behind.
func TestStart(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		spec      coreproc.Spec
		cancelled bool
		wantCode  errs.Code
	}
	//: a directory that exists and is definitively not a control group.
	notACgroup := filepath.Join(t.TempDir(), "not-a-cgroup")
	if err := os.Mkdir(notACgroup, 0o755); err != nil {
		t.Fatalf("creating the fixture: %v", err)
	}
	tests := []tc{
		{name: "a runnable spec", spec: coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", "exit 0"}}},
		{
			name:      "a cancelled context short-circuits",
			spec:      coreproc.Spec{Path: shellPath},
			cancelled: true,
		},
		{name: "an empty path", spec: coreproc.Spec{}, wantCode: coreproc.CodeInvalidSpec},
		{
			name: "an unmappable resource",
			spec: coreproc.Spec{Path: shellPath, Rlimits: map[coreproc.Resource]coreproc.LimitValue{
				coreproc.ResourceUnknown: {Soft: 1, Hard: 1},
			}},
			wantCode: coreproc.CodeUnknownResource,
		},
		{
			name:     "a cgroup path that is not a control group",
			spec:     coreproc.Spec{Path: shellPath, CgroupPath: notACgroup},
			wantCode: coreproc.CodeCgroupUnavailable,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx := t.Context()
		if c.cancelled {
			cancelled, cancel := contextCancelled(t)
			defer cancel()
			ctx = cancelled
		}

		proc, err := svcexec.Start(ctx, c.spec)

		if c.cancelled {
			//: the cancellation is surfaced verbatim, not relabelled: a caller
			//: shutting down needs to recognise its own context error.
			if err == nil {
				t.Fatal("Start on a cancelled context = nil, want the cancellation")
			}
			if proc != nil {
				t.Error("Start returned a process on a cancelled context")
			}
			return
		}
		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("Start(%s) = %v, want code %v", c.name, err, c.wantCode)
			}
			if proc != nil {
				t.Error("Start returned a process beside the error")
			}
			return
		}
		if err != nil {
			t.Fatalf("Start(%s) = %v, want nil", c.name, err)
		}
		if proc == nil {
			t.Fatal("Start returned no process and no error")
		}
		if _, werr := proc.Wait(); werr != nil {
			t.Errorf("Wait = %v, want nil", werr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// contextCancelled returns a context that is already cancelled.
func contextCancelled(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx, cancel
}

// TestStartLimitsHonoured proves the trampoline actually applies what it
// encodes, by reading the limits back from inside the child.
//
// Every other test of this path checks that the payload was BUILT correctly.
// This one checks that it took effect, which is the only assertion a caller
// cares about: a limit that was encoded, transported and then silently ignored
// looks exactly like one that was applied.
func TestStartLimitsHonoured(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the shell builtin that prints the effective limit.
		probe  string
		mutate func(spec *coreproc.Spec)
		want   int64
		//: the base the probe prints in; umask is octal, ulimit decimal.
		base int
	}
	tests := []tc{
		{
			name:  "the soft open-file ceiling",
			probe: "ulimit -n",
			mutate: func(spec *coreproc.Spec) {
				//: well below any default, so the readback is unambiguous.
				spec.Rlimits = map[coreproc.Resource]coreproc.LimitValue{
					coreproc.ResourceNoFile: {Soft: 48, Hard: 48},
				}
			},
			want: 48,
			base: 10,
		},
		{
			name:  "core dumps disabled",
			probe: "ulimit -c",
			mutate: func(spec *coreproc.Spec) {
				spec.Rlimits = map[coreproc.Resource]coreproc.LimitValue{
					coreproc.ResourceCore: {Soft: 0, Hard: 0},
				}
			},
			want: 0,
			base: 10,
		},
		{
			name:  "the file-creation mask",
			probe: "umask",
			mutate: func(spec *coreproc.Spec) {
				//: the shell prints the umask in octal.
				spec.Umask = new(0o077)
			},
			want: 0o077,
			base: 8,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var out strings.Builder
		spec := coreproc.Spec{
			Path:   shellPath,
			Args:   []string{"sh", "-c", c.probe},
			Stdio:  coreproc.StdioCapture,
			Stdout: &out,
		}
		c.mutate(&spec)

		p, err := svcexec.Start(t.Context(), spec)
		if err != nil {
			t.Fatalf("Start(%s) = %v, want nil", c.name, err)
		}
		exit, wErr := p.Wait()
		if wErr != nil {
			t.Fatalf("Wait = %v, want nil", wErr)
		}
		//: the probe shell must itself exit cleanly for its output to mean
		//: anything.
		if !exit.Success() {
			t.Fatalf("the probe shell exited %d", exit.Code)
		}

		got := strings.TrimSpace(out.String())
		gotN, pErr := strconv.ParseInt(got, c.base, 64)
		if pErr != nil {
			t.Fatalf("the probe printed %q, which is not base %d: %v", got, c.base, pErr)
		}
		if gotN != c.want {
			t.Errorf("%q printed %q (%d), want %d", c.probe, got, gotN, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestStartTrampolineFailuresAreTyped pins the gap the handshake pipe closes.
//
// A trampoline child that cannot apply a limit, or cannot execve the target,
// exits with SOME status — and without the pipe Start could not tell that apart
// from the real target exiting with the same one. Each failure must therefore
// arrive as the sentinel the direct-spawn path would have produced.
func TestStartTrampolineFailuresAreTyped(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		spec coreproc.Spec
		want errs.Code
		//: FreeBSD and DragonFly CLAMP a soft ceiling above the hard one rather
		//: than refusing it, so that particular input cannot produce an apply
		//: failure there; the case then asserts the spawn simply succeeds.
		clampingPlatform bool
	}
	tests := []tc{
		{
			name: "a limit the kernel refuses",
			spec: coreproc.Spec{
				Path: shellPath,
				Args: []string{"sh", "-c", "true"},
				//: a soft ceiling above the hard one is EINVAL on Linux, Darwin,
				//: NetBSD and OpenBSD.
				Rlimits: map[coreproc.Resource]coreproc.LimitValue{
					coreproc.ResourceNoFile: {Soft: 100, Hard: 50},
				},
			},
			want:             coreproc.CodeRlimitFailed,
			clampingPlatform: runtime.GOOS == "freebsd" || runtime.GOOS == "dragonfly",
		},
		{
			//: the umask routes the spawn through the trampoline; the bad path
			//: makes its execve fail AFTER the umask has been applied.
			name: "a target the trampoline cannot exec",
			spec: coreproc.Spec{Path: "/nonexistent/kitsunium-proc-exec-test", Umask: new(0o022)},
			want: coreproc.CodeSpawnFailed,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		p, err := svcexec.Start(t.Context(), c.spec)
		if c.clampingPlatform {
			//: the kernel clamped instead of refusing, so the spawn succeeds —
			//: which is the platform's behaviour, not a contract violation.
			if err != nil {
				t.Fatalf("Start on a clamping platform = %v, want nil", err)
			}
			if _, werr := p.Wait(); werr != nil {
				t.Errorf("Wait = %v, want nil", werr)
			}
			return
		}
		if !errs.HasCode(err, c.want) {
			t.Fatalf("Start(%s) = %v, want code %v", c.name, err, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestStartExtraFilesSurviveTrampoline pins the fd ordering that the trampoline
// made load-bearing.
//
// Spec.ExtraFiles must land at fd 3.. — that is the socket-activation contract —
// so the handshake pipe is appended AFTER them. If the pipe took fd 3 instead,
// the child's `cat <&3` would read the parent-closed pipe and see EOF, and a
// socket-activated service would find no socket where its protocol says one is.
func TestStartExtraFilesSurviveTrampoline(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		payload string
		//: how many ExtraFiles precede the one being read back.
		leading int
	}
	tests := []tc{
		{"a single extra file", "fd3-payload", 0},
		{"the first of several", "fd3-payload", 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r, w, pipeErr := os.Pipe()
		if pipeErr != nil {
			t.Fatalf("os.Pipe = %v", pipeErr)
		}
		//: the payload fits the pipe buffer, so the write cannot block; closing
		//: then gives the child's `cat <&3` its EOF with no goroutine involved.
		if _, wErr := w.WriteString(c.payload); wErr != nil {
			t.Fatalf("writing the payload: %v", wErr)
		}
		if wcErr := w.Close(); wcErr != nil {
			t.Fatalf("closing the payload writer: %v", wcErr)
		}

		extras := []*os.File{r}
		for range c.leading {
			//: pad with further descriptors; fd 3 must still be the first one.
			extra, _, perr := os.Pipe()
			if perr != nil {
				t.Fatalf("os.Pipe = %v", perr)
			}
			defer func() {
				if cerr := extra.Close(); cerr != nil {
					t.Logf("closing a padding descriptor: %v", cerr)
				}
			}()
			extras = append(extras, extra)
		}

		var out strings.Builder
		spec := coreproc.Spec{
			Path: shellPath,
			//: read everything from fd 3 — the FIRST ExtraFile — and echo it.
			Args: []string{"sh", "-c", "cat <&3"},
			//: a umask forces the spawn through the trampoline and its pipe.
			Umask:      new(0o022),
			ExtraFiles: extras,
			Stdio:      coreproc.StdioCapture,
			Stdout:     &out,
		}

		proc, err := svcexec.Start(t.Context(), spec)
		//: the child inherited its own dup; the parent's copy goes now.
		if rcErr := r.Close(); rcErr != nil {
			t.Fatalf("closing the parent's read end: %v", rcErr)
		}
		if err != nil {
			t.Fatalf("Start = %v, want nil", err)
		}
		//: Wait joins the capture copier, so out holds the child's full stdout.
		if _, werr := proc.Wait(); werr != nil {
			t.Fatalf("Wait = %v, want nil", werr)
		}

		if got := strings.TrimSpace(out.String()); got != c.payload {
			t.Errorf("the child read %q from fd 3, want %q — the handshake pipe shifted the extras",
				got, c.payload)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestStartEmptyEnvNoLeak pins that a nil Spec.Env spawns an EMPTY environment.
//
// os.StartProcess inherits the parent's environment when Env is nil, so this is
// the difference between a child that sees nothing and one that sees every
// secret the supervisor holds. The canary below is set in the supervisor and
// must not reach the child.
func TestStartEmptyEnvNoLeak(t *testing.T) {
	//: not parallel — it sets a variable in the process environment.
	type tc struct {
		name string
		env  []string
		//: the shell test that must succeed inside the child.
		probe string
	}
	tests := []tc{
		{"a nil environment", nil, `[ -z "$PROC_ENV_LEAK_CANARY" ]`},
		{"an empty environment", []string{}, `[ -z "$PROC_ENV_LEAK_CANARY" ]`},
		//: an explicit environment carries exactly what was asked for, and
		//: still nothing else.
		{"an explicit environment", []string{"MINE=yes"}, `[ "$MINE" = yes ] && [ -z "$PROC_ENV_LEAK_CANARY" ]`},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		spec := coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", c.probe}, Env: c.env}

		p, err := svcexec.Start(t.Context(), spec)
		if err != nil {
			t.Fatalf("Start(%s) = %v, want nil", c.name, err)
		}
		exit, wErr := p.Wait()
		if wErr != nil {
			t.Fatalf("Wait = %v, want nil", wErr)
		}
		if exit.Code != 0 {
			t.Errorf("the child exited %d — the supervisor's environment leaked", exit.Code)
		}
	}
	t.Setenv("PROC_ENV_LEAK_CANARY", "leaked")
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestStdio pins the three modes end to end, through a real child.
//
// Capture is the one with a contract worth stating: Wait returns only after
// every byte the child wrote has reached the caller's writer. Anything less and
// a test that reads the buffer right after Wait would see a truncated capture
// that varies with scheduling.
func TestStdio(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the shell program the child runs.
		program string
		stdin   string
		//: what the captured stdout and stderr must hold.
		wantOut string
		wantErr string
		//: when true the writers are left nil, so both streams are discarded.
		discard bool
		mode    coreproc.StdioMode
	}
	tests := []tc{
		{
			name:    "the two output streams stay separate",
			program: "echo to-stdout; echo to-stderr >&2",
			wantOut: "to-stdout\n",
			wantErr: "to-stderr\n",
			mode:    coreproc.StdioCapture,
		},
		{
			//: far past a pipe buffer, so the drain must actually loop rather
			//: than happening to fit in one read.
			name:    "output larger than a pipe buffer",
			program: "i=0; while [ $i -lt 2000 ]; do echo 0123456789012345678901234567890123456789; i=$((i+1)); done",
			wantOut: strings.Repeat("0123456789012345678901234567890123456789\n", 2000),
			mode:    coreproc.StdioCapture,
		},
		{
			name:    "stdin reaches the child",
			program: "cat",
			stdin:   "fed to the child\n",
			wantOut: "fed to the child\n",
			mode:    coreproc.StdioCapture,
		},
		{
			//: an empty stdin must still reach EOF, or `cat` never returns.
			name:    "an empty stdin still reaches EOF",
			program: "cat",
			stdin:   "",
			mode:    coreproc.StdioCapture,
		},
		{
			name:    "a nil writer discards without failing",
			program: "echo discarded; echo also-discarded >&2",
			discard: true,
			mode:    coreproc.StdioCapture,
		},
		{
			name:    "null mode discards everything",
			program: "echo discarded; echo also-discarded >&2",
			discard: true,
			mode:    coreproc.StdioNull,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var out, errOut strings.Builder
		spec := coreproc.Spec{
			Path:  shellPath,
			Args:  []string{"sh", "-c", c.program},
			Stdio: c.mode,
		}
		if !c.discard {
			spec.Stdout = &out
			spec.Stderr = &errOut
		}
		if c.stdin != "" || c.program == "cat" {
			spec.Stdin = strings.NewReader(c.stdin)
		}

		p, err := svcexec.Start(t.Context(), spec)
		if err != nil {
			t.Fatalf("Start(%s) = %v, want nil", c.name, err)
		}
		exit, wErr := p.Wait()
		if wErr != nil {
			t.Fatalf("Wait = %v, want nil", wErr)
		}
		if !exit.Success() {
			t.Fatalf("the child exited %d", exit.Code)
		}

		if c.discard {
			//: nothing may have reached the writers, since none were given.
			if out.Len() != 0 || errOut.Len() != 0 {
				t.Errorf("a discarded stream still produced output")
			}
			return
		}
		//: Wait has joined the copiers, so the buffers are complete right now —
		//: no polling, no sleep.
		if out.String() != c.wantOut {
			t.Errorf("stdout held %d bytes, want %d", out.Len(), len(c.wantOut))
		}
		if errOut.String() != c.wantErr {
			t.Errorf("stderr held %q, want %q", errOut.String(), c.wantErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestStdioCaptureWriterFailureSurfaces pins that a caller's failing sink is
// reported rather than silently truncating the capture.
//
// It is the ONLY signal available. Once the drain gives up it closes the pipe,
// so the child usually dies of SIGPIPE mid-write — and a caller reading only the
// exit status would see a signalled death and conclude the child crashed, when
// what actually happened is that their own writer refused the output.
func TestStdioCaptureWriterFailureSurfaces(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		program string
		accept  int
	}
	tests := []tc{
		{"a sink that fails immediately", "echo hello", 0},
		{"a sink that fails part way", "i=0; while [ $i -lt 500 ]; do echo padding-line; i=$((i+1)); done", 16},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink := &boundedWriter{accept: c.accept, err: errors.New("the sink is full")}
		spec := coreproc.Spec{
			Path:   shellPath,
			Args:   []string{"sh", "-c", c.program},
			Stdio:  coreproc.StdioCapture,
			Stdout: sink,
		}

		p, err := svcexec.Start(t.Context(), spec)
		if err != nil {
			t.Fatalf("Start = %v, want nil", err)
		}
		exit, wErr := p.Wait()

		//: the failing sink is reported, typed.
		if !errs.HasCode(wErr, coreproc.CodeStdioCaptureFailed) {
			t.Fatalf("Wait = %v, want STDIO_CAPTURE_FAILED", wErr)
		}
		//: the ExitValue still comes back beside it, so a caller has both the
		//: capture failure and whatever the child did — a clean exit, or the
		//: SIGPIPE the closed drain caused.
		if exit.Code == 0 && exit.Signaled {
			t.Errorf("the exit value is inconsistent: code 0 and signalled")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// boundedWriter accepts a fixed number of bytes and then fails, standing in for
// a caller sink that fills up mid-capture.
type boundedWriter struct {
	mu     sync.Mutex
	accept int
	got    int
	err    error
}

// Write accepts up to accept bytes in total, then reports the failure.
func (w *boundedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	room := w.accept - w.got
	//: no room left: every further write fails.
	if room <= 0 {
		return 0, w.err
	}
	//: a partial write is what a filling sink does, and io.Copy treats the
	//: accompanying error as fatal.
	if len(p) > room {
		w.got = w.accept
		return room, w.err
	}
	w.got += len(p)
	return len(p), nil
}
