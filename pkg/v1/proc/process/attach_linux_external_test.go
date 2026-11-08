//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package process_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc/process"
)

// TestAttachThroughTheFacade pins #729's surface: the refusals are the
// re-exported sentinels, and a PID file kept where anyone may write adopts
// only within its AttachScope.
func TestAttachThroughTheFacade(t *testing.T) {
	t.Parallel()
	if _, err := process.Attach(t.Context(), 1); !errors.Is(err, process.AdoptionRefused) || !errs.HasCode(err, process.CodeAdoptionRefused) {
		t.Errorf("Attach(1) = %v, want AdoptionRefused", err)
	}
	if _, err := process.AttachPIDFile(t.Context(), "relative.pid", process.AttachScope{}); !errors.Is(err, process.PIDFileRefused) {
		t.Errorf("AttachPIDFile(relative) = %v, want PIDFileRefused", err)
	}
	child, err := process.Start(t.Context(), process.Spec{Path: "/bin/sh", Args: []string{"sh", "-c", "exec sleep 30"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = child.Signal(process.SIGKILL)
		_, _ = child.Wait()
	})
	dir := filepath.Join(t.TempDir(), "open")
	if err = os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "x.pid")
	if err = os.WriteFile(file, []byte(strconv.Itoa(child.PID())), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = process.AttachPIDFile(t.Context(), file, process.AttachScope{}); !errors.Is(err, process.AdoptionRefused) {
		t.Errorf("AttachPIDFile(open dir, no scope) = %v, want AdoptionRefused", err)
	}
	adopted, err := process.AttachPIDFile(t.Context(), file, process.AttachScope{UIDs: []int{os.Getuid()}})
	if err != nil {
		t.Fatalf("AttachPIDFile(open dir, own uid) = %v", err)
	}
	_ = child.Signal(process.SIGKILL)
	if _, err = adopted.Wait(); !errors.Is(err, process.StatusUnknown) {
		t.Errorf("adopted Wait = %v, want StatusUnknown", err)
	}
}
