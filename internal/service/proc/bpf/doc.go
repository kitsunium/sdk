// Package bpf is the engine of the cgroup BPF programs (ADR 0215): it holds
// the one backend a program links and hands it the policies
// core/proc/bpf validated.
//
// A vendor module, third-party/ebpf/cgroup over cilium/ebpf, calls Register
// when it is imported; a program that does not import it links nothing of
// cilium/ebpf, and Probe and every Attach answer NOT_LINKED. Each Attach
// validates the policy, then the path, then looks for the backend, so
// POLICY_INVALID is answered whether a backend is linked or not. The registry
// is kernel/plugin's (ADR 0159), one slot.
package bpf
