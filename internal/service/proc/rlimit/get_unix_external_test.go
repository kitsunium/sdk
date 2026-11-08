//go:build unix

package rlimit_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/rlimit"
)

// TestGetReadsBackWhatApplySet pins #939's Get: the caller's NOFILE equals
// getrlimit(2)'s answer, and reflects an Apply made in the same process — a
// soft limit lowered by one, then restored. Not parallel: the limit is the
// test binary's own, process-wide.
func TestGetReadsBackWhatApplySet(t *testing.T) {
	var kernel syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &kernel); err != nil {
		t.Fatalf("getrlimit: %v", err)
	}
	got, err := rlimit.Get(0, coreproc.ResourceNoFile)
	if err != nil || got.Soft != uint64(kernel.Cur) || got.Hard != uint64(kernel.Max) {
		t.Fatalf("Get(0, NOFILE) = %+v, %v; getrlimit says %d/%d", got, err, kernel.Cur, kernel.Max)
	}
	lowered := coreproc.LimitValue{Soft: got.Soft - 1, Hard: got.Hard}
	if err := rlimit.Apply(0, map[coreproc.Resource]coreproc.LimitValue{coreproc.ResourceNoFile: lowered}); err != nil {
		t.Skipf("Apply refused here: %v", err)
	}
	t.Cleanup(func() { _ = rlimit.Apply(0, map[coreproc.Resource]coreproc.LimitValue{coreproc.ResourceNoFile: got}) })
	//: the read-back follows the write.
	if after, err := rlimit.Get(os.Getpid(), coreproc.ResourceNoFile); err != nil || after != lowered {
		t.Fatalf("Get after Apply = %+v, %v; want %+v", after, err, lowered)
	}
	//: an unmapped resource is refused as Apply refuses it.
	if _, err := rlimit.Get(0, coreproc.Resource(0x7BAD)); !errs.HasCode(err, coreproc.CodeUnknownResource) {
		t.Fatalf("Get(unknown) = %v, want UNKNOWN_RESOURCE", err)
	}
}

// TestGetAnotherProcess pins the foreign read: prlimit64 on Linux reads a
// child of this test, which inherited the test's limits; elsewhere it is
// refused, as Apply refuses a foreign pid.
func TestGetAnotherProcess(t *testing.T) {
	child, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "sleep 30"}, &os.ProcAttr{Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}})
	if err != nil {
		t.Skipf("no /bin/sh: %v", err)
	}
	defer func() { _ = child.Kill(); _, _ = child.Wait() }()
	got, err := rlimit.Get(child.Pid, coreproc.ResourceNoFile)
	if runtime.GOOS != "linux" {
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Fatalf("Get(child) on %s = %v, want UNSUPPORTED_PLATFORM", runtime.GOOS, err)
		}
		return
	}
	own, _ := rlimit.Get(0, coreproc.ResourceNoFile)
	if err != nil || got.Hard != own.Hard {
		t.Fatalf("Get(child) = %+v, %v; want the inherited hard limit %d", got, err, own.Hard)
	}
}

// TestOpenFilesCountsTen pins #939's OpenFiles: ten files this test opens
// under its own directory raise the count by exactly ten where a count is
// bound, and the refusal is UNSUPPORTED_PLATFORM where none is. Not parallel:
// the descriptor table is process-wide.
func TestOpenFilesCountsTen(t *testing.T) {
	before, err := rlimit.OpenFiles()
	switch runtime.GOOS {
	case "openbsd", "netbsd", "dragonfly":
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Fatalf("OpenFiles on %s = %v, want UNSUPPORTED_PLATFORM", runtime.GOOS, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("OpenFiles = %v", err)
	}
	dir := t.TempDir()
	files := make([]*os.File, 0, 10)
	for i := range 10 {
		f, ferr := os.Create(filepath.Join(dir, strconv.Itoa(i)))
		if ferr != nil {
			t.Fatal(ferr)
		}
		files = append(files, f)
	}
	after, err := rlimit.OpenFiles()
	for _, f := range files {
		_ = f.Close()
	}
	if err != nil || after-before != 10 {
		t.Fatalf("OpenFiles: %d before, %d after ten opens (%v), want +10", before, after, err)
	}
}
