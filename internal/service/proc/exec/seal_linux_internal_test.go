//go:build linux

package exec

import (
	"os"
	"sync/atomic"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// closeOnExec reads FD_CLOEXEC on fd with fcntl(F_GETFD).
func closeOnExec(t *testing.T, fd int) bool {
	t.Helper()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
	if errno != 0 {
		t.Fatalf("fcntl(%d, F_GETFD) = %v", fd, errno)
	}
	return flags&syscall.FD_CLOEXEC != 0
}

// Test_sealWith pins both seal paths of one table: a kernel with
// close_range(CLOSE_RANGE_CLOEXEC) marks everything in one call and never
// walks; one that answers ENOSYS or EINVAL (Linux 5.9/5.10 have the call but
// not the flag) is walked, and remembered so the next seal goes straight to
// the walk; any other refusal fails the spawn as SPAWN_FAILED. Each row holds
// a descriptor without close-on-exec and reads its flag after the seal.
func Test_sealWith(t *testing.T) {
	type tc struct {
		name        string
		answer      syscall.Errno
		wantCalls   int
		wantMissing bool
		wantMarked  bool
		wantErr     bool
	}
	tests := []tc{
		{"close_range marks the table", 0, 1, false, false, false},
		{"ENOSYS falls back to the walk", syscall.ENOSYS, 1, true, true, false},
		{"EINVAL falls back to the walk", syscall.EINVAL, 1, true, true, false},
		{"EPERM fails the spawn", syscall.EPERM, 1, false, false, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe = %v", err)
		}
		defer r.Close()
		defer w.Close()
		fd, err := syscall.Dup(int(w.Fd()))
		if err != nil {
			t.Fatalf("Dup = %v", err)
		}
		defer syscall.Close(fd)
		calls := 0
		//: the double never marks anything itself: a marked fd proves the walk.
		closeRange := func() syscall.Errno {
			calls++
			return c.answer
		}
		var missing atomic.Bool
		err = sealWith(closeRange, &missing)
		if (err != nil) != c.wantErr {
			t.Fatalf("sealWith = %v, want error %v", err, c.wantErr)
		}
		if c.wantErr && !errs.HasCode(err, coreproc.CodeSpawnFailed) {
			t.Errorf("sealWith = %v, want SPAWN_FAILED", err)
		}
		if calls != c.wantCalls || missing.Load() != c.wantMissing {
			t.Errorf("close_range calls = %d, missing = %v; want %d, %v", calls, missing.Load(), c.wantCalls, c.wantMissing)
		}
		if got := closeOnExec(t, fd); got != c.wantMarked {
			t.Errorf("FD_CLOEXEC on fd %d = %v, want %v", fd, got, c.wantMarked)
		}
		//: a kernel remembered without the call is walked without asking again.
		if c.wantMissing {
			if err := sealWith(closeRange, &missing); err != nil || calls != c.wantCalls {
				t.Errorf("second seal = %v after %d calls, want the walk alone", err, calls)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}
