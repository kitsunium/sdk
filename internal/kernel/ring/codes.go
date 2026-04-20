// Package ring: codes.go — range 1400-1499 reserved for the ring buffer.
// Codes are declared at source as typed constants; the errs registry audit
// verifies uniqueness and range membership.
package ring

// range: 1400-1499

// CodeRingFull identifies a TryWrite call on a saturated ring buffer.
const CodeRingFull int = 1401

// CodeRingEmpty identifies a TryRead call on an empty ring buffer.
const CodeRingEmpty int = 1402

// CodeRingCapZero identifies a New call made with a non-positive capacity.
const CodeRingCapZero int = 1403
