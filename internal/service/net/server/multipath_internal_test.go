package server

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestPlainTCPListens pins which loop group binds plain TCP rather than Go's
// default Multipath TCP (#969): one that opted into uring-send-zc, the row
// left on, on a plan that may run io_uring — auto or io_uring. A group that
// did not opt in, one whose row the operator switched off, and one capped at
// epoll or the portable backend keep the standard library's default.
func TestPlainTCPListens(t *testing.T) {
	t.Parallel()
	zc := []corenet.Capability{corenet.CapabilityURingSendZC}
	for _, tc := range []struct {
		name string
		caps []corenet.Capability
		plan loopPlan
		want bool
	}{
		{"opted-in-auto", zc, loopPlan{backend: corenet.LoopBackendAuto}, true},
		{"opted-in-iouring", zc, loopPlan{backend: corenet.LoopBackendIOURing}, true},
		{"not-opted-in", nil, loopPlan{backend: corenet.LoopBackendAuto}, false},
		{"other-opt-in", []corenet.Capability{corenet.CapabilityBusyPoll}, loopPlan{backend: corenet.LoopBackendIOURing}, false},
		{"switched-off", zc, loopPlan{backend: corenet.LoopBackendAuto, disabled: zc}, false},
		{"epoll", zc, loopPlan{backend: corenet.LoopBackendEpoll}, false},
		{"portable", zc, loopPlan{backend: corenet.LoopBackendPortable}, false},
		{"kqueue", zc, loopPlan{backend: corenet.LoopBackendKqueue}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			group := &StreamGroup{loop: corenet.LoopValue{Capabilities: tc.caps}}
			if got := plainTCPListens(group, tc.plan); got != tc.want {
				t.Fatalf("plainTCPListens = %v, want %v", got, tc.want)
			}
		})
	}
}
