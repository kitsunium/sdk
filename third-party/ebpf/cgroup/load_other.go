//go:build !(linux && (amd64 || arm64))

package cgroup

import (
	"runtime"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
)

// probe refuses: cgroup programs are loaded on Linux amd64 and arm64 only.
func probe() error {
	//: UNSUPPORTED_PLATFORM, never a silent no-op.
	return unsupported()
}

// attachDevice refuses: cgroup programs are loaded on Linux amd64 and arm64
// only.
func attachDevice(string, corebpf.DevicePolicyValue) (corebpf.Program, error) {
	//: UNSUPPORTED_PLATFORM, never a silent no-op.
	return nil, unsupported()
}

// attachSKB refuses: cgroup programs are loaded on Linux amd64 and arm64
// only.
func attachSKB(string, corebpf.SKBPolicyValue) (corebpf.SKBProgram, error) {
	//: UNSUPPORTED_PLATFORM, never a silent no-op.
	return nil, unsupported()
}

// attachBind refuses: cgroup programs are loaded on Linux amd64 and arm64
// only.
func attachBind(string, corebpf.BindPolicyValue) (corebpf.Program, error) {
	//: UNSUPPORTED_PLATFORM, never a silent no-op.
	return nil, unsupported()
}

// unsupported is core/proc's UNSUPPORTED_PLATFORM, the platform named.
func unsupported() error {
	//: the platform as fields, for the log.
	return errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{},
		errs.String("feature", "cgroup-bpf"), errs.String("goos", runtime.GOOS), errs.String("goarch", runtime.GOARCH))
}
