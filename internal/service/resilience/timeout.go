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
	//: a deadline hit (and op surfacing it) maps to the typed timeout sentinel.
	if errors.Is(err, context.DeadlineExceeded) || (err != nil && dctx.Err() == context.DeadlineExceeded) {
		//: relabel as TIMEOUT_EXCEEDED, keeping the cause in the trail.
		return wrapAs(coreres.TimeoutExceeded, err)
	}
	//: otherwise propagate the operation's own outcome.
	return err
}
