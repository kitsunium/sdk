// Package protobuf wraps google.golang.org/protobuf as a codec.Codec
// implementation. It lives under third-party/codec/, in a module of its own
// (ADR 0157), and is OPT-IN: a consumer blank-imports it to register the
// "protobuf" Format — pkg/v1/data/codec does NOT pull it. The reason is the
// schema-bound contract, not dep weight: Protobuf only encodes proto.Message
// values, so it cannot honour the universal "every registered codec
// round-trips any Go struct" guarantee the default registry enforces. A
// non-message value surfaces PROTOBUF_MARSHAL_FAILED /
// PROTOBUF_UNMARSHAL_FAILED. Append writes through
// proto.MarshalOptions.MarshalAppend, so a destination with room costs no
// allocation of its own, and Marshal is Append onto nil. It streams in
// encoding/protodelim's layout, one varint-delimited message per Encode and
// per Decode — a file of records, a gRPC-style frame sequence — with a
// declared length past 10 MiB refused (SIZE_EXCEEDED) before the body
// is read. ADR 0023.
//
// Package protobuf — range 0.3.38.* (ADR 0023 third-party/codec/protobuf block).
//
// Package protobuf — declares the sentinel *errs.Error values for Protobuf.
package protobuf
