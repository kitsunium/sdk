// Package multipart implements RFC 7578 multipart/form-data as a
// codec.StreamingCodec registered under Format("multipart"). Blank-importing
// this package is enough to make it resolvable via the core/codec registry.
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
package multipart

import (
	"bytes"
	"io"
	stdmp "mime/multipart"
	"slices"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/core/codec/scratch"
)

// JSONPartName is the field name of the part the JSON-mediated shape writes
// and reads. It matches pkg/v1/codec's csv promotion header for the same
// reason: one recognisable label for "the SDK put a JSON document here".
const JSONPartName string = "_json"

// mimeFormData is the canonical media type of the format.
const mimeFormData string = "multipart/form-data"

// mimeJSON is the Content-Type stamped on the JSON-mediated part.
const mimeJSON string = "application/json"

// BoundaryCodec is the optional extension this package implements on top of
// codec.StreamingCodec: the two constructors that take the boundary from the
// caller instead of recovering it from the bytes.
//
// This is the honest form of the streaming API. An HTTP server already holds
// the authoritative boundary in the request's Content-Type header, and an HTTP
// client must set that header before it writes the first body byte — neither
// can be expressed through NewEncoder / NewDecoder, whose signatures the
// domain fixes. Callers reach it with a type assertion, the same way they
// reach codec.Appender.
type BoundaryCodec interface {
	codec.Codec
	// NewEncoderWithBoundary streams a body framed by the given boundary.
	NewEncoderWithBoundary(w io.Writer, boundary string) (enc codec.Encoder, err error)
	// NewDecoderWithBoundary streams a body framed by the given boundary,
	// bypassing recovery from the wire entirely.
	NewDecoderWithBoundary(r io.Reader, boundary string) (dec codec.Decoder, err error)
}

// BoundaryProvider is implemented by the codec.Encoder this package returns,
// so a caller streaming a request body can read the generated delimiter and
// set its Content-Type header before the first byte goes out.
type BoundaryProvider interface {
	// Boundary returns the delimiter the encoder writes.
	Boundary() string
}

// Package-level state: the codec singleton plus the hoisted MIME table.
var (
	//: register the singleton and expose it as a typed package var. The
	//: registered instance carries the package defaults; a caller needing
	//: other bounds builds an unregistered one with NewWithLimits.
	Codec codec.Codec = codec.Register(&multipartCodec{limits: defaultLimits()})

	//: MIME table hoisted. Registration normalises the key, so a header
	//: carrying the boundary parameter still resolves through FromMIME.
	mimeTypes = []string{mimeFormData}
)

// multipartCodec is the concrete Codec implementation for multipart/form-data.
type multipartCodec struct {
	// limits bounds every encode and every decode this instance drives.
	limits LimitsConfig
}

// defaultLimits returns the resolved package defaults.
func defaultLimits() LimitsConfig {
	//: spelled out rather than resolved from the zero value so the registered
	//: singleton can never carry a zero bound that would refuse everything.
	return LimitsConfig{
		MaxPartBytes:  DefaultMaxPartBytes,
		MaxParts:      DefaultMaxParts,
		MaxTotalBytes: DefaultMaxTotalBytes,
	}
}

// New returns the multipart codec singleton, bounded by the package defaults.
func New() codec.Codec {
	//: stateless apart from its immutable bounds — one singleton is enough.
	return Codec
}

// NewWithLimits returns an unregistered codec bounded by l. Zero fields take
// the package defaults; a negative field is refused with LimitsInvalid rather
// than guessed at (ADR 0031).
//
// The instance is deliberately NOT added to the registry: Format("multipart")
// already resolves to the default-bounded singleton, and a second registration
// under the same name would panic at boot. csv.NewWithEscape is the precedent.
func NewWithLimits(l LimitsConfig) (c codec.Codec, err error) {
	//: clamp the zeros, refuse the negatives.
	resolved, rerr := l.resolve()
	//: propagate the typed refusal verbatim.
	if rerr != nil {
		//: caller sees LIMITS_INVALID naming the offending knob.
		return nil, rerr
	}
	//: fresh instance, outside the registry.
	return &multipartCodec{limits: resolved}, nil
}

// Name implements codec.Codec.
func (*multipartCodec) Name() string {
	//: canonical identifier.
	return "multipart"
}

// MIMETypes lists every MIME alias.
func (*multipartCodec) MIMETypes() []string {
	//: hand back a copy so callers cannot mutate the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions reports no file extension.
//
// multipart/form-data is a transport container: nothing writes it to disk
// under an agreed suffix, so inventing one would make codec.FromExtension
// resolve a name no producer emits. An empty list is the honest answer.
func (*multipartCodec) Extensions() []string {
	//: deliberately empty — see the doc comment.
	return nil
}

// Marshal encodes v as a multipart/form-data body.
//
// FormValue / *FormValue / []PartValue / PartValue / *PartValue encode
// natively; every other value takes the JSON-mediated single-part shape. The
// returned bytes carry the delimiter on every line, so [ContentType] can
// recover the header value the caller must send alongside them.
func (c *multipartCodec) Marshal(v any) (encoded []byte, err error) {
	//: resolve the value to the container shape the encoder writes.
	form, ferr := asForm(v)
	//: shape or JSON failure — nothing has been allocated yet.
	if ferr != nil {
		//: propagate the typed refusal verbatim.
		return nil, ferr
	}
	//: rent an already-Reset buffer from the shared codec pool.
	buf := scratch.AcquireBuffer()
	//: drive the one write path over the pooled buffer.
	if eerr := c.encodeForm(buf, form); eerr != nil {
		//: error path: pool the buffer back with cap-discard semantics.
		scratch.ReleaseBuffer(buf)
		//: the encoder already wrapped the failure.
		return nil, eerr
	}
	//: detach the bytes; small buffers are cloned and repooled, large ones
	//: are orphaned so the caller's slice IS the buffer's storage.
	return detachAndRelease(buf), nil
}

// Unmarshal parses a multipart/form-data body into v.
//
// A *FormValue receives every part plus the recovered delimiter. Any other
// non-nil pointer receives encoding/json's decode of the [JSONPartName] part —
// the read side of the JSON-mediated shape. A body carrying no such part is a
// typed UNMARSHAL_FAILED naming *FormValue as the target that would work.
func (c *multipartCodec) Unmarshal(data []byte, v any) error {
	//: the delimiter is not in the Codec signature — recover it from the body.
	boundary, berr := Boundary(data)
	//: an unrecoverable delimiter is a typed BOUNDARY_INVALID.
	if berr != nil {
		//: caller sees the structural diagnosis.
		return berr
	}
	//: same streaming reader Unmarshal and NewDecoder share.
	dec := c.decoderFor(bytes.NewReader(data), boundary)
	//: a *FormValue target takes the whole container.
	if form, ok := v.(*FormValue); ok && form != nil {
		//: drain every part into the caller's FormValue.
		return readForm(dec, boundary, form)
	}
	//: every other target reads the JSON-mediated part.
	return readJSONMediated(dec, v)
}

// Append encodes v and appends the bytes onto dst, implementing
// codec.Appender. On failure dst is returned with its prior contents intact.
func (c *multipartCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: resolve the value to the container shape the encoder writes.
	form, ferr := asForm(v)
	//: leave dst untouched on a shape rejection.
	if ferr != nil {
		//: Appender contract: prior contents survive an error.
		return dst, ferr
	}
	//: encode into a pooled buffer first so a mid-body failure never lands
	//: partial bytes in the caller's slice.
	buf := scratch.AcquireBuffer()
	//: drive the one write path.
	if eerr := c.encodeForm(buf, form); eerr != nil {
		//: cap-discard release, dst untouched.
		scratch.ReleaseBuffer(buf)
		//: the encoder already wrapped the failure.
		return dst, eerr
	}
	//: single copy into the caller's buffer.
	out := append(dst, buf.Bytes()...)
	//: cap-discard release.
	scratch.ReleaseBuffer(buf)
	//: bytes are the caller's now.
	return out, nil
}

// NewEncoder wraps w in a streaming codec.Encoder with a generated boundary.
// The returned Encoder satisfies [BoundaryProvider], which is the only way to
// learn the delimiter it chose.
func (c *multipartCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: generated boundary — nothing here can fail.
	return c.encoderFor(w)
}

// NewDecoder wraps r in a streaming codec.Decoder, recovering the boundary
// from the head of the stream without consuming it.
//
// codec.Decoder has no error channel, so a stream whose delimiter cannot be
// recovered yields a decoder that reports the typed BOUNDARY_INVALID from its
// first Decode (and answers More() true exactly once, so a More/Decode loop
// cannot swallow it).
func (c *multipartCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: peek the head for the first delimiter line; the bytes stay in place.
	buffered, boundary, serr := sniffReader(r)
	//: park the failure on a reader-less decoder.
	if serr != nil {
		//: surfaced by the first Decode call.
		return &multipartDecoder{pendErr: serr}
	}
	//: normal path — the buffered reader still holds the whole body.
	return c.decoderFor(buffered, boundary)
}

// NewEncoderWithBoundary implements [BoundaryCodec].
func (c *multipartCodec) NewEncoderWithBoundary(w io.Writer, boundary string) (enc codec.Encoder, err error) {
	//: build the concrete encoder, then pin the caller's delimiter.
	built, berr := c.pinnedEncoder(w, boundary)
	//: propagate the typed refusal.
	if berr != nil {
		//: caller sees BOUNDARY_INVALID.
		return nil, berr
	}
	//: ready to stream.
	return built, nil
}

// NewDecoderWithBoundary implements [BoundaryCodec].
func (c *multipartCodec) NewDecoderWithBoundary(r io.Reader, boundary string) (dec codec.Decoder, err error) {
	//: validate up front so a bad boundary fails here rather than as a
	//: mysterious "no parts" result from mime/multipart.
	if verr := validateBoundary(boundary); verr != nil {
		//: typed BOUNDARY_INVALID.
		return nil, verr
	}
	//: authoritative path — nothing is recovered from the wire.
	return c.decoderFor(r, boundary), nil
}

// encoderFor builds the concrete encoder over w with a generated boundary.
func (c *multipartCodec) encoderFor(w io.Writer) *multipartEncoder {
	//: bounds are copied per encoder so concurrent streams never share counts.
	return &multipartEncoder{inner: stdmp.NewWriter(w), count: counter{limits: c.limits}}
}

// pinnedEncoder builds the concrete encoder over w framed by boundary.
func (c *multipartCodec) pinnedEncoder(w io.Writer, boundary string) (enc *multipartEncoder, err error) {
	//: start from the generated-boundary encoder.
	built := c.encoderFor(w)
	//: SetBoundary is the stdlib's own RFC 2046 gate; map its refusal to ours.
	if serr := built.inner.SetBoundary(boundary); serr != nil {
		//: typed BOUNDARY_INVALID; the value is never echoed.
		return nil, boundaryInvalid("mime/multipart rejected the caller-supplied boundary")
	}
	//: pinned and ready.
	return built, nil
}

// decoderFor builds the concrete decoder over r for an already-known boundary.
func (c *multipartCodec) decoderFor(r io.Reader, boundary string) *multipartDecoder {
	//: bounds are copied per decoder so concurrent streams never share counts.
	return &multipartDecoder{inner: stdmp.NewReader(r, boundary), count: counter{limits: c.limits}}
}

// encodeForm writes form to w through the streaming encoder — the single
// write path Marshal, Append and NewEncoder all funnel through.
func (c *multipartCodec) encodeForm(w io.Writer, form *FormValue) error {
	//: build the encoder, pinning the caller's delimiter when supplied.
	enc, eerr := c.formEncoder(w, form.Boundary)
	//: an unusable caller-supplied boundary stops before any byte is written.
	if eerr != nil {
		//: typed BOUNDARY_INVALID.
		return eerr
	}
	//: one Encode per part; bounds are charged inside. Indexed rather than
	//: ranged by value so the 72-byte PartValue is not copied per iteration.
	for i := range form.Parts {
		//: a rejected part aborts the body — the caller discards w.
		if werr := enc.Encode(&form.Parts[i]); werr != nil {
			//: already wrapped.
			return werr
		}
	}
	//: Close writes the terminating delimiter; without it the body is truncated.
	return enc.Close()
}

// formEncoder builds the encoder for a form, honouring an explicit boundary.
func (c *multipartCodec) formEncoder(w io.Writer, boundary string) (enc *multipartEncoder, err error) {
	//: no explicit delimiter — take the generated one.
	if boundary == "" {
		//: nothing can fail on this path.
		return c.encoderFor(w), nil
	}
	//: explicit delimiter — validated by mime/multipart.
	return c.pinnedEncoder(w, boundary)
}

// asForm resolves an arbitrary value to the container shape Marshal writes.
func asForm(v any) (form *FormValue, err error) {
	//: native container shapes.
	switch typed := v.(type) {
	//: value form — take its address so the container travels by pointer.
	case FormValue:
		//: nothing to resolve.
		return &typed, nil
	//: pointer form — a nil pointer carries no form.
	case *FormValue:
		//: reject before dereferencing.
		if typed == nil {
			//: typed refusal.
			return nil, valueInvalid("Marshal called with a nil *FormValue")
		}
		//: already the shape we want.
		return typed, nil
	//: a bare part list is a form with a generated delimiter.
	case []PartValue:
		//: wrap without inventing a boundary — the encoder generates one.
		return &FormValue{Parts: typed}, nil
	}
	//: PartValue / *PartValue / any other value resolve through the encoder's
	//: own single-part rule, which is where the JSON-mediated shape is defined.
	part, perr := asPart(v)
	//: propagate the typed refusal verbatim.
	if perr != nil {
		//: caller sees VALUE_INVALID or MARSHAL_FAILED.
		return nil, perr
	}
	//: one-part body.
	return &FormValue{Parts: []PartValue{*part}}, nil
}

// readForm drains every part of dec into form, stamping the delimiter that
// framed them so the form re-encodes to the same framing.
func readForm(dec *multipartDecoder, boundary string, form *FormValue) error {
	//: accumulator; a partially-filled form is never published.
	var parts []PartValue
	//: drain until the closing delimiter.
	for {
		//: one part per Decode; the decoder holds at most one body.
		var part PartValue
		//: io.EOF is the clean end-of-stream signal.
		derr := dec.Decode(&part)
		//: drained — publish and stop.
		if errIsEOF(derr) {
			//: framing travels with the parts.
			form.Boundary = boundary
			//: publish through the caller's pointer.
			form.Parts = parts
			//: nothing to wrap.
			return nil
		}
		//: any other failure aborts without touching the caller's form.
		if derr != nil {
			//: already wrapped by the decoder.
			return derr
		}
		//: accumulate in wire order.
		parts = append(parts, part)
	}
}

// readJSONMediated finds the JSONPartName part and decodes it into v.
func readJSONMediated(dec *multipartDecoder, v any) error {
	//: walk the parts looking for the SDK's JSON envelope.
	for {
		//: read the raw part so the field name is visible.
		var part PartValue
		//: io.EOF means the body carried no JSON-mediated part.
		derr := dec.Decode(&part)
		//: drained without a match — say what target WOULD have worked.
		if errIsEOF(derr) {
			//: typed UNMARSHAL_FAILED with an actionable diagnostic.
			return unmarshalFailed(nil,
				"Unmarshal: body carries no "+JSONPartName+" part; decode into a *FormValue instead")
		}
		//: any other failure propagates.
		if derr != nil {
			//: already wrapped by the decoder.
			return derr
		}
		//: skip every part that is not the JSON envelope.
		if part.Name != JSONPartName {
			//: keep draining.
			continue
		}
		//: project the envelope onto the caller's value.
		return assignPart(&part, v)
	}
}

// detachAndRelease pulls the encoded bytes out of buf, cloning-and-repooling a
// small buffer and orphaning an over-cap one so the large path pays no copy.
func detachAndRelease(buf *bytes.Buffer) []byte {
	//: an over-cap buffer would pay a huge clone AND pin the pool entry;
	//: hand its storage to the caller and let GC reclaim the header.
	if buf.Cap() > scratch.MaxRetainedBufBytes {
		//: caller-owned slice — do NOT reset the buffer.
		return buf.Bytes()
	}
	//: small buffer: clone so the caller's slice does not alias the pool.
	out := slices.Clone(buf.Bytes())
	//: reset + repool.
	scratch.ReleaseBuffer(buf)
	//: caller-owned copy.
	return out
}
