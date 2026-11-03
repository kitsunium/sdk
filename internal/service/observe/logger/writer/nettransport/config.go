package nettransport

import (
	"net"
	"net/http"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/async"
	"github.com/kitsunium/sdk/internal/service/observe/logger/writer/levelgate"
)

// streamBatchMax is how many queued records a network writer's drainer hands
// its transport at once (#807): one writev(2) on tcp, the bytes on the stream
// unchanged; one sendmmsg(2) on udp, a datagram per record; one NDJSON POST
// on http under HTTPBatchNDJSON.
const streamBatchMax int = 16

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
	// HTTPClient, when non-nil, replaces the default client for the http
	// protocol. SECURITY: supply a client whose Transport enforces an SSRF
	// allowlist for consumer-controlled URLs. Nil falls back to a
	// timeout-bounded client that refuses redirects (CheckRedirect returns
	// http.ErrUseLastResponse), NOT http.DefaultClient — so a redirect cannot
	// bypass the validated target — on a connection pool of its own, which the
	// sink's Close releases: a clone of http.DefaultTransport while that is the
	// stdlib's *http.Transport, proxy environment included. A process that
	// replaced http.DefaultTransport with another RoundTripper gets a fresh
	// transport that keeps only the proxy environment and the idle reaping,
	// never that RoundTripper — whatever it carries reaches this writer only
	// through an HTTPClient that carries it.
	//
	// A supplied client is used AS-IS, Transport included, and its pool is the
	// caller's: the sink's Close leaves it alone, so a caller that built one for
	// this writer alone releases it with CloseIdleConnections after closing the
	// writer. One whose Transport is http.DefaultTransport (a nil Transport is)
	// shares that pool with the whole process, and anything that empties it —
	// every httptest.Server.Close, every http.DefaultClient.CloseIdleConnections
	// — can make a delivered record read as a failed write, which a failover or
	// retry then duplicates. Give such a client a Transport of its own.
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
	// HTTPBatchNDJSON, when true, has the http writer post the records its
	// drainer holds — up to sixteen at a time — as ONE application/x-ndjson
	// body, one record per line, instead of one application/json POST per
	// record (#807). It changes the wire: the collector must read NDJSON
	// (Vector's and Fluent Bit's http sources do, Elasticsearch's _bulk does
	// not without action lines). A refused POST fails every record of its
	// batch. Ignored by tcp and udp. The zero value posts record by record.
	HTTPBatchNDJSON bool
}

// compose builds the network writer chain: the terminal per-record netSink
// wrapped by async (non-blocking ring + OnDrop back-pressure) and then by
// levelgate (the per-writer MinLevel floor). It returns the chain behind the
// core/observe/logger.Sink interface so each protocol factory only supplies its send +
// close seams and a plain-data NetConfig — the composition order lives here,
// mirroring the dbsink and s3 factories.
func compose(network string, send sendFunc, closer func() error, cfg *NetConfig) corelogger.Sink {
	//: record by record: the udp and http seams, and the tests' own seams.
	return composeSink(newNetSink(network, send, closer), cfg, 1)
}

// composeSink wraps base in the async ring, draining batchMax records at a
// time, and the per-writer level floor.
func composeSink(base *netSink, cfg *NetConfig, batchMax int) corelogger.Sink {
	//: terminal per-record sink → async (non-block + OnDrop) → levelgate (floor),
	//: the same ordering dbsink wires so back-pressure precedes the transport.
	nonblocking := async.New(base, async.Config{BufferSize: cfg.BufferSize, OnDrop: cfg.OnDrop, OnError: cfg.OnError, BatchMax: batchMax})
	//: outermost gate drops below-floor records before they reach the ring.
	return levelgate.New(nonblocking, cfg.MinLevel)
}
