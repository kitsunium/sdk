// Package pem wraps encoding/pem as a codec.Codec implementation.
// PEM is block-structured: each wire message is a typed block ("CERTIFICATE",
// "PRIVATE KEY", etc.), so the codec operates on *pem.Block values. It is a
// codec.StreamingCodec too: its stream decoder hands one block per Decode —
// a certificate chain read as encoding/pem.Decode would read it in a loop —
// and its stream encoder writes one block per Encode.
package pem
