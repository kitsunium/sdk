// Package crypto — the process-wide StreamSealer registry + SealStream / OpenStream dispatch.
package crypto

import (
	"fmt"
	"io"

	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// streamSealers maps each Algorithm to its StreamSealer. Backed by the shared
// read-mostly schemeRegistry — register once at import, dispatch is lock-free.
var streamSealers = schemeRegistry[StreamSealer]{verb: "RegisterStreamSealer"}

// RegisterStreamSealer inserts s under s.Algorithm() and returns it so callers
// can bind the singleton to a typed package-level variable. Panics on a nil
// sealer or when a distinct sealer already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in StreamSealer instances back to
// callers so each scheme keeps its concrete type unexported; the contract is the
// StreamSealer interface itself.
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func RegisterStreamSealer(s StreamSealer) StreamSealer {
	//: a typed nil and a non-comparable plug-in both satisfy the port and
	//: neither can serve — refuse at import, where the offender is named.
	if why := plugin.Unusable(s); why != "" {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("crypto.RegisterStreamSealer [%s DUPLICATE_REGISTRATION]: %s", CodeDuplicateRegistration, why))
	}
	//: publish via the shared registry; a distinct duplicate Name is a hard conflict.
	if err := streamSealers.publish(s.Algorithm(), s); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the sealer lets callers bind it to a typed singleton var.
	return s
}

// LookupStreamSealer returns the StreamSealer registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in StreamSealer instances behind the
// StreamSealer interface — concrete types are intentionally unexported per scheme.
func LookupStreamSealer(name Algorithm) (s StreamSealer, ok bool) {
	//: delegate to the shared registry's typed lookup.
	return streamSealers.lookup(name)
}

// AvailableStreamSealers returns the sorted list of registered streaming-AEAD
// Algorithms.
func AvailableStreamSealers() []Algorithm {
	//: delegate to the shared registry's sorted key list.
	return streamSealers.available()
}

// SealStream wraps dst so writes are sealed under key with aad by the
// StreamSealer registered as name. Streaming extends the AEAD domain, so a name
// with no registered sealer returns the shared UnknownAlgorithm sentinel; a
// scheme construction fault propagates unchanged.
func SealStream(name Algorithm, key Key, dst io.Writer, aad []byte) (sealed io.WriteCloser, err error) {
	//: resolve the sealer; a missing import surfaces the AEAD-domain sentinel.
	sealer, ok := LookupStreamSealer(name)
	//: absence path — the sealer package was never blank-imported.
	if !ok {
		//: streaming reuses the AEAD UnknownAlgorithm, not a stream-only miss.
		return nil, UnknownAlgorithm
	}
	//: delegate construction; the scheme owns its salt + header framing.
	return sealer.Writer(key, dst, aad)
}

// OpenStream wraps src so reads are opened under key with aad by the
// StreamSealer registered as name. A name with no registered sealer returns the
// shared UnknownAlgorithm sentinel; a truncated stream surfaces later as
// StreamTruncated from the returned reader.
func OpenStream(name Algorithm, key Key, src io.Reader, aad []byte) (opened io.Reader, err error) {
	//: resolve the sealer; a missing import surfaces the AEAD-domain sentinel.
	sealer, ok := LookupStreamSealer(name)
	//: absence path — the sealer package was never blank-imported.
	if !ok {
		//: streaming reuses the AEAD UnknownAlgorithm, not a stream-only miss.
		return nil, UnknownAlgorithm
	}
	//: delegate construction; the reader enforces the hold-back contract.
	return sealer.Reader(key, src, aad)
}
