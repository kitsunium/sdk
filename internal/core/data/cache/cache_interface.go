package cache

import "context"

// Fill computes the entry for a key that the cache does not hold. It is the
// caller's code: a database query, an HTTP call, a derivation.
//
// It is a FUNC port rather than a one-method interface, the shape
// internal/core already admits for resilience.Operation, scheduler.Job and
// validation.Constraint — and, per ADR 0039, a func type satisfies the
// no-widening rule structurally, because it cannot grow a method at all.
type Fill[V any] func(ctx context.Context) (EntryValue[V], error)
