// Package bpf attaches BPF programs to a control group: a device policy
// (cgroup_device), an IP policy that also counts the traffic (cgroup_skb),
// a bind policy (cgroup_sock_addr) — ADR 0215.
//
//	import _ "github.com/kitsunium/sdk/third-party/ebpf/cgroup" // links the backend
//
//	p, err := bpf.AttachDevice(tree.Path(), bpf.DevicePolicy{
//		Default: bpf.Deny,
//		Rules: []bpf.DeviceRule{
//			{Type: bpf.DeviceChar, Major: 1, Minor: 3, Access: bpf.AccessAll, Verdict: bpf.Allow},
//		},
//	})
//
// # The backend is linked by an import
//
// Loading a BPF program takes a library the standard library does not have,
// so the backend is the vendor module third-party/ebpf, over cilium/ebpf. A
// program that imports github.com/kitsunium/sdk/third-party/ebpf/cgroup —
// a blank import is enough — links it; any other program links nothing of
// cilium/ebpf, and every call here answers [NotLinked]. Off Linux on amd64
// and arm64 the backend answers [UnsupportedPlatform]; without CAP_BPF, or
// on a kernel without cgroup programs, [LoadRefused]. Nothing is ever a
// silent no-op. Call [Probe] once, at wiring time, to learn which.
//
// # What is refused before anything is loaded
//
// A policy is validated first ([PolicyInvalid], the fields naming the rule
// and the field): a verdict is [Allow] or [Deny] and never the zero, the
// default included, so a policy that does not say what it decides is
// refused rather than read as allow-all or deny-all. The group is an
// absolute directory on cgroup v2 below the hierarchy's root — what a
// GroupTree's Path returns —; another one is [AttachFailed].
//
// # Beside the other programs, and for how long
//
// Every program is attached with BPF_F_ALLOW_MULTI: an operation passes
// only when every program of the group and of its ancestors lets it
// through. A program stays attached until its [Program].Close, or until the
// group is removed: the supervisor may exit first, and its services stay
// under their policies.
package bpf
