package bpf

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"

	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
)

// fakeBackend records what the core hands it.
type fakeBackend struct {
	calls *[]string
}

// Probe records the call.
func (f fakeBackend) Probe() error {
	*f.calls = append(*f.calls, "probe")
	return nil
}

// AttachDevice records the call.
func (f fakeBackend) AttachDevice(cgroup string, _ corebpf.DevicePolicyValue) (corebpf.Program, error) {
	*f.calls = append(*f.calls, "device "+cgroup)
	return nil, nil
}

// AttachSKB records the call.
func (f fakeBackend) AttachSKB(cgroup string, _ corebpf.SKBPolicyValue) (corebpf.SKBProgram, error) {
	*f.calls = append(*f.calls, "skb "+cgroup)
	return nil, nil
}

// AttachBind records the call.
func (f fakeBackend) AttachBind(cgroup string, _ corebpf.BindPolicyValue) (corebpf.Program, error) {
	*f.calls = append(*f.calls, "bind "+cgroup)
	return nil, nil
}

// withRegistry stands a fresh registry in for the test.
func withRegistry(t *testing.T) {
	t.Helper()
	old := registry
	registry = new(plugin.Registry[string, corebpf.Backend])
	t.Cleanup(func() { registry = old })
}

// valid policies, one per kind.
var (
	validDevice = corebpf.DevicePolicyValue{Default: corebpf.Allow}
	validSKB    = corebpf.SKBPolicyValue{Default: corebpf.Allow}
	validBind   = corebpf.BindPolicyValue{Default: corebpf.Allow}
)

// TestWithoutABackendEveryCallIsNotLinked is a program that does not import
// the vendor module: every call is NOT_LINKED, never a silent success — a
// refused policy or path is still refused first.
func TestWithoutABackendEveryCallIsNotLinked(t *testing.T) {
	withRegistry(t)
	_, errDevice := AttachDevice("/sys/fs/cgroup/unit", validDevice)
	_, errSKB := AttachSKB("/sys/fs/cgroup/unit", validSKB)
	_, errBind := AttachBind("/sys/fs/cgroup/unit", validBind)
	//: each verb.
	for name, err := range map[string]error{"Probe": Probe(), "AttachDevice": errDevice, "AttachSKB": errSKB, "AttachBind": errBind} {
		//: NOT_LINKED.
		if !errs.HasCode(err, corebpf.CodeNotLinked) {
			t.Errorf("%s = %v, want NOT_LINKED", name, err)
		}
	}
	_, err := AttachDevice("unit", validDevice)
	//: the path before the backend.
	if !errs.HasCode(err, corebpf.CodePolicyInvalid) {
		t.Fatalf("relative path = %v, want POLICY_INVALID", err)
	}
}

// TestALinkedBackendReceivesWhatTheCoreValidated registers a backend: each
// verb reaches it with the path, and a refused policy never does.
func TestALinkedBackendReceivesWhatTheCoreValidated(t *testing.T) {
	withRegistry(t)
	var calls []string
	b := fakeBackend{calls: &calls}
	Register(b)
	_ = Probe()
	_, _ = AttachDevice("/sys/fs/cgroup/a", validDevice)
	_, _ = AttachSKB("/sys/fs/cgroup/b", validSKB)
	_, _ = AttachBind("/sys/fs/cgroup/c", validBind)
	_, err := AttachBind("/sys/fs/cgroup/d", corebpf.BindPolicyValue{})
	//: the refused policy stays in the core.
	if !errs.HasCode(err, corebpf.CodePolicyInvalid) {
		t.Fatalf("zero policy = %v, want POLICY_INVALID", err)
	}
	want := "probe|device /sys/fs/cgroup/a|skb /sys/fs/cgroup/b|bind /sys/fs/cgroup/c"
	//: the four calls, in order, and nothing for the refused one.
	if got := strings.Join(calls, "|"); got != want {
		t.Fatalf("backend saw %q, want %q", got, want)
	}
}

// TestRegisterRefusesWhatItCannotKeep: a nil backend, a typed nil, and a
// second backend beside the first each panic with BACKEND_REFUSED; the
// same backend twice is idempotent.
func TestRegisterRefusesWhatItCannotKeep(t *testing.T) {
	withRegistry(t)
	var calls []string
	first := fakeBackend{calls: &calls}
	Register(first)
	Register(first)
	var typedNil *fakeBackend
	cases := map[string]corebpf.Backend{
		"nil":       nil,
		"typed-nil": typedNil,
		"second":    fakeBackend{calls: new([]string)},
	}
	//: each refusal.
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				r := recover()
				msg, _ := r.(string)
				//: a panic naming BACKEND_REFUSED.
				if !strings.Contains(msg, "BACKEND_REFUSED") {
					t.Fatalf("panic %v, want BACKEND_REFUSED", r)
				}
			}()
			Register(b)
		})
	}
}
