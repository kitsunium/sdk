//go:build unix

package exec

import (
	"errors"
	"io"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// dieWithParentHelperEnv makes the test binary run as the helper process of
// TestStart_dieWithParent — a supervisor the test can SIGKILL — instead of as
// a test suite.
const dieWithParentHelperEnv string = "SDK_EXEC_DIEWITHPARENT_HELPER"

// sleepPath is the program the helper's child runs: the holder itself, so a
// signal to it closes the descriptor it inherited (a shell would die and
// leave its sleep holding it).
const sleepPath string = "/bin/sleep"

// pdeathsigKernel reports whether this kernel has a parent-death signal.
func pdeathsigKernel() bool {
	//: PR_SET_PDEATHSIG on Linux, procctl(PROC_PDEATHSIG_CTL) on FreeBSD.
	return runtime.GOOS == "linux" || runtime.GOOS == "freebsd"
}

// TestStart_dieWithParent starts a helper — this test binary again — that
// spawns `sleep 5` with DieWithParent = SIGTERM and the write end of a pipe
// at fd 3, writes one byte to it, and blocks. The test SIGKILLs the helper,
// its own child: the grandchild must receive SIGTERM and die, so the pipe's
// last writer is gone and the test's read end sees EOF within a second
// (#931). Without the parent-death signal the sleep holds the pipe for its
// five seconds. Where no kernel mechanism exists, Start refuses the field
// with UnsupportedPlatform before spawning anything.
func TestStart_dieWithParent(t *testing.T) {
	//: the helper half: spawn the grandchild, report, block.
	if os.Getenv(dieWithParentHelperEnv) != "" {
		runDieWithParentHelper(t)
		return
	}
	t.Parallel()
	//: the refusal rows: nothing is spawned for a field that cannot be honoured.
	if !pdeathsigKernel() {
		_, err := Start(t.Context(), coreproc.Spec{
			Path: sleepPath, Args: []string{"sleep", "5"},
			DieWithParent: coreproc.Signal(syscall.SIGTERM),
		})
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Fatalf("Start(DieWithParent) on %s = %v, want UnsupportedPlatform", runtime.GOOS, err)
		}
		return
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe = %v", err)
	}
	defer pr.Close()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable = %v", err)
	}
	helper, err := Start(t.Context(), coreproc.Spec{
		Path:       self,
		Args:       []string{self, "-test.run=^TestStart_dieWithParent$", "-test.count=1"},
		Env:        []string{dieWithParentHelperEnv + "=1"},
		ExtraFiles: []*os.File{pw},
		Stdio:      coreproc.StdioNull,
	})
	//: the test keeps no write end of its own: EOF means every holder is gone.
	pw.Close()
	if err != nil {
		t.Fatalf("Start(helper) = %v", err)
	}
	t.Cleanup(func() {
		if kerr := helper.Signal(coreproc.Signal(syscall.SIGKILL)); kerr != nil {
			t.Logf("cleanup Signal: %v", kerr)
		}
		if _, werr := helper.Wait(); werr != nil {
			t.Logf("cleanup Wait: %v", werr)
		}
	})
	//: the helper writes once its grandchild runs.
	ready := make([]byte, 1)
	if err := pr.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline = %v", err)
	}
	if _, err := io.ReadFull(pr, ready); err != nil {
		t.Fatalf("reading the helper's ready byte = %v", err)
	}
	//: the supervisor dies abruptly, as on an OOM kill.
	if err := helper.Signal(coreproc.Signal(syscall.SIGKILL)); err != nil {
		t.Fatalf("SIGKILL to the helper = %v", err)
	}
	if _, err := helper.Wait(); err != nil {
		t.Fatalf("Wait(helper) = %v", err)
	}
	if err := pr.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline = %v", err)
	}
	n, err := pr.Read(make([]byte, 1))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("read after the helper's death = (%d, %v), want EOF within 1s: the grandchild outlived its parent", n, err)
	}
}

// runDieWithParentHelper is the helper's body: it starts sleep with fd 3 and
// DieWithParent = SIGTERM, tells the test, and blocks until it is killed.
func runDieWithParentHelper(t *testing.T) {
	t.Helper()
	pipe := os.NewFile(3, "ready")
	child, err := Start(t.Context(), coreproc.Spec{
		Path:          sleepPath,
		Args:          []string{"sleep", "5"},
		ExtraFiles:    []*os.File{pipe},
		DieWithParent: coreproc.Signal(syscall.SIGTERM),
	})
	if err != nil {
		t.Fatalf("Start(grandchild) = %v", err)
	}
	if _, err := pipe.Write([]byte{'r'}); err != nil {
		t.Fatalf("writing the ready byte = %v", err)
	}
	_, _ = child.Wait()
	//: the grandchild's sleep ended on its own: the test killed nobody.
	time.Sleep(time.Minute)
}
