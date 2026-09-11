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
package nettransport

import (
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
)

// protoTCP / protoUDP / protoHTTP are the canonical writer keys the three
// factories register under and the network identifiers passed to net.Dial.
const (
	protoTCP  = "tcp"
	protoUDP  = "udp"
	protoHTTP = "http"
)

// WriterTCP / WriterUDP / WriterHTTP are the registered factory singletons. The
// blank assignments run writer.Register at package load (no init()), mirroring
// the codec convention.
var (
	WriterTCP  = writer.Register(&netFactory{proto: protoTCP})
	WriterUDP  = writer.Register(&netFactory{proto: protoUDP})
	WriterHTTP = writer.Register(&netFactory{proto: protoHTTP})
)

// netFactory builds a network Sink for one protocol. The proto field is both the
// registry key (Name) and the net.Dial network, so one type serves all three
// registrations without three near-identical structs.
type netFactory struct {
	// proto is the protocol identifier: tcp, udp, or http.
	proto string
}

// Name reports the canonical key (the protocol identifier).
func (f *netFactory) Name() writer.Name {
	//: the protocol doubles as the registry key consumers pass in a spec.
	return writer.Name(f.proto)
}

// Open validates cfg and builds the composed sink. A wrong concrete config type
// yields the shared WriterConfigInvalid; an empty Address surfaces the dial
// sentinel (no address echoed).
func (f *netFactory) Open(cfg writer.Config) (sink corelogger.Sink, err error) {
	//: reject a mismatched config type with the shared sentinel.
	c, ok := cfg.(NetConfig)
	//: the type assertion guards the rest of the construction.
	if !ok {
		//: surface the documented config-type-mismatch sentinel.
		return nil, writer.WriterConfigInvalid
	}
	//: an empty address is a misconfiguration, surfaced as a dial failure with
	//: only the network attached (never the empty address itself).
	if c.Address == "" {
		//: documented dial sentinel — caller must supply an address.
		return nil, wrapDial(nil, f.proto)
	}
	//: build the protocol-specific seam and compose the chain.
	return f.build(c)
}

// build wires the protocol seam (http client or a dialed conn) and composes the
// levelgate(async(netSink)) chain around it.
func (f *netFactory) build(c NetConfig) (sink corelogger.Sink, err error) {
	//: http uses a client seam — no dial here, keep-alive connections pooled.
	if f.proto == protoHTTP {
		//: the http seam allocates per send; its closer drops the idle
		//: connections of the pool it owns (never a supplied client's).
		send, closer := newHTTPSeam(c.Address, c.HTTPClient)
		//: compose the non-blocking + level-gated chain over the http seam.
		return compose(f.proto, send, closer, c), nil
	}
	//: tcp/udp dial once; a dial failure surfaces the dial sentinel.
	send, closer, derr := newConnSeam(f.proto, c.Address, c.Dialer)
	//: forward the dial failure unchanged (origin wins).
	if derr != nil {
		//: NetTransportDialFailed already carries the right code/reason.
		return nil, derr
	}
	//: compose the non-blocking + level-gated chain over the conn seam.
	return compose(f.proto, send, closer, c), nil
}
