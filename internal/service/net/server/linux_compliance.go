//go:build linux

package server

var (
	// The batched reader must satisfy the seam the read loop is written
	// against; a drift here would be silent until the first datagram arrived.
	_ datagramSource = (*multiReader)(nil)
	// It hands its socket over too (ADR 0192 §D4).
	_ handoverSource = (*multiReader)(nil)
	// The batched sink must satisfy the seam replies are written against.
	_ datagramSink = (*multiWriter)(nil)
)
