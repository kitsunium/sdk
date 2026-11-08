//go:build linux && (amd64 || arm64)

package service_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// The supervisor's hand-over across a real re-exec (S0h, kitsunium/sdk#738).
// The test runs this binary as a supervisor (handover-old), under the test's
// t.TempDir(): it starts the store unit of fdstore_linux_external_test —
// its own child, crashed once so it has restarted and keeps a file in its
// store —, freezes it and re-executes itself (handover-new), which takes the
// hand-over, resumes the unit and checks it: the same main process, its
// restart count, its store handed again to the run after one more crash,
// only the payload's descriptors inherited. Then it stops the unit. A
// hand-over of a version the incoming image does not read, or naming a
// descriptor that is not the one recorded, is refused: the incoming image
// stops its child in order and leaves no descriptor open. Every process
// signalled is the test's own: the supervisor, its service.

const (
	// handoverModeEnv says what the supervisor hands over: "ok", "version"
	// (a later version), "older" (a version before any), "corrupt" (a body
	// whose sum is wrong), "mismatch" (another file at a listed descriptor).
	handoverModeEnv = "PROC_SERVICE_TEST_HANDOVER"
	// supervisorTimeout bounds one supervisor's whole run.
	supervisorTimeout = 90 * time.Second
	// supervisorDrain bounds the wait for what the supervisor's output is
	// held by, once it ended: a service still running holds it.
	supervisorDrain = 5 * time.Second
)

func init() {
	helpers["handover-old"] = handoverOld
	helpers["handover-new"] = handoverNew
}

// report writes one line of the supervisor's report.
func report(dir, name, line string) {
	_ = os.WriteFile(filepath.Join(dir, name), []byte(line), 0o600)
}

// failed reports a failure of the supervisor and ends it.
func failed(dir, stage string, err error) {
	report(dir, "failed", stage+": "+fmt.Sprint(err))
	os.Exit(2)
}

// handoverOld is the outgoing image: the unit started, crashed once, frozen,
// then this binary executed again with the hand-over.
func handoverOld() {
	dir, mode := os.Getenv(storeDirEnv), os.Getenv(handoverModeEnv)
	ctx, cancel := context.WithTimeout(context.Background(), supervisorTimeout)
	defer cancel()
	src, err := service.ListenNotify()
	if err != nil {
		failed(dir, "listen", err)
	}
	cfg, err := handoverConfig(dir, src)
	if err != nil {
		failed(dir, "config", err)
	}
	u, err := service.NewUnit(&cfg)
	if err != nil {
		failed(dir, "new", err)
	}
	if err = u.Start(ctx); err != nil {
		failed(dir, "start", err)
	}
	if _, err = upAgain(ctx, u, 0, 0, 2); err != nil {
		failed(dir, "first run", err)
	}
	st, err := crashed(ctx, u, 1)
	if err != nil {
		failed(dir, "crash", err)
	}
	stored := u.FDStore()
	report(dir, "before", fmt.Sprintf("%d %d %d %d %d", st.MainPID, st.Restarts, len(stored), stored[0].Device, stored[0].Inode))
	h, err := service.Freeze(ctx, []*service.Unit{u})
	if err != nil {
		failed(dir, "freeze", err)
	}
	switch mode {
	case "version":
		service.ForceHandoverVersion(99)
	case "older":
		service.ForceHandoverVersion(0)
	case "corrupt":
		service.ForceBeforeExec(func(payload []byte) { payload[len(payload)/2] ^= 0xff })
	case "mismatch":
		// After the payload recorded the file: then the descriptor no longer
		// holds what it says.
		service.ForceBeforeExec(func([]byte) { replaceStored(dir, stored[0]) })
	}
	exe, err := os.Executable()
	if err != nil {
		failed(dir, "executable", err)
	}
	image, err := service.OpenImage(exe)
	if err != nil {
		failed(dir, "image", err)
	}
	err = service.Reexec(h, []string{exe, "-test.run=^$"}, image, reexecEnviron("handover-new"))
	failed(dir, "reexec", err)
}

// replaceStored puts another file at the descriptor the hand-over holds for
// the stored one — found by its device and inode —, as a payload naming a
// wrong descriptor would.
func replaceStored(dir string, stored coresvc.StoredFDValue) {
	other, err := os.CreateTemp(dir, "other")
	if err != nil {
		failed(dir, "other", err)
	}
	for _, fd := range openFDs() {
		var st syscall.Stat_t
		if syscall.Fstat(fd, &st) == nil && uint64(st.Dev) == stored.Device && st.Ino == stored.Inode {
			if err = syscall.Dup3(int(other.Fd()), fd, syscall.O_CLOEXEC); err != nil {
				failed(dir, "dup3", err)
			}
			return
		}
	}
	failed(dir, "replace", os.ErrNotExist)
}

// openFDs lists this process's descriptors from 3 on.
func openFDs() []int {
	entries, _ := os.ReadDir("/proc/self/fd")
	var out []int
	for _, e := range entries {
		if fd, err := strconv.Atoi(e.Name()); err == nil && fd >= 3 {
			out = append(out, fd)
		}
	}
	return out
}

// inheritable lists the descriptors from 3 on that are not close-on-exec,
// each with what it refers to: what an exec would hand on.
func inheritable() string {
	var out []string
	for _, fd := range openFDs() {
		flags, _, errno := syscall.RawSyscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
		if errno == 0 && flags&syscall.FD_CLOEXEC == 0 {
			target, _ := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
			out = append(out, strconv.Itoa(fd)+"="+target)
		}
	}
	return strings.Join(out, ",")
}

// handoverNew is the incoming image: the hand-over taken, the descriptors
// scanned, then — accepted — the unit resumed, checked, crashed once more,
// stopped; or — refused — the refusal reported.
func handoverNew() {
	dir := os.Getenv(storeDirEnv)
	ctx, cancel := context.WithTimeout(context.Background(), supervisorTimeout)
	defer cancel()
	h, err := service.TakeHandover(ctx, 5*time.Second)
	code, _ := errs.CodeOf(err)
	var stopped []string
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == "pid" {
			stopped = append(stopped, f.StringValue())
		}
	}
	report(dir, "take", fmt.Sprintf("code=%s stopped=%s inheritable=%s", code, strings.Join(stopped, ","), inheritable()))
	if err != nil || h == nil {
		report(dir, "end", "inheritable="+inheritable())
		os.Exit(0)
	}
	cfg, err := handoverConfig(dir, nil)
	if err != nil {
		failed(dir, "config", err)
	}
	u, err := service.Resume(&cfg, h)
	if err != nil {
		failed(dir, "resume", err)
	}
	st := u.State()
	stored := u.FDStore()
	if len(stored) != 1 {
		failed(dir, "store", fmt.Errorf("%d stored", len(stored)))
	}
	report(dir, "after", fmt.Sprintf("%d %d %d %d %d %s/%s", st.MainPID, st.Restarts, len(stored), stored[0].Device, stored[0].Inode, st.State, st.Sub))
	_ = os.Remove(filepath.Join(dir, "handed"))
	if st, err = crashed(ctx, u, 2); err != nil {
		failed(dir, "crash after", err)
	}
	handed, _ := os.ReadFile(filepath.Join(dir, "handed"))
	report(dir, "handed-after", string(handed)+"|"+strconv.Itoa(st.MainPID))
	if err = u.Stop(ctx); err != nil {
		failed(dir, "stop", err)
	}
	if err = u.Close(ctx); err != nil {
		failed(dir, "close", err)
	}
	if err = h.Close(ctx, time.Second); err != nil {
		failed(dir, "handover close", err)
	}
	report(dir, "end", "inheritable="+inheritable())
	os.Exit(0)
}

// supervise runs this binary as the supervisor in mode and returns its
// report directory once it ended.
func supervise(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	// The supervisor's TMPDIR holds its notification sockets, which a killed
	// supervisor leaves; short, for sun_path, so not under t.TempDir().
	tmp, err := os.MkdirTemp("", "hs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	t.Cleanup(func() { killHolders(t, dir) })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := osexec.Command(exe, "-test.run=^$")
	cmd.Env = helperEnviron("handover-old", storeDirEnv+"="+dir, handoverModeEnv+"="+mode, "TMPDIR="+tmp)
	// The supervisor dies with the test; its service, spawned by it, carries
	// no such signal and must not: it outlives the re-exec.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	cmd.WaitDelay = supervisorDrain
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err = cmd.Start(); err != nil {
		t.Fatalf("supervisor: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(supervisorTimeout + 10*time.Second):
		_ = cmd.Process.Kill()
		// A service left behind holds the output Wait is draining.
		killHolders(t, dir)
		err = <-done
	}
	t.Cleanup(func() { stopLeftover(t, dir) })
	if failure, ferr := os.ReadFile(filepath.Join(dir, "failed")); ferr == nil {
		t.Fatalf("the supervisor failed at %s (exit %v)\n%s", failure, err, out.String())
	}
	if err != nil {
		t.Fatalf("supervisor: %v\n%s", err, out.String())
	}
	return dir
}

// stopLeftover kills a service a failed supervisor left behind: a process
// whose environment names dir is this test's alone.
func stopLeftover(t *testing.T, dir string) {
	for _, pid := range []int{field(t, dir, "before", 0), field(t, dir, "handed-after", -1)} {
		if pid > 0 && runsService(pid, dir) {
			t.Errorf("process %d of the service outlived its supervisor", pid)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// field reads the i-th field of a report line, -1 the last after "|"; 0
// when absent.
func field(t *testing.T, dir, name string, i int) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0
	}
	if i < 0 {
		_, last, _ := strings.Cut(string(raw), "|")
		n, _ := strconv.Atoi(last)
		return n
	}
	n, _ := strconv.Atoi(strings.Fields(string(raw))[i])
	return n
}

// readReport reads one report line.
func readReport(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("report %s: %v", name, err)
	}
	return string(raw)
}

// TestRealReexecHandover: the store unit crosses a real re-exec of its
// supervisor — the same main process, its restart count, its store, which
// the next run is handed —, the incoming image inherits the payload's
// descriptors alone, and nothing outlives the supervisor.
func TestRealReexecHandover(t *testing.T) {
	dir := supervise(t, "ok")
	before, after := strings.Fields(readReport(t, dir, "before")), strings.Fields(readReport(t, dir, "after"))
	if !slices.Equal(before, after[:5]) || after[5] != "active/running" {
		t.Fatalf("frozen %v, resumed %v: the unit did not cross the exec as it was", before, after)
	}
	take := readReport(t, dir, "take")
	if !strings.HasPrefix(take, "code=0.0.0.0 stopped= inheritable=") {
		t.Fatalf("take: %s", take)
	}
	if inh := strings.TrimPrefix(take, "code=0.0.0.0 stopped= inheritable="); inh != "" {
		t.Fatalf("the incoming image holds inheritable descriptors after the take: %s", inh)
	}
	if handed := readReport(t, dir, "handed-after"); !strings.Contains(handed, "|state|true|state-1") {
		t.Fatalf("the run after the hand-over was handed %q", handed)
	}
	if end := readReport(t, dir, "end"); end != "inheritable=" {
		t.Fatalf("at the end: %s", end)
	}
}

// TestRealReexecHandoverRefused: a hand-over of a later version (N to N-1), of
// a version below the first (an N-1 this image has nothing to migrate from: one
// version exists), corrupted, or naming a descriptor that is not the one
// recorded, is refused by the incoming image, which stops its child in order
// and keeps no descriptor the exec handed it.
func TestRealReexecHandoverRefused(t *testing.T) {
	for _, c := range []struct{ mode, code string }{
		{"version", coresvc.CodeHandoverInvalid.String()},
		{"older", coresvc.CodeHandoverInvalid.String()},
		{"corrupt", coresvc.CodeHandoverInvalid.String()},
		{"mismatch", coresvc.CodeHandoverMismatch.String()},
	} {
		t.Run(c.mode, func(t *testing.T) {
			dir := supervise(t, c.mode)
			main := strconv.Itoa(field(t, dir, "before", 0))
			take := readReport(t, dir, "take")
			if !strings.HasPrefix(take, "code="+c.code+" stopped=") || !strings.Contains(take, "stopped="+main) {
				t.Fatalf("take: %s; want %s, main %s stopped", take, c.code, main)
			}
			if !strings.HasSuffix(take, "inheritable=") {
				t.Fatalf("the refused hand-over left descriptors open: %s", take)
			}
		})
	}
}
