// Package route implements a predicate-based router Sink. Records are
// dispatched to the first downstream sink whose Predicate returns true
// for the record. When no predicate matches, the optional fallback sink
// receives the write; without a fallback, Write returns NoMatch.
//
// Use case: send error-level records to a remote alerting drain while
// keeping info-level records local.
package route

import (
	"context"
	"errors"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// routerSink dispatches each Write to the first matching route entry.
type routerSink struct {
	// entries is the ordered table evaluated on every Write.
	entries []Params
	// fallback receives writes that match no predicate; nil triggers NoMatch.
	fallback corelogger.Sink
}

// New constructs a router Sink from the supplied entries and optional
// fallback Sink. The entries slice is defensively copied so post-
// construction mutation by the caller is harmless.
//
// Params:
//   - fallback: catch-all sink; nil makes Write return NoMatch on misses.
//   - entries: predicate / sink pairs evaluated in order.
//
// Returns:
//   - corelogger.Sink: the router Sink behind the public interface.
func New(fallback corelogger.Sink, entries ...Params) corelogger.Sink {
	//: defensive copy — the router owns its entries table.
	cp := make([]Params, 0, len(entries))
	//: walk the supplied list once, dropping invalid entries on the way.
	for _, entry := range entries {
		//: skip nil predicates / sinks so callers can pass partial entries.
		if entry.When == nil || entry.Sink == nil {
			//: documented contract: incomplete entries are silently ignored.
			continue
		}
		cp = append(cp, entry)
	}
	//: hand back the router behind the public Sink interface.
	return &routerSink{entries: cp, fallback: fallback}
}

// Write dispatches p to the first matching entry or to the fallback sink.
//
// Params:
//   - ctx: request-scoped context forwarded to the matching sink.
//   - rec: originating record consulted by the predicates.
//   - p: formatted bytes forwarded to the matching sink.
//
// Returns:
//   - n: bytes accepted by the matching sink (0 on NoMatch).
//   - err: the matching sink's error; NoMatch when no predicate hits.
func (s *routerSink) Write(ctx context.Context, rec corelogger.RecordEvent, p []byte) (n int, err error) {
	//: walk entries in order; first match wins.
	for _, entry := range s.entries {
		//: predicate guards delegation to the bound sink.
		if entry.When(rec) {
			//: hand the write to the matching downstream sink.
			return entry.Sink.Write(ctx, rec, p)
		}
	}
	//: fall back to the catch-all sink when one is configured.
	if s.fallback != nil {
		//: forward to the catch-all so writes never silently disappear.
		return s.fallback.Write(ctx, rec, p)
	}
	//: documented sentinel — caller knows no entry was wired for this record.
	return 0, NoMatch
}

// Flush forwards to every entry + fallback and aggregates per-sink errors.
//
// Params:
//   - ctx: request-scoped context forwarded to every sink.
//
// Returns:
//   - err: errors.Join of per-sink failures; nil on unanimous success.
func (s *routerSink) Flush(ctx context.Context) error {
	//: collect per-sink errors so callers see every failure, not just the first.
	collected := make([]error, 0, len(s.entries)+1)
	//: walk every entry in order; failures are captured but never short-circuit.
	for _, entry := range s.entries {
		//: every entry sees the flush regardless of upstream failures.
		if ferr := entry.Sink.Flush(ctx); ferr != nil {
			//: append the failure to the join set.
			collected = append(collected, ferr)
		}
	}
	//: also flush the fallback when it exists.
	if s.fallback != nil {
		//: include the fallback in the joined error set.
		if ferr := s.fallback.Flush(ctx); ferr != nil {
			//: append the fallback failure too.
			collected = append(collected, ferr)
		}
	}
	//: aggregate the per-sink failures via errors.Join when any sink failed.
	if len(collected) > 0 {
		//: surface the joined chain so callers can errors.Is each cause.
		return errors.Join(collected...)
	}
	//: happy path — every sink flushed cleanly.
	return nil
}

// Close forwards to every entry + fallback and aggregates per-sink errors.
//
// Returns:
//   - err: errors.Join of per-sink failures; nil on unanimous success.
func (s *routerSink) Close() error {
	//: collect per-sink errors so callers see every failure, not just the first.
	collected := make([]error, 0, len(s.entries)+1)
	//: walk every entry in order; failures are captured but never short-circuit.
	for _, entry := range s.entries {
		//: every entry sees the close regardless of upstream failures.
		if cerr := entry.Sink.Close(); cerr != nil {
			//: append the failure to the join set.
			collected = append(collected, cerr)
		}
	}
	//: also close the fallback when it exists.
	if s.fallback != nil {
		//: include the fallback in the joined error set.
		if cerr := s.fallback.Close(); cerr != nil {
			//: append the fallback failure too.
			collected = append(collected, cerr)
		}
	}
	//: aggregate the per-sink failures via errors.Join when any sink failed.
	if len(collected) > 0 {
		//: surface the joined chain so callers can errors.Is each cause.
		return errors.Join(collected...)
	}
	//: happy path — every sink closed cleanly.
	return nil
}
