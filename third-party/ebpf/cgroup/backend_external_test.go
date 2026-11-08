package cgroup_test

import (
	"net/netip"
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
	svcbpf "github.com/kitsunium/sdk/internal/service/proc/bpf"
	"github.com/kitsunium/sdk/third-party/ebpf/cgroup"
)

// supported reports whether this build loads programs: Linux on amd64 and
// arm64, as the package's build constraint says.
func supported() bool {
	return runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64")
}

// TestImportingThePackageLinksTheBackend proves the registration: the engine's
// Probe, which answers NOT_LINKED in a program that does not import this
// package, now reaches the backend — UNSUPPORTED_PLATFORM off Linux amd64
// and arm64, LOAD_REFUSED without the privilege, nil with it.
func TestImportingThePackageLinksTheBackend(t *testing.T) {
	//: the registered backend is this package's.
	if cgroup.Backend == nil {
		t.Fatal("Backend is nil")
	}
	err := svcbpf.Probe()
	//: never NOT_LINKED once the package is imported.
	if errs.HasCode(err, corebpf.CodeNotLinked) {
		t.Fatalf("Probe = %v after the import", err)
	}
	//: off Linux amd64 and arm64, the platform is refused by name.
	if !supported() {
		//: UNSUPPORTED_PLATFORM.
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Fatalf("Probe on %s/%s = %v, want UNSUPPORTED_PLATFORM", runtime.GOOS, runtime.GOARCH, err)
		}
		return
	}
	//: on Linux, nil with the privilege, LOAD_REFUSED without it.
	if err != nil && !errs.HasCode(err, corebpf.CodeLoadRefused) {
		t.Fatalf("Probe = %v, want nil or LOAD_REFUSED", err)
	}
	t.Logf("Probe on this host: %v", err)
}

// TestRefusalsComeBeforeAnyLoad calls the engine's functions and the backend
// directly with a refused policy and a refused path: POLICY_INVALID, on
// every platform, nothing loaded.
func TestRefusalsComeBeforeAnyLoad(t *testing.T) {
	good := corebpf.DevicePolicyValue{Default: corebpf.Allow}
	bad := corebpf.DevicePolicyValue{}
	skb := corebpf.SKBPolicyValue{Default: corebpf.Allow, Rules: []corebpf.PrefixRuleValue{
		{Prefix: netip.MustParsePrefix("::ffff:10.0.0.0/104"), Verdict: corebpf.Deny},
	}}
	bind := corebpf.BindPolicyValue{Default: corebpf.Allow, Rules: []corebpf.BindRuleValue{
		{Family: corebpf.FamilyAny, Transport: corebpf.TransportAny, Ports: corebpf.PortRangeValue{First: 9, Last: 1}, Verdict: corebpf.Deny},
	}}
	calls := map[string]func() error{
		"engine-device-policy":  func() error { _, err := svcbpf.AttachDevice("/sys/fs/cgroup/x", bad); return err },
		"engine-device-path":    func() error { _, err := svcbpf.AttachDevice("relative/x", good); return err },
		"engine-device-root":    func() error { _, err := svcbpf.AttachDevice("/", good); return err },
		"engine-skb-mapped":     func() error { _, err := svcbpf.AttachSKB("/sys/fs/cgroup/x", skb); return err },
		"engine-bind-range":     func() error { _, err := svcbpf.AttachBind("/sys/fs/cgroup/x", bind); return err },
		"backend-device-policy": func() error { _, err := cgroup.Backend.AttachDevice("/sys/fs/cgroup/x", bad); return err },
		"backend-device-path":   func() error { _, err := cgroup.Backend.AttachDevice("/sys/fs/cgroup/../x", good); return err },
		"backend-skb-mapped":    func() error { _, err := cgroup.Backend.AttachSKB("/sys/fs/cgroup/x", skb); return err },
		"backend-bind-range":    func() error { _, err := cgroup.Backend.AttachBind("/sys/fs/cgroup/x", bind); return err },
	}
	//: each call.
	for name, call := range calls {
		//: POLICY_INVALID.
		if err := call(); !errs.HasCode(err, corebpf.CodePolicyInvalid) {
			t.Errorf("%s = %v, want POLICY_INVALID", name, err)
		}
	}
}
