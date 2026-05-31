// Package crypto — the StreamSealer port: chunked authenticated encryption.
package crypto

import "io"

// StreamSealer is a streaming authenticated-encryption scheme: it seals and
// opens an io.Writer / io.Reader pair in fixed-size chunks under a Key and
// associated data, extending the whole-buffer AEAD domain to arbitrarily large
// payloads. The Reader honours the hold-back contract: it MUST NOT surface a
// chunk's plaintext downstream before that chunk authenticates, and a stream cut
// short surfaces as StreamTruncated rather than as accepted plaintext (the
// final chunk carries a flag that a truncated stream never reaches).
// Implementations self-register via RegisterStreamSealer.
//
// IFACE-PLUGIN: the registry hands plug-in StreamSealer instances back behind
// this interface; concrete scheme types stay unexported in their own packages.
type StreamSealer interface {
	// Algorithm reports the canonical key under which this scheme registers.
	Algorithm() Algorithm
	// Writer wraps dst so writes are sealed under key with aad; Close writes
	// the final authenticated chunk.
	Writer(key Key, dst io.Writer, aad []byte) (io.WriteCloser, error)
	// Reader wraps src so reads are opened under key with aad; a truncated
	// stream surfaces as StreamTruncated from the returned reader.
	Reader(key Key, src io.Reader, aad []byte) (io.Reader, error)
}
