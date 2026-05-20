// Package flatbuffers — small adapter interfaces that let strongly-typed
// FlatBuffers wrappers participate in the universal codec contract without
// reaching into schema-generated structs from this package.
package flatbuffers

// BytesProvider is the optional source interface a Marshal argument may
// satisfy to hand the codec its already-encoded FlatBuffer payload. The
// returned slice MUST remain valid for the lifetime of the codec call;
// the codec performs no copy and treats the buffer as read-only.
type BytesProvider interface {
	Bytes() []byte
}

// BytesAcceptor is the optional sink interface an Unmarshal target may
// satisfy to receive the decoded FlatBuffer payload. The codec passes the
// caller's data slice by reference; implementations that need ownership
// MUST copy the slice themselves.
type BytesAcceptor interface {
	SetBytes(data []byte)
}
