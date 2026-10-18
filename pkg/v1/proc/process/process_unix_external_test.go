//go:build unix

// Package process_test — the live spawn. Start/Stop/Wait need a real child, so
// the round trip is gated on the same `unix` tag as the implementation rather
// than on a runtime.GOOS guess; the non-Unix contract lives in
// process_other_test.go.
package process_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/proc/process"
)

// shPath is the shell used by the live-spawn facade test. POSIX requires it at
// this path, so an absent /bin/sh is a broken host rather than a reason to
// silently pass.
const shPath = "/bin/sh"

// TestStartAndStop exercises the full facade path: spawn, group-stop, and Wait.
func TestStartAndStop(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		script  string
		setpgid bool
		signal  process.Signal
	}
	tests := []tc{
		{
			// A shell that backgrounds and waits is the case Setpgid exists
			// for: signalling the leader alone would leave the sleep running.
			"a process group stopped by SIGTERM",
			"sleep 30 & wait", true, process.SIGTERM,
		},
		{"a single process stopped by SIGTERM", "sleep 30", false, process.SIGTERM},
		{
			// SIGKILL cannot be caught, so it exercises the path where the
			// grace window never matters.
			"a process group killed outright",
			"sleep 30 & wait", true, process.SIGKILL,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		p, err := process.Start(t.Context(), process.Spec{
			Path:    shPath,
			Args:    []string{"sh", "-c", c.script},
			Setpgid: c.setpgid,
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		//: the handle must report a positive leader pid.
		if p.PID() <= 0 {
			t.Fatalf("PID() = %d, want > 0", p.PID())
		}
		//: a graceful group stop must succeed within the grace window.
		if sErr := p.Stop(t.Context(), 2*time.Second, c.signal); sErr != nil {
			t.Fatalf("Stop: %v", sErr)
		}
		//: Wait must reap the stopped group without a host fault.
		if _, wErr := p.Wait(); wErr != nil {
			t.Fatalf("Wait: %v", wErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestCaptureAndBareNamesThroughTheFacade is what a consumer outside the SDK
// module writes, with the public names alone: a child's stdout captured into a
// buffer through process.StdioCapture, started by a BARE name. It pins where
// the name is searched — the parent's PATH when Spec.Env carries none, the
// child's own when it does — by running a program that exists only in a
// directory named in Spec.Env, and failing to find it without that PATH.
func TestCaptureAndBareNamesThroughTheFacade(t *testing.T) {
	t.Parallel()
	probeDir := t.TempDir()
	probe := filepath.Join(probeDir, "kprobe-facade")
	//: echo, not printf: the child's PATH is probeDir alone, and printf is
	//: a program on OpenBSD's ksh (exit 127 there), where echo is a builtin
	//: of every sh.
	writeExecutable(t, probe, "#!/bin/sh\necho from-spec-env\n")
	type tc struct {
		name    string
		spec    process.Spec
		want    string
		wantErr error
	}
	tests := []tc{
		{
			"sh by name, found in the parent's PATH (Spec.Env is nil)",
			process.Spec{Path: "sh", Args: []string{"sh", "-c", "printf captured"}},
			"captured", nil,
		},
		{
			"a name found only through Spec.Env's PATH",
			process.Spec{Path: "kprobe-facade", Env: []string{"PATH=" + probeDir}},
			"from-spec-env\n", nil,
		},
		{
			"the same name without that PATH is not found",
			process.Spec{Path: "kprobe-facade"},
			"", exec.ErrNotFound,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var stdout bytes.Buffer
		p, err := process.Start(t.Context(), withStdio(c.spec, process.StdioCapture, &stdout))
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("%s: Start = %v, want %v", c.name, err, c.wantErr)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: Start: %v", c.name, err)
		}
		exit, err := p.Wait()
		if err != nil || exit.Code != 0 {
			t.Fatalf("%s: Wait = (%+v, %v)", c.name, exit, err)
		}
		//: Wait returns only after every captured byte reached the writer.
		if stdout.String() != c.want {
			t.Errorf("%s: captured %q, want %q", c.name, stdout.String(), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// writeExecutable writes a program this test binary then executes, under
// syscall.ForkLock.
//
// Without the lock the case that runs it failed about one run in twenty
// (issue #269, 15 of 300 here) with SPAWN_FAILED wrapping ETXTBSY: execve
// refuses a file any process holds open for WRITING, and a sibling test
// forking while os.WriteFile had the probe open gave its child a copy of that
// descriptor until the child's own exec closed it — close-on-exec acts at
// the exec, not at the fork (golang/go#22315). Nothing was wrong with the
// runner's PATH, the name's resolution through Spec.Env or the spawn: the
// SDK reports the kernel's refusal as it should. Only a fork of THIS process
// can inherit the descriptor, and every one takes syscall.ForkLock for
// writing — the lock's own documentation prescribes the read side around a
// descriptor's life — so none can happen while the file is open. os/exec's
// own test retries on ETXTBSY instead; the lock removes the race rather than
// waiting it out.
func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	syscall.ForkLock.RLock()
	err := os.WriteFile(path, []byte(content), 0o755)
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// withStdio returns spec wired to capture its standard output into out under
// mode — spelled with the public StdioMode type, which is the point.
func withStdio(spec process.Spec, mode process.StdioMode, out *bytes.Buffer) process.Spec {
	spec.Stdio, spec.Stdout = mode, out
	return spec
}
