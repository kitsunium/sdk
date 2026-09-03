//go:build linux

// Package server — compile-time interface assertions, kept out of the
// production source per KTN-IFACE-ASSERT-PLACEMENT.
package server

// The batched reader must satisfy the seam the read loop is written against; a
// drift here would be silent until the first datagram arrived.
var _ datagramSource = (*multiReader)(nil)
