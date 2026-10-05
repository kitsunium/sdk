// Package tlv implements a self-describing Type-Length-Value codec.
// Every value is encoded as a 1-byte tag, an unsigned LEB128 length, and
// a tag-specific value payload. Composite tags (slice/map/struct) recurse
// into nested TLV records. Reflection drives both directions so arbitrary
// Go values fit through the same Marshal/Unmarshal interface.
//
// The codec is registered under Name "tlv" and the canonical
// "application/x-tlv" MIME type at package import time.
//
// Package tlv — reflection-driven decoder shared by Unmarshal and the
// streaming Decoder. Every helper returns the residual byte slice plus a
// wrapped *errs.Error on failure.
//
// Package tlv — target-aware decode fast path.
//
// The default decoder (decoder.go) produces an untyped value tree
// (map[string]any for structs, []any for slices, map[any]any for maps)
// then projects it onto the caller's typed target. For typed-struct
// targets this pays two costs: the outer map[string]any allocation +
// per-field string keys, AND a second walk through projectMapToStruct.
//
// decodeStructInto walks the wire ONCE, locating each field by name
// in the cached structTypeInfo and assigning into target.Field(i)
// directly. The result is byte-identical to the untyped path on
// roundtrip; only the in-flight allocation profile changes.
//
// Scope: top-level *struct targets only. Nested struct fields still
// fall through the untyped decodeValue + convertValue helpers so
// cross-shape narrowings (e.g. map[string]any field on a typed
// struct) remain compatible. Extending the fast path to nested
// structs is a follow-up that reuses the same primitives.
//
// Package tlv — reflection-driven encoder shared by Marshal, Append, and
// the streaming Encoder. Every helper returns the (possibly re-allocated)
// destination slice plus a wrapped *errs.Error on failure.
//
// Package tlv — shared reflection helpers used by both encoder and decoder.
//
// reflectView is the local wrapper that lets package-internal helpers
// accept reflect values without exposing the concrete reflect.Value type
// across the encode/decode boundary. bytesFromReflectValue is the only
// reflection helper currently shared by both sides — the decoder uses
// the slice fast-path when reconstructing []byte targets, the encoder
// uses the array fallback when emitting `tagBytes` from an array source.
//
// Package tlv — type metadata cache shared by encoder and decoder.
//
// Reflection-driven codecs pay reflect.Type.NumField + reflect.Type.Field
// on every encode and every decode. For hot-path payloads with repeated
// types those calls dominate the ns/op budget. structTypeInfoCache
// memoises the (field name, index, type, kind) tuples per reflect.Type
// pointer so subsequent encodes/decodes of the same struct skip the
// reflect walk entirely and reuse the cached metadata.
//
// The cache is global, write-once-per-type, lock-free on the read path
// (sync.Map's read-mostly fast path). reflect.Type pointers are stable
// for the life of the process, so cache invalidation is unnecessary.
//
// Package tlv — wire-format tag constants and shared limits for the TLV
// (Type-Length-Value) codec.
//
// Wire layout (each record):
//
//	tag(1B) length(varint) value(...)
//
// length is unsigned LEB128 (1-10 bytes); value layout depends on the tag.
package tlv
