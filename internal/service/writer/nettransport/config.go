// Package nettransport — the NetConfig value type plus compose, the single
// helper a factory uses to build the levelgate(async(netSink)) chain. Keeping
// the composition order here (not in each factory) means tcp / udp / http all
// inherit the same back-pressure + level-floor wiring, with only the transport
// send/close seam varying per protocol.
package nettransport

import (
	"net"
	"net/http"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/async"
	"github.com/kitsunium/sdk/internal/service/writer/levelgate"
)

// NetConfig tunes a network writer. Address is required (a host:port for
// tcp/udp, a URL for http); every other field is optional. It carries only
// stdlib types, so the package stays dep-light.
type NetConfig struct {
	// Address is the destination: "host:port" for tcp/udp, a full URL for http.
	Address string
	// Dialer, when non-nil, replaces net.Dial for tcp/udp connection setup.
	// SECURITY (CWE-918): when Address is consumer-controlled, plug an
	// allowlist dialer so an attacker cannot target internal services or the
	// cloud-metadata endpoint (169.254.169.254). Nil falls back to net.Dial.
	Dialer func(network, addr string) (conn net.Conn, err error)
	// HTTPClient, when non-nil, replaces http.DefaultClient for the http
	// protocol. SECURITY: supply a client whose Transport enforces an SSRF
	// allowlist for consumer-controlled URLs. Nil falls back to a client with
	// a sane default timeout.
	HTTPClient *http.Client
	// MinLevel is the optional per-writer severity floor; the zero value
	// (level.Info) inherits the handler-global level.
	MinLevel level.Level
	// BufferSize is the async ring capacity; a non-positive value applies
	// async's own default. The producer never blocks on the network — drops
	// surface through OnDrop instead.
	BufferSize int
	// OnDrop, when non-nil, is invoked with the count of records discarded
	// because the non-blocking ring saturated (network slower than producers).
	OnDrop func(dropped int)
	// OnError, when non-nil, is invoked for every downstream send failure the
	// async drainer observes (dial/write/non-2xx). Nil discards the error — the
	// async ring is the only failure surface once the producer has handed off.
	// Code-only, like Dialer/HTTPClient — never decoded from a config blob.
	OnError func(err error)
}

// compose builds the network writer chain: the terminal per-record netSink
// wrapped by async (non-blocking ring + OnDrop back-pressure) and then by
// levelgate (the per-writer MinLevel floor). It returns the chain behind the
// core/logger.Sink interface so each protocol factory only supplies its send +
// close seams and a plain-data NetConfig — the composition order lives here,
// mirroring the dbsink and s3 factories.
func compose(network string, send sendFunc, closer func() error, cfg NetConfig) corelogger.Sink {
	//: terminal per-record sink → async (non-block + OnDrop) → levelgate (floor),
	//: the same ordering dbsink wires so back-pressure precedes the transport.
	base := newNetSink(network, send, closer)
	nonblocking := async.New(base, async.Config{BufferSize: cfg.BufferSize, OnDrop: cfg.OnDrop, OnError: cfg.OnError})
	//: outermost gate drops below-floor records before they reach the ring.
	return levelgate.New(nonblocking, cfg.MinLevel)
}
