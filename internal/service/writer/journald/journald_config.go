// Package journald — the Config value type, in its own file per the
// one-exported-struct-per-file convention.
package journald

import (
	"net"

	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// Config tunes the journald sink. Every field is optional: the zero
// Config connects the default systemd journal socket, inherits the
// handler-global level, and uses async's default ring. It carries only stdlib
// types, so the package stays dep-light.
type Config struct {
	// SocketPath overrides the journal datagram socket; the zero value uses the
	// systemd default (/run/systemd/journal/socket).
	SocketPath string
	// Dialer, when non-nil, replaces net.Dial for connecting the socket.
	// Injectable so tests connect an in-memory pipe with no real journald.
	Dialer func(network, addr string) (conn net.Conn, err error)
	// MinLevel is the optional per-writer severity floor; the zero value
	// (level.Info) inherits the handler-global level.
	MinLevel level.Level
	// BufferSize is the async ring capacity; a non-positive value applies
	// async's own default. The producer never blocks on the socket — a slow
	// journald surfaces as drops via OnDrop.
	BufferSize int
	// OnDrop, when non-nil, is invoked with the count of records discarded
	// because the non-blocking ring saturated.
	OnDrop func(dropped int)
	// OnError, when non-nil, is invoked with each downstream send failure the
	// async drainer observes; the zero value silently discards them.
	OnError func(err error)
}
