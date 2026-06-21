// Package hcl wraps HashiCorp HCL v2 as a codec.Codec implementation. It lives
// under third-party/ (root module) — NOT internal/service/codec — because
// hashicorp/hcl/v2 pulls go-cty and a heavier dependency graph; quarantining it
// here keeps the dep-light service module (and its proc syscall code) untouched
// (ADR 0012 / ADR 0021 §Why-not / ADR 0022). It is opt-in: a consumer
// blank-imports this package to register the "hcl" Format; pkg/v1/codec does
// NOT pull it (the public module stays dep-light).
//
// HCL is decode-oriented; symmetric value-marshal is struct-only via
// gohcl.EncodeIntoBody, so the top-level value MUST be a struct (or pointer to
// one) with `hcl:"…"` field tags. Non-streaming; implements the optional Appender.
package hcl

import (
	"reflect"
	"slices"

	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsimple"
	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxHCLBytes caps Unmarshal input for untrusted payloads (CWE-400) before the
// parser allocates. 10 MiB matches the other library-backed codecs.
const maxHCLBytes int = 10 << 20

// decodeFilename is the synthetic name hclsimple.Decode reports in diagnostics.
const decodeFilename string = "input.hcl"

var (
	// Codec is the registered HCL singleton.
	Codec codec.Codec = codec.Register(&hclCodec{})

	// mimeTypes is hoisted so MIMETypes does not allocate the literal per call.
	mimeTypes = []string{"application/hcl"}

	// extensions is hoisted for the same reason.
	extensions = []string{".hcl"}
)

// hclCodec is the concrete Codec implementation for HCL. Stateless.
type hclCodec struct{}

// New returns the HCL codec singleton.
func New() codec.Codec {
	//: stateless — one singleton serves the whole process.
	return Codec
}

// Name returns the canonical Format identifier.
func (*hclCodec) Name() string {
	//: the registered Format string.
	return "hcl"
}

// MIMETypes returns a defensive copy of the MIME alias list.
func (*hclCodec) MIMETypes() []string {
	//: callers must not mutate the package-level table.
	return slices.Clone(mimeTypes)
}

// Extensions returns a defensive copy of the file-extension list.
func (*hclCodec) Extensions() []string {
	//: callers must not mutate the package-level table.
	return slices.Clone(extensions)
}

// structLike reports whether rt is a struct or a pointer to a struct — the
// only shapes gohcl.EncodeIntoBody accepts at the top level. A nil rt (the
// dynamic type of a nil interface value) is never encodable.
func structLike(rt reflect.Type) bool {
	//: a nil type means a nil interface value — reject up front.
	if rt == nil {
		//: not encodable.
		return false
	}
	//: deref one pointer level if present.
	if rt.Kind() == reflect.Pointer {
		//: inspect the pointee instead.
		rt = rt.Elem()
	}
	//: HCL bodies are built from struct fields only.
	return rt.Kind() == reflect.Struct
}

// Marshal encodes v as HCL. v must be a struct (or *struct) with hcl tags;
// anything else returns HCL_MARSHAL_FAILED. A gohcl panic on an unsupported
// field type is recovered into the same sentinel.
func (*hclCodec) Marshal(v any) (encoded []byte, err error) {
	//: top-level must be struct-like (gohcl.EncodeIntoBody contract).
	if !structLike(reflect.TypeOf(v)) {
		//: surface the marshal sentinel rather than letting gohcl panic.
		return nil, errs.Wrap(nil, errs.WrapParams{
			Code:    CodeHCLMarshalFailed,
			Reason:  "HCL_MARSHAL_FAILED",
			Public:  "HCL encoding failed",
			Private: "third-party/codec/hcl.Marshal: top-level value is not a struct",
		})
	}
	//: gohcl.EncodeIntoBody panics on an unsupported field type; recover it.
	defer func() {
		//: convert a library panic into the marshal sentinel.
		if r := recover(); r != nil {
			//: discard any partial output.
			encoded = nil
			//: wrap the recovered panic value.
			err = errs.Wrap(nil, errs.WrapParams{
				Code:    CodeHCLMarshalFailed,
				Reason:  "HCL_MARSHAL_FAILED",
				Public:  "HCL encoding failed",
				Private: "third-party/codec/hcl.Marshal: gohcl.EncodeIntoBody panicked on an unsupported field type",
			})
		}
	}()
	//: build a fresh hclwrite file and encode v into its body.
	f := hclwrite.NewEmptyFile()
	gohcl.EncodeIntoBody(v, f.Body())
	//: hand back the serialised HCL bytes.
	return f.Bytes(), nil
}

// Unmarshal parses+decodes HCL into v (a non-nil pointer to a struct/map) after
// the size cap.
func (*hclCodec) Unmarshal(data []byte, v any) error {
	//: CWE-400 defence — refuse oversized inputs before the parser allocates.
	if len(data) > maxHCLBytes {
		//: surface the size sentinel.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeHCLSizeExceeded,
			Reason:  "HCL_SIZE_EXCEEDED",
			Public:  "HCL input exceeds size limit",
			Private: "third-party/codec/hcl.Unmarshal: len(data) > maxHCLBytes",
		})
	}
	//: parse + decode in one step; nil EvalContext = no variables/functions.
	derr := hclsimple.Decode(decodeFilename, data, nil, v)
	//: success fast-path.
	if derr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the diagnostics with the dotted-quad code.
	return errs.Wrap(derr, errs.WrapParams{
		Code:    CodeHCLUnmarshalFailed,
		Reason:  "HCL_UNMARSHAL_FAILED",
		Public:  "HCL decoding failed",
		Private: "third-party/codec/hcl.Unmarshal: hclsimple.Decode returned diagnostics",
	})
}

// Append encodes v as HCL and appends it onto dst (optional codec.Appender).
// HCL has no in-place API, so the encode allocates; a failure leaves dst intact.
func (c *hclCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: snapshot dst so a failure restores the caller's buffer exactly.
	origLen := len(dst)
	//: encode (Marshal already wraps + recovers any error).
	out, merr := c.Marshal(v)
	//: propagate a marshal failure, leaving dst untouched.
	if merr != nil {
		//: restore dst to its original length.
		return dst[:origLen], merr
	}
	//: append the encoded HCL onto dst.
	return append(dst, out...), nil
}
