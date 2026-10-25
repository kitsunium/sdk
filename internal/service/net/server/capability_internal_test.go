package server

import (
	"runtime"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// capRowOf is ls's row for c, failing the test when there is none.
func capRowOf(t *testing.T, ls corenet.LoopStateValue, c corenet.Capability) corenet.CapabilityValue {
	t.Helper()
	for _, row := range ls.Capabilities {
		if row.Name == c {
			return row
		}
	}
	t.Fatalf("no row %q in %+v", c, ls.Capabilities)
	return corenet.CapabilityValue{}
}

// startLoop starts one loop group under vars with v and returns its State.
func startLoop(t *testing.T, vars map[string]string, v corenet.LoopValue) corenet.StateValue {
	t.Helper()
	srv := newServer(envOf(vars))
	t.Cleanup(func() { swallowErr(srv.Close()) })
	srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Loop(v)).HandleLoop(&loopLineH{})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v %v", err, errs.FieldsOf(err))
	}
	st := srv.State()
	if c := loopDial(t, st.Listeners[0].Address); c == nil {
		t.Fatal("no connection")
	}
	return st
}

// TestCapabilities_report reads the merged report of a loop group on every
// platform: the leaf's rows or the portable backend's, and server's
// tcp-nodelay row last, on by default, disabled by the operator's switch,
// which State names.
func TestCapabilities_report(t *testing.T) {
	t.Parallel()
	st := startLoop(t, nil, corenet.LoopValue{})
	ls := st.Loops[0]
	if row := capRowOf(t, ls, corenet.CapabilityTCPNoDelay); row.Status != statusOn || row.Class != classKernel {
		t.Fatalf("tcp-nodelay = %+v, want on", row)
	}
	if last := ls.Capabilities[len(ls.Capabilities)-1]; last.Name != corenet.CapabilityTCPNoDelay {
		t.Fatalf("server's row is not last: %+v", last)
	}
	if st.Degraded() {
		t.Fatalf("a default report degrades the server: %+v", st.Faults)
	}
	off := startLoop(t, map[string]string{corenet.EnvDisable: "tcp-nodelay,uring-direct"}, corenet.LoopValue{})
	ols := off.Loops[0]
	for _, c := range []corenet.Capability{corenet.CapabilityTCPNoDelay, corenet.CapabilityURingDirect} {
		if row := capRowOf(t, ols, c); row.Status != statusDisabled || row.Reason != corenet.EnvDisable {
			t.Errorf("%s = %+v, want disabled by %s", c, row, corenet.EnvDisable)
		}
	}
	if off.Degraded() {
		t.Fatalf("a switch is an override, not a fault: %+v", off.Faults)
	}
}

// TestCapabilities_unknownTokenLeavesNone pins R5: an unknown token caps the
// engines at portable and switches every optional capability off, each row
// saying so.
func TestCapabilities_unknownTokenLeavesNone(t *testing.T) {
	t.Parallel()
	st := startLoop(t, map[string]string{corenet.EnvDisable: "uring-sqpoll"}, corenet.LoopValue{})
	ls := st.Loops[0]
	if ls.Backend != corenet.LoopBackendPortable {
		t.Fatalf("backend %v, want portable", ls.Backend)
	}
	for _, row := range ls.Capabilities {
		if row.Status != statusDisabled || row.Reason != unknownDisables {
			t.Errorf("%s = %+v, want disabled by the unknown token", row.Name, row)
		}
	}
}

// TestCapabilities_optInRefused refuses an opt-in no backend may honour, on
// every platform, the portable backend included.
func TestCapabilities_optInRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []corenet.Capability{corenet.CapabilityURingSendZC, corenet.CapabilityEpoll, "nope"} {
		srv := newServer(envOf(map[string]string{corenet.EnvEngine: "portable"}))
		srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Loop(corenet.LoopValue{Capabilities: []corenet.Capability{c}})).HandleLoop(&loopLineH{})
		err := srv.Start(t.Context())
		swallowErr(srv.Close())
		if !errs.HasCode(err, corenet.CodeLoopCapabilityRefused) {
			t.Errorf("%q: %v, want LoopCapabilityRefused", c, err)
		}
	}
}

// TestCapabilities_missingIsAFault opts into SQ_REWIND while the operator
// switched NO_SQARRAY, which it requires, off: the row reads missing and
// State reports it as a capability fault, on io_uring.
func TestCapabilities_missingIsAFault(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("io_uring is Linux's")
	}
	st := startLoop(t, map[string]string{corenet.EnvDisable: "uring-no-sqarray"},
		corenet.LoopValue{Capabilities: []corenet.Capability{corenet.CapabilityURingSQRewind}})
	ls := st.Loops[0]
	if ls.Backend != corenet.LoopBackendIOURing {
		t.Skipf("io_uring not running here: %v %q", ls.Backend, ls.DegradedReason)
	}
	if row := capRowOf(t, ls, corenet.CapabilityURingSQRewind); row.Status != statusMissing {
		t.Fatalf("rewind = %+v, want missing", row)
	}
	found := false
	for _, f := range st.Faults {
		if f.Source == faultCapability && f.Group == "loop" && strings.HasPrefix(f.Reason, string(corenet.CapabilityURingSQRewind)) {
			found = true
		}
	}
	if !found || !st.Degraded() {
		t.Fatalf("faults = %+v, want a capability fault", st.Faults)
	}
}
