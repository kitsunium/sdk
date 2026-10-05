// Package cache — hosts the compile-time interface assertions, keeping them
// out of the production source so the runtime binary carries no
// diagnostic-only declarations.
//
// Package cache — the L1/L2 chain.
//
// Package cache — the NewChain constructor configuration.
//
// Package cache provides the concrete cache stores implementing
// internal/core/data/cache: a tagged, stampede-protected memory store over the
// kernel LRU+TTL primitive, and the chain that puts one store in front of
// another. Stdlib-only, cross-OS. ADR 0049.
//
// Package cache — the tagged, stampede-protected in-memory store.
//
// Package cache — what the underlying primitive actually stores.
//
// Package cache — the refusals every store shares, and the wrapping rule for
// a caller's fill error.
//
// Package cache — the two-way tag index behind InvalidateTag.
//
// Package cache — one level of a chained store.
package cache
