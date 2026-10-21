package failover

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	corefailover "github.com/kitsunium/sdk/internal/core/observe/logger/middleware/failover"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// failoverSink wraps an ordered list of downstream sinks tried sequentially.
type failoverSink struct {
	// chain is the ordered sink list — first hit returns; misses cascade.
	chain []corelogger.Sink
	// cooldown demotes a failed branch for this long; zero disables it, and
	// Write then never reads the clock or retryAt.
	cooldown time.Duration
	// clk measures cooldown; nil when cooldown is zero.
	clk clock.Clock
	// retryAt holds, per branch, the clock instant (UnixNano) until which the
	// branch is tried last; zero means "in its place". Atomic, because Write
	// runs from every goroutine that logs and a lock would serialise them.
	retryAt []atomic.Int64
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
		return nil, corefailover.Empty
	}
	//: hand back the failover Sink behind the public interface.
	return &failoverSink{chain: cp}, nil
}

// NewWithConfig constructs a failover Sink trying each branch in order, a
// branch that failed demoted to the end of the order until cfg.Cooldown has
// passed on cfg.Clock. A zero Config is New exactly; an empty chain returns
// Empty, as New does.
func NewWithConfig(cfg Config, branches ...corelogger.Sink) (sink corelogger.Sink, err error) {
	//: the chain is New's — the same copy, the same refusal of an empty one.
	sink, err = New(branches...)
	//: no cooldown, or no chain: the sink is New's verdict exactly.
	if err != nil || cfg.Cooldown <= 0 {
		//: New's sink, or Empty.
		return sink, err
	}
	out, isFailover := sink.(*failoverSink)
	//: New only ever returns its own type; the check keeps the assertion safe.
	if !isFailover {
		//: unreachable, but never a panic.
		return sink, nil
	}
	//: a cooldown needs a clock and one deadline per branch; without one the
	//: sink is New's, and Write touches neither.
	out.cooldown = cfg.Cooldown
	out.clk = cfg.Clock
	//: the real clock is a description, not a guess.
	if out.clk == nil {
		out.clk = clock.System
	}
	out.retryAt = make([]atomic.Int64, len(out.chain))
	//: the demoting sink.
	return out, nil
}

// Write tries each branch in order, returning the first success. When every
// branch fails, Write returns Exhausted wrapping the joined failure chain.
func (s *failoverSink) Write(ctx context.Context, rec corelogger.RecordEvent, p []byte) (n int, err error) {
	//: collect per-branch errors so the Exhausted error carries the full picture.
	//: DECLARED NIL, never pre-sized: Write runs once per log record, and the
	//: happy path returns on the FIRST branch without ever appending — yet a
	//: make([]error, 0, len(s.chain)) whose capacity is not a constant escapes
	//: the compiler's implicit stack budget at three branches and heap-
	//: allocates on every write, so a longer fallback chain taxed the healthy
	//: path for failures that never happened. Measured at
	//: internal/service/observe/logger/BENCH.md §0. Flush/Close keep their pre-sized
	//: slates because they run once per sink lifetime, not once per record.
	var collected []error
	//: with a cooldown, the walk demotes the branches that failed lately.
	if s.cooldown > 0 {
		//: the cooled walk returns its own verdict.
		return s.writeCooled(ctx, rec, p)
	}
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
	return 0, exhausted(collected, len(s.chain), rec)
}

// exhausted wraps every branch's failure in the documented sentinel.
func exhausted(collected []error, attempts int, rec corelogger.RecordEvent) error {
	//: the joined chain, so a caller can errors.Is every cause.
	return errs.Wrap(errors.Join(collected...), errs.WrapParams{
		Code:    corefailover.CodeFailoverExhausted,
		Reason:  "FAILOVER_EXHAUSTED",
		Public:  "All failover sinks returned an error",
		Private: "service/observe/logger/middleware/failover.Write exhausted the chain",
	}, errs.Int("attempts", attempts), errs.Int("level", int(rec.Level)))
}

// writeCooled is Write under a cooldown: a first pass in declaration order
// over the branches not cooling down, then — only when all of them failed — a
// second pass over the cooling ones, in the same order. A cooling branch is
// demoted, never skipped, so a cooldown alone never loses a record. The clock
// is read once per Write.
func (s *failoverSink) writeCooled(ctx context.Context, rec corelogger.RecordEvent, p []byte) (n int, err error) {
	now := s.clk.Now().UnixNano()
	//: nil until a branch fails, for the reason Write's slate is.
	var collected []error
	cooling := false
	//: first pass: the branches in their place.
	for i, branch := range s.chain {
		//: demoted until its deadline: the second pass may still reach it.
		if s.retryAt[i].Load() > now {
			cooling = true
			continue
		}
		//: the first success short-circuits, as Write's does.
		if written, ok := s.attempt(ctx, i, branch, rec, p, now, &collected); ok {
			//: accepted.
			return written, nil
		}
	}
	//: second pass, only when there is a demoted branch left to try.
	if cooling {
		//: the demoted branches, in declaration order.
		for i, branch := range s.chain {
			//: the first pass already tried the others.
			if s.retryAt[i].Load() <= now {
				continue
			}
			//: a demoted branch that recovered takes the record.
			if written, ok := s.attempt(ctx, i, branch, rec, p, now, &collected); ok {
				//: accepted.
				return written, nil
			}
		}
	}
	//: every branch was tried and failed.
	return 0, exhausted(collected, len(collected), rec)
}

// attempt writes to branch i, restores it to its place on success and
// demotes it for the cooldown on failure, collecting the failure.
func (s *failoverSink) attempt(ctx context.Context, i int, branch corelogger.Sink, rec corelogger.RecordEvent, p []byte, now int64, collected *[]error) (n int, ok bool) {
	written, werr := branch.Write(ctx, rec, p)
	//: accepted: a demoted branch is back in its place.
	if werr == nil {
		//: a load first, so the healthy path never writes a shared word.
		if s.retryAt[i].Load() != 0 {
			s.retryAt[i].Store(0)
		}
		//: the byte count.
		return written, true
	}
	//: failed: tried last until the cooldown has passed.
	s.retryAt[i].Store(now + int64(s.cooldown))
	*collected = append(*collected, werr)
	//: the walk continues.
	return 0, false
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
