// Package tee — the TeeSink decorator that fans each record out to every
// primary sink and spills to a dead-letter sink only when all primaries fail.
package tee

import (
	"context"
	"errors"
	"slices"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TeeSink fans every record out to all primary sinks. When — and only when —
// every primary rejects the record, it routes that record to the optional
// spill (dead-letter) sink. A record accepted by at least one primary is
// never spilled.
type TeeSink struct {
	// primaries receive every Write; a record they all reject is spilled.
	primaries []corelogger.Sink
	// spill is the optional dead-letter sink; nil disables the spill seam.
	spill corelogger.Sink
}

// NewTeeSink builds a TeeSink from cfg. The primaries slice is copied so
// later caller mutation cannot affect the sink. A nil cfg.Spill disables the
// dead-letter seam.
func NewTeeSink(cfg Config) *TeeSink {
	//: defensive copy so post-construction mutation by the caller is harmless.
	cp := slices.Clone(cfg.Primaries)
	//: hand back the fan-out sink wrapping the configured primaries and spill.
	return &TeeSink{primaries: cp, spill: cfg.Spill}
}

// Write fans r out to every primary sink. It returns the byte count of the
// first primary that accepted the record. When all primaries fail, the record
// is routed to the spill sink and a CodeTeeAllBranchesFailed error wrapping
// the joined causes is returned; if the spill also fails, CodeSpillFailed is
// returned instead.
func (t *TeeSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	n, accepted, causes := t.fanOut(ctx, r, p)
	//: at least one primary accepted the record — no spill needed.
	if accepted {
		//: hand back the first accepting byte count with a clean error.
		return n, nil
	}
	//: every primary failed — route to spill and map the outcome to a code.
	return 0, t.handleAllFailed(ctx, r, p, errors.Join(causes...))
}

// fanOut delivers the record to every primary, returning the first accepted
// byte count, whether any primary accepted it, and the collected causes.
func (t *TeeSink) fanOut(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, accepted bool, causes []error) {
	//: deliver to every primary; collect errors without short-circuiting.
	for _, s := range t.primaries {
		written, werr := s.Write(ctx, r, p)
		//: a primary failure is recorded but does not stop fan-out.
		if werr != nil {
			causes = append(causes, werr)
			continue
		}
		accepted = true
		//: remember the first successful byte count.
		if n == 0 {
			n = written
		}
	}
	//: return the aggregate fan-out outcome to the caller.
	return n, accepted, causes
}

// handleAllFailed routes a record that every primary rejected to the spill
// sink and maps the outcome to a typed error.
func (t *TeeSink) handleAllFailed(ctx context.Context, r corelogger.RecordEvent, p []byte, joined error) error {
	//: with no spill configured the joined primary causes are surfaced.
	if t.spill == nil {
		//: surface the all-branches sentinel wrapping the joined causes.
		return wrapAllBranchesFailed(joined)
	}
	//: a spill failure supersedes the primary failure in the report.
	if _, serr := t.spill.Write(ctx, r, p); serr != nil {
		//: wrap the joined primary causes plus the spill cause under SpillFailed.
		return wrapSpillFailed(errors.Join(joined, serr))
	}
	//: spill accepted the dead-letter record; still report the primary failure.
	return wrapAllBranchesFailed(joined)
}

// wrapAllBranchesFailed wraps cause under CodeTeeAllBranchesFailed so callers
// can match it with errs.HasCode(err, CodeTeeAllBranchesFailed).
func wrapAllBranchesFailed(cause error) error {
	//: stamp the typed code while preserving the wrapped cause chain.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeTeeAllBranchesFailed,
		Reason:  "ALL_BRANCHES_FAILED",
		Public:  "every primary sink rejected the record",
		Private: "service/logger/middleware/tee: all primary sinks failed to write the record",
	})
}

// wrapSpillFailed wraps cause under CodeSpillFailed so callers can match it
// with errs.HasCode(err, CodeSpillFailed).
func wrapSpillFailed(cause error) error {
	//: stamp the typed code while preserving the wrapped cause chain.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeSpillFailed,
		Reason:  "SPILL_FAILED",
		Public:  "failed to spill record to the dead-letter sink",
		Private: "service/logger/middleware/tee: the dead-letter spill sink rejected the record",
	})
}

// Flush forwards the flush to every primary and the spill sink, aggregating
// any errors with errors.Join.
func (t *TeeSink) Flush(ctx context.Context) error {
	var causes []error
	//: flush every primary; collect rather than short-circuit.
	for _, s := range t.primaries {
		//: a nil error keeps the aggregate clean.
		if cerr := s.Flush(ctx); cerr != nil {
			causes = append(causes, cerr)
		}
	}
	//: the spill sink is optional; flush it only when configured.
	if t.spill != nil {
		//: a nil error keeps the aggregate clean.
		if cerr := t.spill.Flush(ctx); cerr != nil {
			causes = append(causes, cerr)
		}
	}
	//: errors.Join returns nil when no branch failed.
	return errors.Join(causes...)
}

// Close closes every primary and the spill sink, aggregating any errors with
// errors.Join.
func (t *TeeSink) Close() error {
	var causes []error
	//: close every primary; collect rather than short-circuit.
	for _, s := range t.primaries {
		//: a nil error keeps the aggregate clean.
		if cerr := s.Close(); cerr != nil {
			causes = append(causes, cerr)
		}
	}
	//: the spill sink is optional; close it only when configured.
	if t.spill != nil {
		//: a nil error keeps the aggregate clean.
		if cerr := t.spill.Close(); cerr != nil {
			causes = append(causes, cerr)
		}
	}
	//: errors.Join returns nil when no branch failed.
	return errors.Join(causes...)
}
