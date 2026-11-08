//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/exec"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// TestNewAttacher pins the default Attacher over proc/exec (#729, the base of
// S0c): a MAINPID= adoption and a PID file kept in a directory anyone may
// write — adopted only within the scope's users — and the end of the adopted
// process observed as STATUS_UNKNOWN.
func TestNewAttacher(t *testing.T) {
	t.Parallel()
	child, err := exec.Start(t.Context(), coreproc.Spec{Path: "/bin/sh", Args: []string{"sh", "-c", "exec sleep 30"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = child.Signal(coreproc.Signal(syscall.SIGKILL))
		_, _ = child.Wait()
	})
	dir := filepath.Join(t.TempDir(), "open")
	if err = os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "main.pid")
	if err = os.WriteFile(file, []byte(strconv.Itoa(child.PID())), 0o600); err != nil {
		t.Fatal(err)
	}

	own := service.NewAttacher(coreproc.AttachValue{UIDs: []int{os.Getuid()}})
	other := service.NewAttacher(coreproc.AttachValue{UIDs: []int{os.Getuid() + 12345}})
	if _, oErr := other.AttachPIDFile(t.Context(), file); !errs.HasCode(oErr, coreproc.CodeAdoptionRefused) {
		t.Fatalf("AttachPIDFile outside the scope = %v, want ADOPTION_REFUSED", oErr)
	}
	fromFile, err := own.AttachPIDFile(t.Context(), file)
	if err != nil {
		t.Fatalf("AttachPIDFile within the scope: %v", err)
	}
	byPID, err := own.AttachPID(t.Context(), child.PID())
	if err != nil {
		t.Fatalf("AttachPID: %v", err)
	}
	if err = child.Signal(coreproc.Signal(syscall.SIGKILL)); err != nil {
		t.Fatalf("killing the child: %v", err)
	}
	for _, p := range []coreproc.Process{fromFile, byPID} {
		if _, wErr := p.Wait(); !errors.Is(wErr, coreproc.StatusUnknown) {
			t.Errorf("an adopted Wait = %v, want STATUS_UNKNOWN", wErr)
		}
	}
}
