// Package journald registers the "journald" writer factory (ADR 0015): a
// stdlib unix-datagram sink that ships records to the systemd journal. Importing
// the package self-registers the factory (no init()), so
// writer.Open("journald", journald.Config{…}) and YAML FromConfig
// topologies resolve. Linux-only in practice (the socket is systemd's), but the
// code is plain stdlib net and builds everywhere; on a host without journald the
// Open simply fails with JournaldOpenFailed.
package journald

import (
	"net"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/async"
	"github.com/kitsunium/sdk/internal/service/writer/levelgate"
)

// writerName is the canonical registry key. socketNetwork / defaultSocketPath
// are the unix-datagram network and the systemd journal socket default.
const (
	writerName        = "journald"
	socketNetwork     = "unixgram"
	defaultSocketPath = "/run/systemd/journal/socket"
)

// Writer is the registered journald factory singleton. The blank assignment runs
// writer.Register at package load (no init()), mirroring the codec convention.
var Writer = writer.Register(&journaldFactory{})

// journaldFactory builds a journald Sink from a Config.
type journaldFactory struct{}

// Name reports the canonical key "journald".
func (*journaldFactory) Name() writer.Name {
	//: the literal key consumers pass in a writer spec.
	return writerName
}

// Open connects the journal socket and composes the level-gated, non-blocking
// chain over the datagram sink. A wrong config type yields the shared
// WriterConfigInvalid; a connect failure surfaces JournaldOpenFailed.
func (*journaldFactory) Open(cfg writer.Config) (sink corelogger.Sink, err error) {
	//: reject a mismatched config type with the shared sentinel.
	c, ok := cfg.(Config)
	//: the type assertion guards the rest of the construction.
	if !ok {
		//: surface the documented config-type-mismatch sentinel.
		return nil, writer.WriterConfigInvalid
	}
	//: default to the systemd journal socket when no override is given.
	path := c.SocketPath
	//: the zero value selects the standard journald datagram socket.
	if path == "" {
		//: standard systemd journal socket path.
		path = defaultSocketPath
	}
	//: nil dialer falls back to stdlib net.Dial (injectable for tests).
	dial := c.Dialer
	//: the fallback is a one-time construction branch, not a hot path.
	if dial == nil {
		//: default dialer preserves zero-config usage.
		dial = net.Dial
	}
	//: connect the unix-datagram socket; a failure surfaces the open sentinel.
	conn, derr := dial(socketNetwork, path)
	//: forward the connect failure (the socket path is never echoed).
	if derr != nil {
		//: JournaldOpenFailed already carries the right code/reason.
		return nil, wrapOpen(derr)
	}
	//: terminal datagram sink → async (non-block + OnDrop) → levelgate (floor).
	base := newJournaldSink(conn)
	nonblocking := async.New(base, async.Config{BufferSize: c.BufferSize, OnDrop: c.OnDrop})
	//: outermost gate drops below-floor records before they reach the ring.
	return levelgate.New(nonblocking, c.MinLevel), nil
}
