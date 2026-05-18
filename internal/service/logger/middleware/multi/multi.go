// Package multi implements a fanout Sink that broadcasts each Write to a
// fixed list of downstream sinks. A single failure does NOT short-circuit
// the fanout — every sink receives the payload, and the per-sink errors
// are aggregated via errors.Join so callers see the complete failure set.
//
// Use case: emit logs to console + file + remote drain simultaneously, with
// independent failure modes per branch.
package multi

import (
	"context"
	"errors"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fanoutSink dispatches every Write call to all branch sinks in order. The
// branches slice is owned by the fanout sink — callers MUST NOT mutate it
// after construction.
type fanoutSink struct {
	// branches receives every Write / Flush / Close invocation in order.
	branches []corelogger.Sink
}

// New constructs a fanout Sink that broadcasts to every sink in branches.
// A nil or empty branches slice is treated as a no-op sink — every Write
// returns nil and Flush / Close are no-ops.
//
// Params:
//   - branches: downstream sinks; nil entries are silently skipped at Write time.
//
// Returns:
//   - sink: the fanout Sink behind the public corelogger.Sink interface.
func New(branches ...corelogger.Sink) corelogger.Sink {
	//: defensive copy so post-construction mutation by the caller is harmless.
	cp := make([]corelogger.Sink, 0, len(branches))
	//: walk the supplied list once, dropping nil entries on the way through.
	for _, b := range branches {
		//: skip nil entries so callers can pass a sparse slate.
		if b == nil {
			continue
		}
		cp = append(cp, b)
	}
	//: hand back the fanout sink behind the public Sink interface.
	return &fanoutSink{branches: cp}
}

// Write dispatches p to every branch sink in order and aggregates any
// per-sink failures via errors.Join wrapped in the FanoutWriteFailed
// sentinel.
//
// Params:
//   - ctx: request-scoped context forwarded to every branch.
//   - r: originating record forwarded to every branch.
//   - p: formatted bytes forwarded to every branch.
//
// Returns:
//   - n: bytes accepted by the LAST successful branch; informational only.
//   - err: FanoutWriteFailed wrapping the joined per-sink errors; nil on
//     unanimous success.
func (s *fanoutSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: collect per-branch errors so callers see every failure, not just the first.
	errs2 := make([]error, 0, len(s.branches))
	//: walk every branch in order; failures are captured but never short-circuit.
	for _, b := range s.branches {
		//: every branch sees the payload regardless of upstream failures.
		written, werr := b.Write(ctx, r, p)
		//: track the byte count of the latest accepting branch.
		if werr == nil {
			n = written
			continue
		}
		//: append the failure to the join set without short-circuiting.
		errs2 = append(errs2, werr)
	}
	//: aggregate the per-branch failures via errors.Join when any branch failed.
	if len(errs2) > 0 {
		//: wrap with the documented sentinel for HasCode-style introspection.
		return n, errs.Wrap(errors.Join(errs2...), errs.WrapParams{
			Code:    CodeFanoutWriteFailed,
			Reason:  "FANOUT_WRITE_FAILED",
			Public:  "One or more fan-out sinks failed",
			Private: "service/logger/middleware/multi.Write aggregated per-sink errors",
		}, errs.Int("failed", len(errs2)), errs.Int("level", int(r.Level)))
	}
	//: happy path — every branch accepted the payload.
	return n, nil
}

// Flush forwards the call to every branch and aggregates per-branch errors.
//
// Params:
//   - ctx: request-scoped context forwarded to every branch.
//
// Returns:
//   - err: errors.Join of the per-branch failures; nil on unanimous success.
func (s *fanoutSink) Flush(ctx context.Context) error {
	//: collect per-branch errors so callers see every failure, not just the first.
	errs2 := make([]error, 0, len(s.branches))
	//: walk every branch in order; failures are captured but never short-circuit.
	for _, b := range s.branches {
		//: every branch sees the flush regardless of upstream failures.
		if ferr := b.Flush(ctx); ferr != nil {
			//: append the failure to the join set without short-circuiting.
			errs2 = append(errs2, ferr)
		}
	}
	//: aggregate the per-branch failures via errors.Join when any branch failed.
	if len(errs2) > 0 {
		//: surface the joined chain so callers can errors.Is each branch's cause.
		return errors.Join(errs2...)
	}
	//: happy path — every branch flushed cleanly.
	return nil
}

// Close forwards the call to every branch and aggregates per-branch errors.
//
// Returns:
//   - err: errors.Join of the per-branch failures; nil on unanimous success.
func (s *fanoutSink) Close() error {
	//: collect per-branch errors so callers see every failure, not just the first.
	errs2 := make([]error, 0, len(s.branches))
	//: walk every branch in order; failures are captured but never short-circuit.
	for _, b := range s.branches {
		//: every branch sees the close regardless of upstream failures.
		if cerr := b.Close(); cerr != nil {
			//: append the failure to the join set without short-circuiting.
			errs2 = append(errs2, cerr)
		}
	}
	//: aggregate the per-branch failures via errors.Join when any branch failed.
	if len(errs2) > 0 {
		//: surface the joined chain so callers can errors.Is each branch's cause.
		return errors.Join(errs2...)
	}
	//: happy path — every branch closed cleanly.
	return nil
}
