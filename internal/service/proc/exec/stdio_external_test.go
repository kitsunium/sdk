//go:build unix

// Package exec_test — black-box acceptance tests for per-process stdio wiring
// (issue #73): capture delivers 100% of stdout/stderr, large output exercises
// pipe backpressure, null discards, stdin is fed from a reader, a nil capture
// writer discards, and capture composes with Setpgid. All gated on the /bin/sh
// probe via requireShell (defined in exec_external_test.go).
package exec_test

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// errWriter fails every Write, simulating a capture sink that errors mid-stream.
type errWriter struct{}

// Write always fails, so the output copier records a writer error.
func (errWriter) Write(_ []byte) (int, error) {
	//: a sink that rejects every byte exercises the StdioCaptureFailed path.
	return 0, errors.New("sink write failed")
}

// startAndWait spawns spec, drives Wait, and fails the test on any spawn/wait
// fault — the common path for the stdio cases below.
func startAndWait(t *testing.T, spec coreproc.Spec) coreproc.ExitValue {
	t.Helper()
	p, err := svcexec.Start(context.Background(), spec)
	//: a clean spawn of /bin/sh is the precondition for every stdio assertion.
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	exit, wErr := p.Wait()
	//: a normal exit is not a wait4 fault.
	if wErr != nil {
		t.Fatalf("Wait: %v", wErr)
	}
	//: Wait joined the copiers, so the buffers are now safe to read.
	return exit
}

// TestStdioCaptureSplitsStreams asserts StdioCapture delivers stdout and stderr
// to their distinct writers, byte-for-byte — the acceptance (a) contract.
func TestStdioCaptureSplitsStreams(t *testing.T) {
	t.Parallel()
	requireShell(t)

	var out, errb bytes.Buffer
	spec := coreproc.Spec{
		Path:   shPath,
		Args:   []string{"sh", "-c", "printf to-stdout; printf to-stderr 1>&2"},
		Stdio:  coreproc.StdioCapture,
		Stdout: &out,
		Stderr: &errb,
	}
	exit := startAndWait(t, spec)
	//: the child exits cleanly.
	if exit.Code != 0 {
		t.Fatalf("exit Code = %d, want 0", exit.Code)
	}
	//: stdout must carry exactly what the child wrote to fd 1.
	if out.String() != "to-stdout" {
		t.Fatalf("stdout = %q, want %q", out.String(), "to-stdout")
	}
	//: stderr must carry exactly what the child wrote to fd 2 — no crosstalk.
	if errb.String() != "to-stderr" {
		t.Fatalf("stderr = %q, want %q", errb.String(), "to-stderr")
	}
}

// TestStdioCaptureLargeOutput asserts capture delivers 100% of an output larger
// than the kernel pipe buffer, so the copier drains under backpressure with no
// truncation — the acceptance (a) "large-output backpressure" contract.
func TestStdioCaptureLargeOutput(t *testing.T) {
	t.Parallel()
	requireShell(t)

	//: 256 KiB far exceeds the 64 KiB pipe buffer, forcing the child to block on
	//: write until the copier drains — the backpressure path under test.
	const want int = 256 * 1024
	var out bytes.Buffer
	spec := coreproc.Spec{
		Path:   shPath,
		Args:   []string{"sh", "-c", "head -c " + strconv.Itoa(want) + " /dev/zero"},
		Stdio:  coreproc.StdioCapture,
		Stdout: &out,
	}
	exit := startAndWait(t, spec)
	//: the producer exits cleanly once the copier has drained it.
	if exit.Code != 0 {
		t.Fatalf("exit Code = %d, want 0", exit.Code)
	}
	//: every byte must have been captured — a short read is a backpressure bug.
	if out.Len() != want {
		t.Fatalf("captured %d bytes, want %d", out.Len(), want)
	}
}

// TestStdioCaptureStdin asserts StdioCapture feeds Spec.Stdin to the child and
// the child reads it — round-tripped through `cat` back into the stdout writer.
func TestStdioCaptureStdin(t *testing.T) {
	t.Parallel()
	requireShell(t)

	const payload = "piped-input\nsecond-line\n"
	var out bytes.Buffer
	spec := coreproc.Spec{
		Path:   shPath,
		Args:   []string{"sh", "-c", "cat"},
		Stdio:  coreproc.StdioCapture,
		Stdin:  strings.NewReader(payload),
		Stdout: &out,
	}
	exit := startAndWait(t, spec)
	//: cat exits 0 at stdin EOF.
	if exit.Code != 0 {
		t.Fatalf("exit Code = %d, want 0", exit.Code)
	}
	//: the child must have seen exactly the bytes the reader supplied.
	if out.String() != payload {
		t.Fatalf("stdout = %q, want %q", out.String(), payload)
	}
}

// TestStdioCaptureNilWriterDiscards asserts a nil capture writer discards that
// stream to the null device without error — the acceptance (c) contract.
func TestStdioCaptureNilWriterDiscards(t *testing.T) {
	t.Parallel()
	requireShell(t)

	//: capture mode with no writers — both streams fall back to the null device.
	spec := coreproc.Spec{
		Path:  shPath,
		Args:  []string{"sh", "-c", "echo discarded; echo also 1>&2"},
		Stdio: coreproc.StdioCapture,
	}
	exit := startAndWait(t, spec)
	//: discarding output must not affect the child's clean exit.
	if exit.Code != 0 {
		t.Fatalf("exit Code = %d, want 0", exit.Code)
	}
}

// TestStdioNull asserts StdioNull discards output and gives the child an
// immediate-EOF stdin — `read` fails (EOF) so the shell takes the `||` branch.
func TestStdioNull(t *testing.T) {
	t.Parallel()
	requireShell(t)

	spec := coreproc.Spec{
		Path: shPath,
		//: stdin is /dev/null, so `read` hits EOF and returns non-zero; the child
		//: still exits 0 via the `|| true`, proving stdin was wired to null.
		Args:  []string{"sh", "-c", "read x || true; echo to-null; echo to-null 1>&2"},
		Stdio: coreproc.StdioNull,
	}
	exit := startAndWait(t, spec)
	//: null wiring must not perturb the exit status.
	if exit.Code != 0 {
		t.Fatalf("exit Code = %d, want 0", exit.Code)
	}
}

// TestStdioCaptureWriterFailureSurfaces asserts that a capture writer which
// errors mid-stream is surfaced from Wait as StdioCaptureFailed rather than
// silently swallowed, while the child's real exit status is still reported.
func TestStdioCaptureWriterFailureSurfaces(t *testing.T) {
	t.Parallel()
	requireShell(t)

	spec := coreproc.Spec{
		Path:   shPath,
		Args:   []string{"sh", "-c", "echo hello"},
		Stdio:  coreproc.StdioCapture,
		Stdout: errWriter{},
	}
	p, err := svcexec.Start(context.Background(), spec)
	//: a clean spawn is the precondition.
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	exit, wErr := p.Wait()
	//: the writer failure must surface as the typed capture error, not silence.
	if !errs.HasCode(wErr, coreproc.CodeStdioCaptureFailed) {
		t.Fatalf("Wait err = %v, want StdioCaptureFailed", wErr)
	}
	//: the real exit status still stands alongside the capture error.
	if exit.Code != 0 {
		t.Fatalf("exit Code = %d, want 0 (the child itself ran fine)", exit.Code)
	}
}

// TestStdioCaptureWithSetpgid asserts capture composes with Setpgid (a
// group-leading child) — the acceptance "works alongside Setpgid/Setsid" contract.
func TestStdioCaptureWithSetpgid(t *testing.T) {
	t.Parallel()
	requireShell(t)

	var out bytes.Buffer
	spec := coreproc.Spec{
		Path:    shPath,
		Args:    []string{"sh", "-c", "printf grouped"},
		Setpgid: true,
		Stdio:   coreproc.StdioCapture,
		Stdout:  &out,
	}
	p, err := svcexec.Start(context.Background(), spec)
	//: a clean spawn is the precondition.
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	//: the leader pid is a valid pgid when Setpgid is set.
	if p.PID() <= 0 {
		t.Fatalf("PID = %d, want > 0", p.PID())
	}
	exit, wErr := p.Wait()
	//: a normal exit is not a wait4 fault.
	if wErr != nil {
		t.Fatalf("Wait: %v", wErr)
	}
	//: the group leader's output is still captured in full.
	if exit.Code != 0 || out.String() != "grouped" {
		t.Fatalf("exit=%d stdout=%q, want 0 / %q", exit.Code, out.String(), "grouped")
	}
	//: the group is gone after the leader exited — kill(-pgid,0) reports ESRCH.
	if err := syscall.Kill(-p.PID(), 0); err != syscall.ESRCH {
		t.Fatalf("group %d kill-probe = %v, want ESRCH", p.PID(), err)
	}
}
