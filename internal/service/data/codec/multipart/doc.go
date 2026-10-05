// Package multipart — boundary recovery and validation.
//
// The delimiter that separates the parts of a multipart body is announced in
// the message's Content-Type header, NOT in the body. codec.Codec carries no
// header, so this file is where the gap is closed: the boundary is re-emitted
// on every delimiter line, which makes a well-formed body self-describing, and
// these helpers read it back out. See CLAUDE.md §The boundary problem for what
// that buys and what it does not.
//
// Package multipart implements RFC 7578 multipart/form-data as a
// codec.StreamingCodec registered under Format("multipart"). Blank-importing
// this package is enough to make it resolvable via the core/data/codec registry.
//
// The format is a CONTAINER, not a value serialisation: its native Go shape is
// [FormValue], a list of named [PartValue] sections. Any other value takes the
// JSON-mediated shape — a single part named [JSONPartName] carrying
// encoding/json's output — so the universal Marshal(F, v) / Unmarshal(F, b, &v)
// contract holds without a facade-side promotion rule. baseenc is the in-tree
// precedent for a JSON-mediated pipeline.
//
// Streaming is the point of the format, so [codec.StreamingCodec] is the
// interface that matters here: NewEncoder writes one part per Encode straight
// to the io.Writer, NewDecoder reads one part per Decode straight off the
// io.Reader, and neither holds more than a single part body at a time. Marshal
// and Unmarshal are the same two paths driven over a scratch buffer.
//
// The delimiter lives in the message's Content-Type header, which the Codec
// contract cannot carry. See CLAUDE.md §The boundary problem for how that gap
// is closed ([ContentType] on the write side, [Boundary] on the read side) and
// for the one case it is NOT closed (a body with a "--"-prefixed preamble,
// which only the real header can disambiguate — hence [BoundaryCodec]).
//
// Package multipart — the running tally a LimitsConfig is checked against.
//
// Package multipart — adapts mime/multipart.Reader to codec.Decoder.
//
// This is the ONLY read path in the package: Unmarshal drives this decoder
// over a bytes.Reader, so a body parsed from memory and a body streamed off a
// socket go through identical code and identical bounds.
//
// Package multipart — reading a part's Content-Disposition, and the one rule
// its filename is reduced by on every operating system.
//
// Package multipart — adapts mime/multipart.Writer to codec.Encoder.
//
// This is the ONLY write path in the package: Marshal and Append both drive
// this encoder over a scratch buffer, so a body built in memory and a body
// streamed to a socket go through identical code and identical bounds.
//
// Package multipart — the two value types that model a body: one section
// (PartValue) and the container that frames them (FormValue). They live in one
// file because neither is meaningful without the other.
//
// Package multipart — the memory bound applied to every decode AND every
// encode. A multipart body is the shape an upload arrives in, so the decoder
// is the SDK surface most exposed to a hostile stream: unbounded, a single
// crafted part drives io.ReadAll to an OOM, and a boundary flood does the same
// with a very large number of empty parts. Three bounds close both doors
// (per-part bytes, part count, aggregate bytes); the encoder enforces the
// identical set so the codec never emits a body it would refuse to read back.
//
// Package multipart — compile-time proof that the concrete types satisfy the
// contracts this package advertises. Kept in its own *_compliance.go file so
// the assertions live where a reader looks for them and nowhere else.
package multipart
