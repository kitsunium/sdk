// Package failover implements a sequential-retry Sink that tries downstream
// sinks in order until one succeeds. The first successful Write returns
// nil; if every sink fails, Write returns Exhausted wrapping an
// errors.Join of the per-sink failures.
//
// Use case: emit logs to a primary collector with a local file as a
// fallback so logs survive a network outage.
package failover

import (
	"context"
	"errors"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// failoverSink wraps an ordered list of downstream sinks tried sequentially.
type failoverSink struct {
	// chain is the ordered sink list — first hit returns; misses cascade.
	chain []corelogger.Sink
}

// New constructs a failover Sink trying each branch in order.
func New(branches ...corelogger.Sink) (sink corelogger.Sink, err error) {
	//: defensive copy that drops nil entries so the chain stays clean.
	cp := make([]corelogger.Sink, 0, len(branches))
	//: walk the supplied list once; nil branches are silently skipped.
	for _, branch := range branches {
		//: skip nil entries so callers can pass a sparse slate.
		if branch == nil {
			//: documented contract: nil branches are not errors.
			continue
		}
		cp = append(cp, branch)
	}
	//: reject empty chains — failover with no targets cannot accept any write.
	if len(cp) == 0 {
		//: documented sentinel — caller must supply at least one branch.
		return nil, Empty
	}
	//: hand back the failover Sink behind the public interface.
	return &failoverSink{chain: cp}, nil
}

// Write tries each branch in order, returning the first success. When every
// branch fails, Write returns Exhausted wrapping the joined failure chain.
func (s *failoverSink) Write(ctx context.Context, rec corelogger.RecordEvent, p []byte) (n int, err error) {
	//: collect per-branch errors so the Exhausted error carries the full picture.
	collected := make([]error, 0, len(s.chain))
	//: walk the chain in order; first success short-circuits the loop.
	for _, branch := range s.chain {
		//: try the branch and capture either success or failure.
		written, werr := branch.Write(ctx, rec, p)
		//: short-circuit on the first success — this is the failover contract.
		if werr == nil {
			//: hand back the byte count plus a clean error.
			return written, nil
		}
		//: append the failure to the join set and try the next branch.
		collected = append(collected, werr)
	}
	//: every branch failed — wrap the joined chain in the documented sentinel.
	return 0, errs.Wrap(errors.Join(collected...), errs.WrapParams{
		Code:    CodeFailoverExhausted,
		Reason:  "FAILOVER_EXHAUSTED",
		Public:  "All failover sinks returned an error",
		Private: "service/logger/middleware/failover.Write exhausted the chain",
	}, errs.Int("attempts", len(s.chain)), errs.Int("level", int(rec.Level)))
}

// Flush forwards to every distinct branch and aggregates per-branch errors via
// errors.Join. A sink reused across multiple chain slots (V33) is flushed once.
func (s *failoverSink) Flush(ctx context.Context) error {
	//: collect per-branch errors so callers see every failure, not just the first.
	collected := make([]error, 0, len(s.chain))
	//: dedup so a shared sink instance is not double-flushed (double fsync).
	seen := make(map[corelogger.Sink]struct{}, len(s.chain))
	//: walk every branch in order; failures are captured but never short-circuit.
	for _, branch := range s.chain {
		//: skip a branch pointer already flushed via an earlier slot.
		if _, dup := seen[branch]; dup {
			continue
		}
		seen[branch] = struct{}{}
		//: every distinct branch sees the flush regardless of upstream failures.
		if ferr := branch.Flush(ctx); ferr != nil {
			//: append the failure to the join set without short-circuiting.
			collected = append(collected, ferr)
		}
	}
	//: aggregate the per-branch failures via errors.Join when any branch failed.
	if len(collected) > 0 {
		//: surface the joined chain so callers can errors.Is each cause.
		return errors.Join(collected...)
	}
	//: happy path — every branch flushed cleanly.
	return nil
}

// Close forwards to every distinct branch and aggregates per-branch errors via
// errors.Join. A sink reused across multiple chain slots (V33) is closed once so
// a non-idempotent terminal sink does not surface a spurious double-close error.
func (s *failoverSink) Close() error {
	//: collect per-branch errors so callers see every failure, not just the first.
	collected := make([]error, 0, len(s.chain))
	//: dedup so a shared sink instance is not double-closed (os.ErrClosed).
	seen := make(map[corelogger.Sink]struct{}, len(s.chain))
	//: walk every branch in order; failures are captured but never short-circuit.
	for _, branch := range s.chain {
		//: skip a branch pointer already closed via an earlier slot.
		if _, dup := seen[branch]; dup {
			continue
		}
		seen[branch] = struct{}{}
		//: every distinct branch sees the close regardless of upstream failures.
		if cerr := branch.Close(); cerr != nil {
			//: append the failure to the join set without short-circuiting.
			collected = append(collected, cerr)
		}
	}
	//: aggregate the per-branch failures via errors.Join when any branch failed.
	if len(collected) > 0 {
		//: surface the joined chain so callers can errors.Is each cause.
		return errors.Join(collected...)
	}
	//: happy path — every branch closed cleanly.
	return nil
}
