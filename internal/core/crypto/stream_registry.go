package crypto

import "io"

// streamSealers maps each Algorithm to its StreamSealer. Backed by the shared
// read-mostly schemeRegistry — register once at import, dispatch is lock-free.
var streamSealers = schemeRegistry[StreamSealer]{verb: "RegisterStreamSealer"}

// registerStreamSealer is RegisterStreamSealer's body: decl_gen.go writes RegisterStreamSealer, from the
// design, as one call of it.
//
// IFACE-PLUGIN: the registry hands plug-in StreamSealer instances back to
// callers so each scheme keeps its concrete type unexported; the contract is the
// StreamSealer interface itself.
func registerStreamSealer(s StreamSealer) StreamSealer {
	//: refuse an unusable sealer, then publish it under its Algorithm; both
	//: refusals panic at boot with the dotted-quad code.
	return streamSealers.register(s)
}

// lookupStreamSealer is LookupStreamSealer's body: decl_gen.go writes LookupStreamSealer, from the
// design, as one call of it.
//
// IFACE-PLUGIN: the registry stores plug-in StreamSealer instances behind the
// StreamSealer interface — concrete types are intentionally unexported per scheme.
func lookupStreamSealer(name Algorithm) (s StreamSealer, ok bool) {
	//: a lock-free snapshot read; a miss hands back nil AND false.
	return streamSealers.table.Lookup(name)
}

// availableStreamSealers is AvailableStreamSealers's body: decl_gen.go writes AvailableStreamSealers, from the
// design, as one call of it.
func availableStreamSealers() []Algorithm {
	//: sorted ascending, the caller's own slice; nil before any registration.
	return streamSealers.table.Names()
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
