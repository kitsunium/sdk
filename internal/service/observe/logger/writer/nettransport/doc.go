// Package nettransport — the transport seams: the ONLY file that touches net /
// net/http. Each constructor returns a sendFunc (+ optional closer) captured by
// netSink, so the concrete transport stays confined here and tests inject a
// recording sendFunc with no real socket.
//
// Package nettransport — the NetConfig value type plus compose, the single
// helper a factory uses to build the levelgate(async(netSink)) chain. Keeping
// the composition order here (not in each factory) means tcp / udp / http all
// inherit the same back-pressure + level-floor wiring, with only the transport
// send/close seam varying per protocol.
//
// Package nettransport — the config Decoder making tcp/udp/http reachable from a
// config file via pkg/v1/observe/logger.FromConfig. Only the plain-data keys are
// decodable (address / min_level / buffer_size); the Dialer and HTTPClient SSRF
// seams are code-only and never come from a config blob. A malformed shape
// yields the shared core/observe/logger/writer.WriterConfigInvalid (no per-package code), tagged
// with the protocol only — never the offending value (secret gate).
//
// Package nettransport — netSink, the terminal per-record network sink. It ships
// each formatted record straight to the transport seam under a mutex, mirroring
// the syslog sink: the send is synchronous, so the recycled payload p is fully
// consumed before Write returns and needs no defensive clone (the Write path is
// 0 alloc — see BENCH.md). Non-blocking back-pressure is provided by the async
// middleware composed around it, not here.
//
// Package nettransport registers stdlib network writer factories — "tcp",
// "udp", and "http" (ADR 0015). Importing the package self-registers all three
// (no init()), so writer.Open("tcp", NetConfig{…}) and FromConfig topologies
// resolve. It is the dep-light seam that community adapters (Loki, Elastic,
// Datadog, a Kafka bridge) build on WITHOUT pulling a vendor SDK into the tree:
// each composes levelgate(async(netSink)) over a stdlib net.Conn or http.Client.
//
// SECURITY (CWE-918): when the destination is consumer-controlled, supply
// NetConfig.Dialer (tcp/udp) or NetConfig.HTTPClient (http) with an SSRF
// allowlist — the raw address is never echoed into an error.
//
// Package nettransport — the wrap points every failure of this writer goes through,
// so each carries its code from internal/core/observe/logger/writer/nettransport.
package nettransport
