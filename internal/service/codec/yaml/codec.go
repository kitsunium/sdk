// Package yaml wraps gopkg.in/yaml.v3 as a codec.Codec implementation.
// Streaming is supported: yaml.v3 exposes Encoder/Decoder types that can
// serialise a sequence of documents separated by `---` markers.
package yaml

import (
	"bytes"
	"io"
	"slices"
	"sync"

	goyaml "gopkg.in/yaml.v3"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxYAMLBytes caps the byte size Unmarshal accepts from untrusted input.
// gopkg.in/yaml.v3 caps alias-expansion internally (since v3.0.0), but a
// single very large YAML document still forces the whole buffer into
// memory before any structural check. 10 MiB comfortably covers every
// realistic configuration document while preventing memory-exhaustion
// DoS on attacker-controlled payloads (CWE-400 / CWE-776). Streaming
// callers that legitimately need larger inputs use NewDecoder with
// their own io.LimitReader sizing.
const maxYAMLBytes int = 10 << 20

// maxRetainedBufBytes caps the size of *bytes.Buffer instances re-pooled
// by Marshal / Append. A one-off oversized payload would otherwise pin
// a large buffer for the lifetime of the pool's GC window. 256 KiB is
// the project-wide threshold (codec-perf-extreme initiative).
const maxRetainedBufBytes int = 256 << 10

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables + the Marshal-side buffer pool.
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&yamlCodec{})

	//: MIME table hoisted.
	mimeTypes = []string{"application/yaml", "text/yaml", "application/x-yaml"}

	//: extension table hoisted for the same reason.
	extensions = []string{".yaml", ".yml"}

	//: bufferPool reuses *bytes.Buffer across Marshal / Append calls.
	//: yaml.v3 has no Encoder.Reset(w) — we pool only the buffer and
	//: spin up a fresh Encoder per call. Saves the bytes.Buffer header
	//: allocation + the geometric grow cascade between consecutive
	//: encodes.
	bufferPool = sync.Pool{
		New: func() any { return new(bytes.Buffer) },
	}
)

// yamlCodec is the concrete Codec implementation for YAML.
type yamlCodec struct{}

// New returns a YAML codec instance.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*yamlCodec) Name() string {
	//: canonical identifier.
	return "yaml"
}

// MIMETypes lists every MIME alias.
func (*yamlCodec) MIMETypes() []string {
	//: hand back the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
func (*yamlCodec) Extensions() []string {
	//: hand back the package-level slice.
	return slices.Clone(extensions)
}

// Marshal serialises v as YAML bytes. Routes through a pooled
// *bytes.Buffer + goyaml.NewEncoder so the per-call bytes.Buffer
// allocation goyaml.Marshal pays internally is amortised across calls.
func (*yamlCodec) Marshal(v any) (encoded []byte, err error) {
	//: rent the output buffer; pool guarantees a *bytes.Buffer.
	buf, ok := bufferPool.Get().(*bytes.Buffer)
	//: pool invariant guard — never expected to fail at runtime.
	if !ok {
		//: invariant broken — fail loud at the call site.
		panic("service/codec/yaml: bufferPool yielded non-*bytes.Buffer")
	}
	//: start clean — pool may return a partially-filled buffer.
	buf.Reset()
	//: yaml.v3 Encoder has no Reset(w) — fresh one per call.
	enc := goyaml.NewEncoder(buf)
	//: encode into the pooled buffer.
	if merr := enc.Encode(v); merr != nil {
		//: drop the buffer back to the pool if not oversized.
		releaseBuffer(buf)
		//: wrap the library error for reason-based matching.
		return nil, errs.Wrap(merr, errs.WrapParams{
			Code:    CodeYAMLMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "YAML encoding failed",
			Private: "service/codec/yaml.Marshal: gopkg.in/yaml.v3 returned an error",
		})
	}
	//: yaml.v3 requires Close to flush the trailing document marker
	//: + any pending state before the encoded bytes are complete.
	if cerr := enc.Close(); cerr != nil {
		//: drop the buffer back to the pool if not oversized.
		releaseBuffer(buf)
		//: surface the close failure as a marshal error.
		return nil, errs.Wrap(cerr, errs.WrapParams{
			Code:    CodeYAMLMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "YAML encoding failed",
			Private: "service/codec/yaml.Marshal: encoder.Close returned an error",
		})
	}
	//: detach: clone the buffer's bytes so the returned slice does not
	//: alias the pooled buffer (next caller would overwrite it).
	out := slices.Clone(buf.Bytes())
	//: cap-discard release.
	releaseBuffer(buf)
	//: success — bytes are the caller's now.
	return out, nil
}

// releaseBuffer returns buf to the pool unless its capacity exceeds the
// cap-discard threshold.
func releaseBuffer(buf *bytes.Buffer) {
	//: oversized buffers would pin large allocations for the lifetime
	//: of the pool's GC window — drop them instead.
	if buf.Cap() > maxRetainedBufBytes {
		//: orphan the buffer; the GC will reclaim it.
		return
	}
	//: pool expects a clean buffer.
	buf.Reset()
	//: return for the next caller.
	bufferPool.Put(buf)
}

// Unmarshal parses data as YAML into v.
func (*yamlCodec) Unmarshal(data []byte, v any) error {
	//: cap input size so attacker-controlled payloads cannot exhaust RAM; yaml.v3 caps alias expansion internally but has no upstream byte
	//: budget, so 10 MiB is the project-wide safe default.
	if len(data) > maxYAMLBytes {
		//: surface an UNMARSHAL_FAILED with a diagnostic Private message.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeYAMLUnmarshalFailed,
			Reason:  "UNMARSHAL_FAILED",
			Public:  "YAML input exceeds size limit",
			Private: "service/codec/yaml.Unmarshal: len(data) exceeds maxYAMLBytes",
		}, errs.Int("len", len(data)), errs.Int("cap", maxYAMLBytes))
	}
	//: delegate to yaml.v3 for the actual decoding.
	uerr := goyaml.Unmarshal(data, v)
	//: success fast-path.
	if uerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library error.
	return errs.Wrap(uerr, errs.WrapParams{
		Code:    CodeYAMLUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "YAML decoding failed",
		Private: "service/codec/yaml.Unmarshal: gopkg.in/yaml.v3 returned an error",
	})
}

// Append encodes v as YAML and appends the bytes to dst. Implements the
// optional codec.Appender interface so hot-path callers can stream
// records into a recycled buffer. Avoids the double-copy the Marshal
// delegation shape paid (slices.Clone inside Marshal → append into dst)
// by encoding directly into a pooled *bytes.Buffer and appending its
// contents onto dst in a single copy step.
func (*yamlCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: rent the output buffer; pool guarantees a *bytes.Buffer.
	buf, ok := bufferPool.Get().(*bytes.Buffer)
	//: pool invariant guard — never expected to fail at runtime.
	if !ok {
		//: invariant broken — fail loud at the call site.
		panic("service/codec/yaml: bufferPool yielded non-*bytes.Buffer")
	}
	//: start clean — pool may return a partially-filled buffer.
	buf.Reset()
	//: yaml.v3 Encoder has no Reset(w) — fresh one per call.
	enc := goyaml.NewEncoder(buf)
	//: encode into the pooled buffer.
	if merr := enc.Encode(v); merr != nil {
		//: cap-discard release; leave dst pristine, surface the wrapped error.
		releaseBuffer(buf)
		//: wrap the library error for reason-based matching.
		return dst, errs.Wrap(merr, errs.WrapParams{
			Code:    CodeYAMLMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "YAML encoding failed",
			Private: "service/codec/yaml.Append: gopkg.in/yaml.v3 returned an error",
		})
	}
	//: yaml.v3 requires Close to flush the trailing document marker
	//: + any pending state before the encoded bytes are complete.
	if cerr := enc.Close(); cerr != nil {
		//: cap-discard release; dst pristine on close failure too.
		releaseBuffer(buf)
		//: surface the close failure as a marshal error.
		return dst, errs.Wrap(cerr, errs.WrapParams{
			Code:    CodeYAMLMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "YAML encoding failed",
			Private: "service/codec/yaml.Append: encoder.Close returned an error",
		})
	}
	//: append the encoded bytes onto the caller's buffer (1 copy total).
	dst = append(dst, buf.Bytes()...)
	//: cap-discard release.
	releaseBuffer(buf)
	//: success — bytes are the caller's now.
	return dst, nil
}

// NewEncoder wraps w in a streaming codec.Encoder.
func (*yamlCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: wrap the yaml.v3 encoder to expose our Close contract.
	return &yamlEncoder{inner: goyaml.NewEncoder(w)}
}

// NewDecoder wraps r in a streaming codec.Decoder.
func (*yamlCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: wrap the yaml.v3 decoder to expose More on our interface.
	return &yamlDecoder{inner: goyaml.NewDecoder(r)}
}
