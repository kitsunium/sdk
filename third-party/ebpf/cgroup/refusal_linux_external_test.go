//go:build linux && (amd64 || arm64)

package cgroup_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
	svcbpf "github.com/kitsunium/sdk/internal/service/proc/bpf"
)

// TestADirectoryThatIsNoGroupIsRefused attaches to a test's temporary
// directory, which is on no cgroup v2 filesystem: ATTACH_FAILED before any
// program is loaded, with or without the privilege — or LOAD_REFUSED before
// 5.11 without CAP_SYS_RESOURCE, the limit being checked first. Nothing can
// be attached anywhere: the directory is refused first.
func TestADirectoryThatIsNoGroupIsRefused(t *testing.T) {
	p, err := svcbpf.AttachDevice(t.TempDir(), corebpf.DevicePolicyValue{Default: corebpf.Allow})
	//: attached to a plain directory: a defect, undone at once.
	if err == nil {
		_ = p.Close()
		t.Fatal("attached to a directory that is no cgroup")
	}
	//: ATTACH_FAILED, or LOAD_REFUSED by the memlock limit.
	if !errs.HasCode(err, corebpf.CodeAttachFailed) && !errs.HasCode(err, corebpf.CodeLoadRefused) {
		t.Fatalf("AttachDevice = %v, want ATTACH_FAILED", err)
	}
}
