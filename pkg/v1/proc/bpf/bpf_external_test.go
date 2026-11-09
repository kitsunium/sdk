package bpf_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	"github.com/kitsunium/sdk/pkg/v1/proc/bpf"
)

// TestAProgramWithoutTheVendorModuleIsNotLinked is a consumer that imports
// the facade and not third-party/ebpf/cgroup: every call is NOT_LINKED — a
// typed refusal, never a silent no-op — and the policy is still validated
// first.
func TestAProgramWithoutTheVendorModuleIsNotLinked(t *testing.T) {
	policy := bpf.DevicePolicy{Default: bpf.Deny, Rules: []bpf.DeviceRule{
		{Type: bpf.DeviceChar, Major: 1, Minor: 3, Access: bpf.AccessAll, Verdict: bpf.Allow},
	}}
	_, errDevice := bpf.AttachDevice("/sys/fs/cgroup/unit", policy)
	_, errSKB := bpf.AttachSKB("/sys/fs/cgroup/unit", bpf.SKBPolicy{Default: bpf.Allow})
	_, errBind := bpf.AttachBind("/sys/fs/cgroup/unit", bpf.BindPolicy{Default: bpf.Allow})
	//: each verb.
	for name, err := range map[string]error{"Probe": bpf.Probe(), "AttachDevice": errDevice, "AttachSKB": errSKB, "AttachBind": errBind} {
		//: NOT_LINKED, matched through the facade's code.
		if !errs.HasCode(err, bpf.CodeNotLinked) {
			t.Errorf("%s = %v, want NOT_LINKED", name, err)
		}
	}
	_, err := bpf.AttachDevice("/sys/fs/cgroup/unit", bpf.DevicePolicy{})
	//: the zero policy is refused before the backend is looked for.
	if !errs.HasCode(err, bpf.CodePolicyInvalid) {
		t.Fatalf("zero policy = %v, want POLICY_INVALID", err)
	}
}
