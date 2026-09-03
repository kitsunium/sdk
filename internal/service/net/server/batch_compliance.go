// Package server — compile-time interface assertions, kept out of the
// production source per KTN-IFACE-ASSERT-PLACEMENT.
package server

// The portable reader is the floor every platform falls back to, so it must
// satisfy the seam on every platform, not only where the batched path exists.
var _ datagramSource = (*portableReader)(nil)
