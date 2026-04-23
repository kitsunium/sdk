// Package ring: errors.go declares the sentinels returned by this package's
// constructor and TryWrite / TryRead operations. Each var's name equals its
// errs.Define Reason in SCREAMING_SNAKE form.
package ring

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// Full is returned by TryWrite when the ring has no slot available; the
	// caller decides whether to drop, block, or retry depending on its
	// upstream policy (e.g. async sink's DropOldest / DropNewest).
	Full = errs.Define(CodeRingFull, "RING_FULL",
		"Ring buffer is at capacity",
		"internal/kernel/ring.TryWrite called on a full ring")

	// Empty is returned by TryRead when no item is available; the caller
	// decides whether to spin, sleep, or block on a wake signal.
	Empty = errs.Define(CodeRingEmpty, "RING_EMPTY",
		"Ring buffer is empty",
		"internal/kernel/ring.TryRead called on an empty ring")

	// CapZero is returned by New when the requested capacity is non-positive.
	CapZero = errs.Define(CodeRingCapZero, "RING_CAP_ZERO",
		"Ring buffer capacity must be positive",
		"internal/kernel/ring.New called with cap <= 0")
)
