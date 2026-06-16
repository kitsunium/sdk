//go:build unix

// Package rlimit_test — cross-platform native-behaviour proof. Unlike the
// Linux-only /proc observability in rlimit_external_test.go, this reads the
// ceiling back via getrlimit(2), so it runs and asserts the real setrlimit(2)
// effect on EVERY Unix target (linux, darwin, freebsd, netbsd, openbsd).
package rlimit_test

import (
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/service/proc/rlimit"
)

// TestApplyNoFileObservableUnix lowers the calling process's RLIMIT_NOFILE soft
// ceiling by one and asserts getrlimit(2) reports the new value — the proof that
// rlimit acts natively on this Unix kernel rather than degrading. Field reads go
// through rlimFields so FreeBSD/DragonFly (int64 fields) need no inline cast.
func TestApplyNoFileObservableUnix(t *testing.T) {
	var before syscall.Rlimit
	//: read the current ceiling to derive a valid lower target.
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &before); err != nil {
		//: a host that cannot read its own rlimit cannot run this assertion.
		t.Skipf("getrlimit(NOFILE): %v", err)
	}
	curSoft, curHard := rlimFields(before)
	//: a soft ceiling under 16 leaves no room to lower and observe the change.
	if curSoft < 16 {
		//: cannot lower below the floor the test targets.
		t.Skipf("starting soft nofile too low: %d", curSoft)
	}
	target := curSoft - 1
	err := rlimit.Apply(0, map[coreproc.Resource]coreproc.LimitValue{
		//: lower soft to target, keep hard at the current hard (a valid ceiling).
		coreproc.ResourceNoFile: {Soft: target, Hard: curHard},
	})
	//: applying a valid lower soft ceiling must succeed on every Unix target.
	if err != nil {
		//: an error here means setrlimit or the resource mapping is broken.
		t.Fatalf("Apply(NOFILE soft=%d hard=%d): %v", target, curHard, err)
	}
	var after syscall.Rlimit
	//: read the ceiling back; the kernel must now report the lowered soft value.
	if gerr := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &after); gerr != nil {
		//: a failing read-back leaves the effect unverifiable.
		t.Fatalf("getrlimit(NOFILE) after apply: %v", gerr)
	}
	gotSoft, _ := rlimFields(after)
	//: the lowered soft ceiling must be observable — the native-behaviour proof.
	if gotSoft != target {
		//: the limit did not take effect — the core assertion failed.
		t.Fatalf("soft nofile = %d, want %d", gotSoft, target)
	}
}
