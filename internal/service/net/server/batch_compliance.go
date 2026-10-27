package server

var (
	// The portable reader is the floor every platform falls back to, so it
	// must satisfy the seam on every platform, not only where the batched path
	// exists.
	_ datagramSource = (*portableReader)(nil)
	// The portable sink is the floor replies fall back to on every platform.
	_ datagramSink = portableSink{}
	// The queued sink is BatchReplies' floor on every platform.
	_ datagramSink = (*queuedSink)(nil)
	// A bound socket forwards the handover to its source.
	_ handoverSource = (*boundPacketConn)(nil)
)
