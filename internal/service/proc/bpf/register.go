package bpf

import (
	"github.com/kitsunium/sdk/internal/kernel/plugin"

	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
)

// slot is the one name the backend is registered under: a process links one
// BPF backend or none.
const slot = "cgroup"

// registry holds the backend the vendor module registered at import; a
// pointer, so a test can stand a fresh one in its place.
var registry = new(plugin.Registry[string, corebpf.Backend])

// Register publishes the backend a vendor module implements, at its import,
// and returns it so the module can bind it to a variable. It panics with
// BACKEND_REFUSED on a nil or non-comparable backend, or on a second backend
// beside the first; registering the same backend again changes nothing.
//
// IFACE-PLUGIN: the registry hands the plug-in back behind the port, so the
// vendor's backend keeps its concrete type unexported.
func Register(b corebpf.Backend) corebpf.Backend {
	//: a typed nil and a non-comparable value satisfy the port and cannot serve.
	if why := plugin.Unusable(b); why != "" {
		//: refused at import, where the offender is named.
		panic(corebpf.BackendRefused.Error() + ": " + why)
	}
	//: one backend per process; the same one twice is idempotent.
	if registry.Publish(slot, b) {
		//: a second, different backend.
		panic(corebpf.BackendRefused.Error() + ": a backend is already registered")
	}
	//: bound by the caller to a typed variable.
	return b
}

// Probe reports whether this process may load and attach cgroup programs:
// NOT_LINKED when no backend is linked, UNSUPPORTED_PLATFORM off Linux on
// amd64 and arm64, LOAD_REFUSED when the kernel refuses the smallest
// program — no CAP_BPF, a kernel without cgroup programs —, nil otherwise.
// It attaches nothing; call it once, at wiring time.
func Probe() error {
	b, err := linked()
	//: no backend, nothing to probe.
	if err != nil {
		//: NOT_LINKED.
		return err
	}
	//: the backend's own answer.
	return b.Probe()
}

// AttachDevice validates policy and the path, then attaches a cgroup_device
// program enforcing it to the control group at the directory cgroup — what
// GroupTree.Path returns. POLICY_INVALID and NOT_LINKED are answered before
// anything is loaded.
//
// IFACE-PLUGIN: the attached programs are the linked backend's, behind the
// Program port; the engine has no concrete type to return.
func AttachDevice(cgroup string, policy corebpf.DevicePolicyValue) (corebpf.Program, error) {
	//: the policy and the path, before the backend.
	if err := policy.Validate(); err != nil {
		//: POLICY_INVALID.
		return nil, err
	}
	b, err := backendFor(cgroup)
	//: a refused path or no backend.
	if err != nil {
		//: POLICY_INVALID or NOT_LINKED.
		return nil, err
	}
	//: the backend loads and attaches.
	return b.AttachDevice(cgroup, policy)
}

// AttachSKB validates policy and the path, then attaches a cgroup_skb
// program on ingress and one on egress, enforcing and counting it, to the
// control group at cgroup. POLICY_INVALID and NOT_LINKED are answered before
// anything is loaded.
//
// IFACE-PLUGIN: the attached programs are the linked backend's, behind the
// SKBProgram port; the engine has no concrete type to return.
func AttachSKB(cgroup string, policy corebpf.SKBPolicyValue) (corebpf.SKBProgram, error) {
	//: the policy and the path, before the backend.
	if err := policy.Validate(); err != nil {
		//: POLICY_INVALID.
		return nil, err
	}
	b, err := backendFor(cgroup)
	//: a refused path or no backend.
	if err != nil {
		//: POLICY_INVALID or NOT_LINKED.
		return nil, err
	}
	//: the backend loads and attaches.
	return b.AttachSKB(cgroup, policy)
}

// AttachBind validates policy and the path, then attaches a
// cgroup_sock_addr program on each bind hook, enforcing it, to the control
// group at cgroup. POLICY_INVALID and NOT_LINKED are answered before
// anything is loaded.
//
// IFACE-PLUGIN: the attached programs are the linked backend's, behind the
// Program port; the engine has no concrete type to return.
func AttachBind(cgroup string, policy corebpf.BindPolicyValue) (corebpf.Program, error) {
	//: the policy and the path, before the backend.
	if err := policy.Validate(); err != nil {
		//: POLICY_INVALID.
		return nil, err
	}
	b, err := backendFor(cgroup)
	//: a refused path or no backend.
	if err != nil {
		//: POLICY_INVALID or NOT_LINKED.
		return nil, err
	}
	//: the backend loads and attaches.
	return b.AttachBind(cgroup, policy)
}

// backendFor checks the path, then returns the linked backend.
//
// IFACE-PLUGIN: the backend is the plug-in the vendor module registered.
func backendFor(cgroup string) (corebpf.Backend, error) {
	//: the path first: a refused path is the caller's to fix, linked or not.
	if err := corebpf.CheckCgroup(cgroup); err != nil {
		//: POLICY_INVALID.
		return nil, err
	}
	//: the backend, or NOT_LINKED.
	return linked()
}

// linked returns the registered backend, or NOT_LINKED.
//
// IFACE-PLUGIN: the backend is the plug-in the vendor module registered.
func linked() (corebpf.Backend, error) {
	b, ok := registry.Lookup(slot)
	//: nothing imported the vendor module.
	if !ok {
		//: never a silent no-op.
		return nil, corebpf.NotLinked
	}
	//: the backend.
	return b, nil
}
