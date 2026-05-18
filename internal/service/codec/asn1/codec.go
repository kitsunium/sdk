// Package asn1 wraps encoding/asn1 as a codec.Codec implementation.
// Only DER (Distinguished Encoding Rules) is emitted because the stdlib
// produces DER exclusively; BER parsing is still accepted on Unmarshal.
package asn1

import (
	stdasn1 "encoding/asn1"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Codec is the ASN.1 DER singleton, registered with core/codec at package load.
// Binding the registration result to a named var is more idiomatic than
// `var _ = codec.Register(...)` and keeps us clear of init().
var Codec codec.Codec = codec.Register(&asn1Codec{})

// asn1Codec is the concrete Codec implementation for ASN.1 DER.
type asn1Codec struct{}

// New returns an ASN.1 DER codec instance.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*asn1Codec) Name() string {
	//: canonical identifier — "-der" disambiguates from BER/CER peers.
	return "asn1-der"
}

// MIMETypes lists every MIME alias.
func (*asn1Codec) MIMETypes() []string {
	//: x.509 authorities registered application/pkix-* for DER bytes.
	return []string{"application/pkix-cert"}
}

// Extensions lists every file extension.
func (*asn1Codec) Extensions() []string {
	//: .der is the canonical DER extension; .cer is accepted as a historical alias.
	return []string{".der", ".cer"}
}

// Marshal encodes v into ASN.1 DER bytes.
func (*asn1Codec) Marshal(v any) (encoded []byte, err error) {
	//: delegate to the stdlib for the actual encoding.
	out, merr := stdasn1.Marshal(v)
	//: success fast-path.
	if merr == nil {
		//: return the encoded bytes verbatim.
		return out, nil
	}
	//: wrap the stdlib error for reason-based matching.
	return nil, errs.Wrap(merr, errs.WrapParams{
		Code:    CodeASN1MarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "ASN.1 DER encoding failed",
		Private: "service/codec/asn1.Marshal: encoding/asn1 returned an error",
	})
}

// Unmarshal parses data as ASN.1 BER/DER into v. Trailing bytes past the
// first decoded structure are accepted silently — this matches
// encoding/asn1's own lenient contract and preserves compatibility with
// callers that feed a known-sized prefix of a larger buffer. If strict
// trailing-byte rejection becomes a requirement, add a dedicated
// UnmarshalStrict helper rather than tightening this default.
func (*asn1Codec) Unmarshal(data []byte, v any) error {
	//: encoding/asn1.Unmarshal returns the remaining bytes; we ignore them
	//: per the contract documented on the function comment.
	_, uerr := stdasn1.Unmarshal(data, v)
	//: success fast-path.
	if uerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the stdlib error.
	return errs.Wrap(uerr, errs.WrapParams{
		Code:    CodeASN1UnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "ASN.1 DER decoding failed",
		Private: "service/codec/asn1.Unmarshal: encoding/asn1 returned an error",
	})
}
