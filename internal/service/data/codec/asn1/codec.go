package asn1

import (
	stdasn1 "encoding/asn1"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	coreasn1 "github.com/kitsunium/sdk/internal/core/data/codec/asn1"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Codec is the ASN.1 DER singleton, registered with core/data/codec at package load.
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
		Code:    coreasn1.CodeASN1MarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "ASN.1 DER encoding failed",
		Private: "service/data/codec/asn1.Marshal: encoding/asn1 returned an error",
	})
}

// Append encodes v as ASN.1 DER and appends the bytes to dst. Implements
// the optional codec.Appender interface. Delegates to Marshal so the
// wrap/error contract has a single source — stdlib encoding/asn1 lacks
// a MarshalToBuffer-style direct-write API.
func (c *asn1Codec) Append(dst []byte, v any) (appended []byte, err error) {
	//: delegate to Marshal so the wrap/error contract has a single source.
	encoded, merr := c.Marshal(v)
	//: surface any encoding failure without touching dst.
	if merr != nil {
		//: return the untouched buffer plus the wrapped error.
		return dst, merr
	}
	//: append the encoded bytes onto the caller's buffer.
	return append(dst, encoded...), nil
}

// Unmarshal parses data as ASN.1 DER — with the BER relaxations
// encoding/asn1 reads — into v. Trailing bytes past the first decoded
// structure are accepted silently — this matches encoding/asn1's own lenient
// contract and preserves compatibility with callers that feed a known-sized
// prefix of a larger buffer. NewStrict's instance refuses them.
func (*asn1Codec) Unmarshal(data []byte, v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
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
		Code:    coreasn1.CodeASN1UnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "ASN.1 DER decoding failed",
		Private: "service/data/codec/asn1.Unmarshal: encoding/asn1 returned an error",
	})
}

// strictCodec is the instance NewStrict returns: the registered codec's
// encoding, and an Unmarshal that refuses bytes past the first structure.
type strictCodec struct {
	asn1Codec
}

// NewStrict returns a fresh ASN.1 DER codec, not registered, whose Unmarshal
// refuses any byte past the first decoded structure with TRAILING_DATA
// (0.3.9.3). The registered codec accepts them, as encoding/asn1 does, so a
// signed blob with bytes appended decodes equal to the original; a caller
// that hashes or verifies what it decoded, rather than what it received,
// uses this instance. Marshal and Append are the registered codec's.
func NewStrict() codec.Codec {
	//: stateless; never added to the registry.
	return &strictCodec{}
}

// Unmarshal parses data like the registered codec, then refuses a non-empty
// rest: a signed blob with bytes appended must not decode equal to the
// original. On that refusal v holds the structure encoding/asn1 decoded.
func (*strictCodec) Unmarshal(data []byte, v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	rest, uerr := stdasn1.Unmarshal(data, v)
	//: not DER, or not what the target holds.
	if uerr != nil {
		//: the registered codec's failure.
		return errs.Wrap(uerr, errs.WrapParams{
			Code:    coreasn1.CodeASN1UnmarshalFailed,
			Reason:  "UNMARSHAL_FAILED",
			Public:  "ASN.1 DER decoding failed",
			Private: "service/data/codec/asn1.NewStrict.Unmarshal: encoding/asn1 returned an error",
		})
	}
	//: bytes past the structure.
	if len(rest) > 0 {
		//: refused, naming how many — never what they hold.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    coreasn1.CodeASN1TrailingData,
			Reason:  "TRAILING_DATA",
			Public:  "ASN.1 input carries trailing data",
			Private: "service/data/codec/asn1.NewStrict.Unmarshal: bytes follow the first decoded structure",
		}, errs.Int("trailing", len(rest)))
	}
	//: exactly one structure.
	return nil
}
