// Package server — the per-group connection ceiling.
package server

import (
	"context"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// connLimiter caps how many connections a group serves at once.
//
// The bulkhead from the resilience domain is reused rather than reimplemented:
// it is exactly a reject-mode channel semaphore, which is what a connection
// ceiling is. Reusing it means the rejection semantics are the ones the rest of
// the SDK already documents, and the ceiling is carried alongside so a refusal
// can name the number it hit.
type connLimiter struct {
	// runner admits or rejects; nil is never stored, the whole struct is.
	runner coreres.Runner
	// ceiling is the configured maximum, reported in the refusal.
	ceiling int
}

// newConnLimiter returns the group's ceiling, or nil when it has none.
func newConnLimiter(limits corenet.LimitsValue) *connLimiter {
	//: zero means "no ceiling", so the hot path skips the policy entirely.
	if limits.MaxConns <= 0 {
		//: no ceiling, so the hot path skips the policy entirely.
		return nil
	}
	//: the ceiling is carried alongside so a refusal can name the number it hit.
	return &connLimiter{
		runner:  svcres.NewBulkhead(limits.MaxConns),
		ceiling: limits.MaxConns,
	}
}

// admit runs the handler under the group's ceiling, translating a rejection
// into the domain's own sentinel.
//
// A connection turned away here has already been accepted by the kernel, so it
// is answered by closing it immediately rather than by letting the accept queue
// overflow silently — a refusal the peer can observe beats a timeout it cannot
// distinguish from a hang.
func (s *Server) admit(ctx context.Context, limiter *connLimiter, c corenet.Conn, handler corenet.ConnHandler) error {
	//: no ceiling configured — call the handler directly, with no closure and
	//: no policy on the hot path.
	if limiter == nil {
		//: straight to the handler, with no closure on the hot path.
		return handler.ServeConn(ctx, c)
	}
	err := limiter.runner.Run(ctx, func(runCtx context.Context) error {
		//: the slot is held for exactly as long as the handler runs.
		return handler.ServeConn(runCtx, c)
	})
	//: the ceiling rejected this connection.
	if errs.HasCode(err, coreres.CodeBulkheadFull) {
		s.rejected.Add(1)
		//: report in the domain's terms, so a net consumer never meets a
		//: resilience sentinel it has no reason to know about.
		return errs.Wrap(corenet.ConnLimitReached, errs.WrapParams{},
			errs.Int("limit", limiter.ceiling))
	}
	//: whatever the handler itself produced.
	return err
}
