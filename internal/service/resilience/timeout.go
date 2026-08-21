// Package resilience provides the concrete reliability policies (retry,
// circuit-breaker, rate-limit, bulkhead, timeout) implementing
// core/resilience.Runner. Each constructor returns a Runner; policies compose by
// nesting. ADR 0026. Cross-OS: 100% portable (context, time, sync, atomic).
package resilience

import (
	"context"
	"errors"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
)

// timeoutRunner caps an Operation's wall-clock with a context deadline.
type timeoutRunner struct {
	timeout time.Duration
}

// NewTimeout returns a Runner that fails an Operation with TimeoutExceeded if it
// does not finish within d. The Operation MUST honour ctx for the deadline to
// fire promptly (a ctx-ignoring op caps precision at op granularity).
func NewTimeout(d time.Duration) coreres.Runner {
	//: a stateless value runner — safe to share.
	return timeoutRunner{timeout: d}
}

// Run executes op under a derived deadline.
func (t timeoutRunner) Run(ctx context.Context, op coreres.Operation) error {
	//: derive a deadline-bound context; cancel releases its resources.
	dctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	//: run the operation against the deadline-bound context.
	err := op(dctx)
	//: the deadline is authoritative regardless of what op returned. An op that
	//: ignores ctx and reports success AFTER the deadline expired must not be
	//: relabelled as success — that silently voids the policy, which is exactly
	//: the "op granularity" precision the doc promises to degrade to, not to
	//: abandon. Checking dctx.Err() unconditionally (rather than only when
	//: err != nil) is what makes the late-success case fail honestly.
	if dctx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
		//: relabel as TIMEOUT_EXCEEDED, keeping the cause in the trail.
		return wrapAs(coreres.TimeoutExceeded, err)
	}
	//: otherwise propagate the operation's own outcome.
	return err
}
