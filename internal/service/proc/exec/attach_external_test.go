//go:build (linux && !mips && !mipsle && !mips64 && !mips64le) || darwin || dragonfly || freebsd || netbsd || openbsd

// Package exec_test — adopting a process from a PID or a PID file (#729), on
// every kernel that holds one: a pidfd on Linux, a kqueue knote on darwin and
// the BSDs. The cgroup bound is the one difference, given by cgroupBound.
package exec_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// startSleeping starts a child of the test, through the SDK's spawn, that
// sleeps until killed; the test's cleanup kills and reaps it.
func startSleeping(t *testing.T) coreproc.Process {
	t.Helper()
	p, err := svcexec.Start(t.Context(), coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", "exec sleep 30"}})
	if err != nil {
		t.Fatalf("Start = %v", spawnDetail(err))
	}
	t.Cleanup(func() {
		_ = p.Signal(coreproc.Signal(syscall.SIGKILL))
		_, _ = p.Wait()
	})
	return p
}

// TestAttachObservesTheEnd pins #729's handle: the end of an adopted process
// is observed with STATUS_UNKNOWN and no exit code, its spawner still reads
// the real status — the adoption never waits it in the kernel's sense — and,
// once the process is reaped, a signal through the handle is ESRCH rather than
// reaching whatever process takes the pid next.
func TestAttachObservesTheEnd(t *testing.T) {
	t.Parallel()
	child := startSleeping(t)
	adopted, err := svcexec.Attach(t.Context(), child.PID())
	if err != nil {
		t.Fatalf("Attach = %v", err)
	}
	waiter, ok := adopted.(coreproc.ContextWaiter)
	if !ok {
		t.Fatalf("the adopted handle is no ContextWaiter")
	}
	short, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, wErr := waiter.WaitContext(short); !errors.Is(wErr, context.DeadlineExceeded) {
		t.Fatalf("WaitContext while running = %v, want the deadline", wErr)
	}
	if sErr := child.Signal(coreproc.Signal(syscall.SIGKILL)); sErr != nil {
		t.Fatalf("killing the child: %v", sErr)
	}
	exit, wErr := adopted.Wait()
	if !errors.Is(wErr, coreproc.StatusUnknown) || exit.Code != -1 || exit.Success() {
		t.Fatalf("adopted Wait = %+v, %v, want Code -1 and STATUS_UNKNOWN", exit, wErr)
	}
	real, rErr := child.Wait()
	if rErr != nil || !real.Signaled || real.Signal != coreproc.Signal(syscall.SIGKILL) {
		t.Fatalf("the spawner's Wait = %+v, %v, want the real SIGKILL status", real, rErr)
	}
	if sErr := adopted.Signal(coreproc.Signal(syscall.SIGTERM)); !errs.HasCode(sErr, coreproc.CodeSignalFailed) || !errors.Is(sErr, syscall.ESRCH) {
		t.Errorf("Signal after the reap = %v, want SIGNAL_FAILED wrapping ESRCH", sErr)
	}
	if _, ok := adopted.(coreproc.ProcessHandle); !ok {
		t.Errorf("the adopted handle is no ProcessHandle")
	}
}

// TestAttachStop pins Stop on an adopted process: the signal goes through the
// pidfd and Stop returns once the process has ended.
func TestAttachStop(t *testing.T) {
	t.Parallel()
	child := startSleeping(t)
	adopted, err := svcexec.Attach(t.Context(), child.PID())
	if err != nil {
		t.Fatalf("Attach = %v", err)
	}
	if sErr := adopted.Stop(t.Context(), 10*time.Second, coreproc.Signal(syscall.SIGTERM)); sErr != nil {
		t.Fatalf("Stop = %v", sErr)
	}
	exit, wErr := child.Wait()
	if wErr != nil || !exit.Signaled || exit.Signal != coreproc.Signal(syscall.SIGTERM) {
		t.Errorf("the child's exit = %+v, %v, want SIGTERM", exit, wErr)
	}
}

// TestAttachRefusals pins what Attach refuses before holding anything.
func TestAttachRefusals(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	type tc struct {
		name string
		ctx  context.Context
		pid  int
		want errs.Code
	}
	tests := []tc{
		{"pid 0", t.Context(), 0, coreproc.CodeInvalidSpec},
		{"init", t.Context(), 1, coreproc.CodeAdoptionRefused},
		{"this process", t.Context(), os.Getpid(), coreproc.CodeAdoptionRefused},
		{"no such process", t.Context(), 2147483647, coreproc.CodeSignalFailed},
	}
	for _, c := range tests {
		if p, err := svcexec.Attach(c.ctx, c.pid); p != nil || !errs.HasCode(err, c.want) {
			t.Errorf("Attach(%s) = %v, %v, want code %v", c.name, p, err, c.want)
		}
	}
	if _, err := svcexec.Attach(cancelled, 2); !errors.Is(err, context.Canceled) {
		t.Errorf("Attach(cancelled) = %v, want context.Canceled", err)
	}
}

// ancestorsVouched is the test's own oracle of the walk's trust for dir: every
// directory from the root down belongs to root or this user and is closed to
// group and others unless sticky. It is written apart from the walk so the
// test compares two readings.
func ancestorsVouched(t *testing.T, dir string) bool {
	t.Helper()
	euid := uint32(os.Geteuid())
	cur := "/"
	for _, part := range append([]string{""}, strings.Split(strings.Trim(dir, "/"), "/")...) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			t.Fatalf("lstat %s: %v", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		st := info.Sys().(*syscall.Stat_t)
		if st.Uid != 0 && st.Uid != euid {
			return false
		}
		if st.Mode&0o022 != 0 && st.Mode&syscall.S_ISVTX == 0 {
			return false
		}
	}
	return true
}

// TestAttachPIDFile pins #729's adoption from a PID file: a symbolic link, a
// FIFO, a relative path or garbage are PID_FILE_REFUSED; PID 1 and this
// process ADOPTION_REFUSED; a file another user could have written adopts only
// through a bound — the cgroup and the users, checked whatever the file — and
// a link never redirects the read to a witness elsewhere in the tree.
func TestAttachPIDFile(t *testing.T) {
	t.Parallel()
	child := startSleeping(t)
	pid := strconv.Itoa(child.PID())
	//: the canonical path: darwin's temporary tree lies under /var, a link to
	//: /private/var, which the walk follows (root's link in root's directory)
	//: and ancestorsVouched, stricter, would not.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	//: a directory anyone may write: no file in it is trustworthy.
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	//: a directory closed to others: trustworthy when its ancestors are.
	closed := filepath.Join(base, "closed")
	if err := os.Mkdir(closed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(closed, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) string {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	witness := write(filepath.Join(base, "witness"), pid)
	link := filepath.Join(closed, "link.pid")
	if err := os.Symlink(witness, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(closed, "fifo.pid")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	ownCgroup, ownCgroupCode, ownCgroupSkip := cgroupBound(t)
	trustedHere := ancestorsVouched(t, closed)
	own := []int{os.Getuid()}

	type tc struct {
		name string
		path string
		want coreproc.AttachValue
		//: zero: adopted.
		code errs.Code
		skip string
	}
	tests := []tc{
		{
			name: "a closed directory, no bound", path: write(filepath.Join(closed, "x.pid"), pid+"\n"),
			code: map[bool]errs.Code{true: 0, false: coreproc.CodeAdoptionRefused}[trustedHere],
		},
		{name: "an open directory, no bound", path: write(filepath.Join(open, "x.pid"), pid), code: coreproc.CodeAdoptionRefused},
		{name: "an open directory, bound to this user", path: filepath.Join(open, "x.pid"), want: coreproc.AttachValue{UIDs: own}},
		{
			name: "a closed directory, bound to another user", path: filepath.Join(closed, "x.pid"),
			want: coreproc.AttachValue{UIDs: []int{os.Getuid() + 12345}}, code: coreproc.CodeAdoptionRefused,
		},
		{
			name: "an open directory, bound to this cgroup", path: filepath.Join(open, "x.pid"),
			want: coreproc.AttachValue{Cgroup: ownCgroup}, code: ownCgroupCode, skip: ownCgroupSkip,
		},
		{
			name: "an open directory, bound to another cgroup", path: filepath.Join(open, "x.pid"),
			want: coreproc.AttachValue{Cgroup: "/kitsunium-no-such-cgroup"}, code: coreproc.CodeAdoptionRefused,
		},
		{name: "a symbolic link to a witness", path: link, want: coreproc.AttachValue{UIDs: own}, code: coreproc.CodePIDFileRefused},
		{name: "a FIFO", path: fifo, want: coreproc.AttachValue{UIDs: own}, code: coreproc.CodePIDFileRefused},
		{name: "a relative path", path: "x.pid", want: coreproc.AttachValue{UIDs: own}, code: coreproc.CodePIDFileRefused},
		{name: "garbage", path: write(filepath.Join(closed, "garbage.pid"), "12ab"), want: coreproc.AttachValue{UIDs: own}, code: coreproc.CodePIDFileRefused},
		{name: "init", path: write(filepath.Join(closed, "init.pid"), "1"), want: coreproc.AttachValue{UIDs: own}, code: coreproc.CodeAdoptionRefused},
		{
			name: "this process", path: write(filepath.Join(closed, "self.pid"), strconv.Itoa(os.Getpid())),
			want: coreproc.AttachValue{UIDs: own}, code: coreproc.CodeAdoptionRefused,
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			if c.skip != "" {
				t.Skip(c.skip)
			}
			p, err := svcexec.AttachPIDFile(t.Context(), c.path, c.want)
			if c.code != 0 {
				if p != nil || !errs.HasCode(err, c.code) {
					t.Fatalf("AttachPIDFile(%s) = %v, %v, want code %v", c.path, p, err, c.code)
				}
				return
			}
			if err != nil {
				t.Fatalf("AttachPIDFile(%s) = %v, want the child", c.path, err)
			}
			if p.PID() != child.PID() {
				t.Errorf("adopted pid %d, want %d", p.PID(), child.PID())
			}
		})
	}
}

// TestAttachPIDFileReusedPID pins the reuse race as far as one test can make
// it: the process a PID file named is gone — killed and reaped — when the file
// is read, so the adoption is refused (ESRCH) rather than holding nothing or
// a stranger; and a handle taken before the end reports the end, then ESRCH.
func TestAttachPIDFileReusedPID(t *testing.T) {
	t.Parallel()
	child := startSleeping(t)
	file := filepath.Join(t.TempDir(), "x.pid")
	if err := os.WriteFile(file, []byte(strconv.Itoa(child.PID())), 0o600); err != nil {
		t.Fatal(err)
	}
	bound := coreproc.AttachValue{UIDs: []int{os.Getuid()}}
	early, err := svcexec.AttachPIDFile(t.Context(), file, bound)
	if err != nil {
		t.Fatalf("AttachPIDFile = %v", err)
	}
	_ = child.Signal(coreproc.Signal(syscall.SIGKILL))
	_, _ = child.Wait()
	if _, wErr := early.Wait(); !errors.Is(wErr, coreproc.StatusUnknown) {
		t.Errorf("the early handle's Wait = %v, want STATUS_UNKNOWN", wErr)
	}
	if sErr := early.Signal(coreproc.Signal(syscall.SIGTERM)); !errors.Is(sErr, syscall.ESRCH) {
		t.Errorf("the early handle's Signal = %v, want ESRCH", sErr)
	}
	if p, lErr := svcexec.AttachPIDFile(t.Context(), file, bound); p != nil || !errs.HasCode(lErr, coreproc.CodeSignalFailed) {
		t.Errorf("AttachPIDFile on a reaped pid = %v, %v, want SIGNAL_FAILED (ESRCH)", p, lErr)
	}
}
