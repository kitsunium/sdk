//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — what an io_uring stop issues: a shutdown a connection and no
// cancel by descriptor, counted on the SQEs the kernel took, whatever the
// machine's speed.
package loop

import (
	"sync/atomic"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// stopSQEs counts, over a group's life, the SQEs its rings handed the kernel
// that tell a linear close from the quadratic one ADR 0200 §D2 removed.
type stopSQEs struct {
	// shutdowns are IORING_OP_SHUTDOWN: one a close.
	shutdowns atomic.Int64
	// byDescriptor are cancels by descriptor (IORING_ASYNC_CANCEL_FD): each
	// walks every armed request of the ring, so one a close is n².
	byDescriptor atomic.Int64
	// linked are SQEs hard-linked to the next: the old close chained its
	// cancel behind the shutdown, into io-wq.
	linked atomic.Int64
}

// stopGuardOf counts io_uring's stop; the other backends issue no SQE.
func stopGuardOf(b backendCase) stopGuard {
	//: only io_uring has SQEs to count.
	if b.backend != corenet.LoopBackendIOURing {
		//: the time bound alone.
		return nil
	}
	//: counted.
	return &stopSQEs{}
}

// set implements stopGuard: every ring of the group reports what it submits.
func (s *stopSQEs) set(c *Config) {
	c.hooks.taken = s.taken
}

// taken counts one SQE the kernel took, on a loop's thread.
func (s *stopSQEs) taken(op, flags uint8, opFlags uint32) {
	//: the link, whatever the operation.
	if flags&sqeIOHardlink != 0 {
		s.linked.Add(1)
	}
	//: by operation.
	switch {
	//: a close.
	case op == opShutdown:
		s.shutdowns.Add(1)
	//: a cancel that looks the descriptor up in every armed request.
	case op == opAsyncCancel && opFlags&cancelFD != 0:
		s.byDescriptor.Add(1)
	}
}

// check implements stopGuard: conns connections were stopped, each by one
// shutdown and nothing walking the ring, so the stop is linear by
// construction — a regression to the linked cancel fails here at any speed.
func (s *stopSQEs) check(t *testing.T, conns int) {
	t.Helper()
	shutdowns, byDescriptor, linked := s.shutdowns.Load(), s.byDescriptor.Load(), s.linked.Load()
	t.Logf("stop SQEs: %d shutdowns, %d cancels by descriptor, %d linked", shutdowns, byDescriptor, linked)
	if byDescriptor != 0 {
		t.Errorf("the stop issued %d cancels by descriptor, want none: each walks every armed request (ADR 0200 §D2)", byDescriptor)
	}
	if linked != 0 {
		t.Errorf("the stop issued %d hard-linked SQEs, want none: a close is a shutdown alone (ADR 0200 §D2)", linked)
	}
	if shutdowns != int64(conns) {
		t.Errorf("the stop issued %d shutdowns for %d connections, want one each", shutdowns, conns)
	}
}
