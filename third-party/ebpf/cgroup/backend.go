package cgroup

import (
	"cmp"

	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
	svcbpf "github.com/kitsunium/sdk/internal/service/proc/bpf"
)

// Backend is the cgroup BPF backend this package registers with the
// engine, internal/service/proc/bpf, when it is imported: a program that
// imports it, even blank, is served by the engine's — and pkg/v1/proc/bpf's
// — Probe and Attach functions instead of NOT_LINKED.
var Backend corebpf.Backend = svcbpf.Register(backend{})

// backend implements the port over cilium/ebpf on Linux amd64 and arm64,
// and refuses with UNSUPPORTED_PLATFORM everywhere else.
type backend struct{}

// Probe loads, then releases, the smallest cgroup_device program.
func (backend) Probe() error {
	//: the platform's answer.
	return probe()
}

// AttachDevice attaches a cgroup_device program enforcing policy.
func (backend) AttachDevice(cgroup string, policy corebpf.DevicePolicyValue) (corebpf.Program, error) {
	//: the port may be called directly: validate again, it costs nothing.
	if err := checked(cgroup, policy.Validate()); err != nil {
		//: POLICY_INVALID.
		return nil, err
	}
	//: the platform's attachment.
	return attachDevice(cgroup, policy)
}

// AttachSKB attaches a cgroup_skb program on each hook, enforcing and
// counting policy.
func (backend) AttachSKB(cgroup string, policy corebpf.SKBPolicyValue) (corebpf.SKBProgram, error) {
	//: the port may be called directly: validate again, it costs nothing.
	if err := checked(cgroup, policy.Validate()); err != nil {
		//: POLICY_INVALID.
		return nil, err
	}
	//: the platform's attachment.
	return attachSKB(cgroup, policy)
}

// AttachBind attaches a cgroup_sock_addr program on each bind hook,
// enforcing policy.
func (backend) AttachBind(cgroup string, policy corebpf.BindPolicyValue) (corebpf.Program, error) {
	//: the port may be called directly: validate again, it costs nothing.
	if err := checked(cgroup, policy.Validate()); err != nil {
		//: POLICY_INVALID.
		return nil, err
	}
	//: the platform's attachment.
	return attachBind(cgroup, policy)
}

// checked returns the policy's refusal, else the path's.
func checked(cgroup string, policyErr error) error {
	//: the policy first, as the core does, then the path.
	return cmp.Or(policyErr, corebpf.CheckCgroup(cgroup))
}
