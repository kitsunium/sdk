package consensus

import "time"

// The capabilities the node finds by type assertion on the ports it was
// handed, each beside the port it widens (ADR 0039); consensus_compliance.go
// and tcp_compliance.go pin the shipped stores and transports to them.

// dirHolder is the shipped log's hold on its data directory: a DataDir
// refuses offline work and Close while a Node holds it (D11, R3-18).
type dirHolder interface {
	acquire() error
	release()
}

// silenceBounder is a transport whose sender links a node bounds by read
// silence (TCPTransport.boundSilence, #1116).
type silenceBounder interface {
	boundSilence(d time.Duration)
}

// joinReleaser is a transport that keeps a JOIN answer's members admitted
// apart from the engine's configuration until the joiner is admitted
// (#552).
type joinReleaser interface {
	releaseJoinAnswer()
}
