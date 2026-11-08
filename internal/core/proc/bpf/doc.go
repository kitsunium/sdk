// Package bpf is the contract of the BPF programs a supervisor attaches to a
// control group (ADR 0215): the policies as plain values — a device policy
// for cgroup_device, an IP policy with its counters for cgroup_skb, a bind
// policy for cgroup_sock_addr —, their validation, the ports a vendor module
// implements and an attachment satisfies, and the codes of the range
// 0.3.107.*.
//
// The standard library cannot load a BPF program, so the backend is a vendor
// module, third-party/ebpf, over cilium/ebpf, quarantined there (ADR 0157).
// The engine, internal/service/proc/bpf, holds the one backend a program
// links — the one that imports github.com/kitsunium/sdk/third-party/ebpf/cgroup
// — and answers NOT_LINKED in every other program, never a silent no-op.
//
// # What is refused before anything is loaded
//
// A verdict is Allow or Deny, never the zero (ADR 0031), the default
// included: a policy that forgot to say what it decides is refused, not read
// as allow-all or deny-all. Validate names the rule and the field
// (POLICY_INVALID); CheckCgroup refuses a path that is not absolute and clean.
//
// # What the programs decide
//
// A device policy is devices.allow's lines in order, the first that matches
// deciding. An IP policy is a longest-prefix match over the peer's address,
// one map per family, shared by the ingress and the egress programs. A bind
// policy is rules over the family, the transport and a range of local ports,
// in order. Every program is attached with BPF_F_ALLOW_MULTI: it runs beside
// the programs already attached to the group, and an operation passes only
// when every program of the group and of its ancestors lets it through.
//
// # How long a program lives
//
// Until Close, or until the group is removed. The attachment is the
// kernel's, not the process's: a supervisor that exits leaves its services
// under their policies.
package bpf
