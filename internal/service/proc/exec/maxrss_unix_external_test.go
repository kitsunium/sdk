//go:build unix && !illumos && !solaris

// Package exec_test — the peak RSS of a real child, in KiB.
package exec_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// TestWait_maxRSSIsKiB spawns a shell that holds 64 MiB in a variable and reads
// its peak RSS back through Wait. The port documents KiB on every platform;
// darwin's kernel reports ru_maxrss in bytes and Linux and the BSDs in
// kilobytes, so the bounds — 32 MiB to 1 GiB, written in KiB — accept any
// honest KiB figure for that child and refuse a byte figure, which would read
// about 67 000 000 and land far above 1 048 576 (issue #357). illumos and
// Solaris are excluded because their kernels leave ru_maxrss at 0.
func TestWait_maxRSSIsKiB(t *testing.T) {
	t.Parallel()
	const (
		//: 32 MiB in KiB: half of what the child holds, so the shell's own
		//: copy strategy cannot push an honest reading below it.
		lowKiB int64 = 32 << 10
		//: 1 GiB in KiB: far above the child, far below its size in bytes.
		highKiB int64 = 1 << 20
	)
	//: absolute paths, because the child runs with an empty environment and
	//: therefore no PATH; the length check proves the 64 MiB were really held.
	script := `x=$(/bin/dd if=/dev/zero bs=1048576 count=64 2>/dev/null | /usr/bin/tr '\000' a); test "${#x}" -eq 67108864`
	p, err := svcexec.Start(t.Context(), coreproc.Spec{
		Path: "/bin/sh",
		Args: []string{"sh", "-c", script},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	exit, err := p.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if exit.Code != 0 {
		t.Fatalf("the child exited %d: it did not hold the 64 MiB the bounds assume", exit.Code)
	}
	if exit.MaxRSS < lowKiB || exit.MaxRSS > highKiB {
		t.Errorf("MaxRSS = %d, want a KiB figure in [%d, %d]", exit.MaxRSS, lowKiB, highKiB)
	}
}
