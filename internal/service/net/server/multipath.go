package server

import (
	"slices"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// plainTCPListens reports whether a loop group binds its listeners with plain
// TCP rather than Go's default, Multipath TCP on Linux since Go 1.24 (#969):
// it opted into uring-send-zc, the operator did not switch the row off, and
// its plan may run io_uring. An accepted MPTCP socket lacks SOCK_SUPPORT_ZC
// and answers IORING_OP_SEND_ZC with EOPNOTSUPP, after which the loop sends
// every flight plainly: the opt-in would do nothing. Every other group keeps
// the standard library's default, MPTCP included.
func plainTCPListens(group *StreamGroup, plan loopPlan) bool {
	//: io_uring's opt-in, asked and left on, on a plan that may run io_uring.
	return slices.Contains(group.loop.Capabilities, corenet.CapabilityURingSendZC) &&
		!slices.Contains(plan.disabled, corenet.CapabilityURingSendZC) &&
		(plan.backend == corenet.LoopBackendAuto || plan.backend == corenet.LoopBackendIOURing)
}
