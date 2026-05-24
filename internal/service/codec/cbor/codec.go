// Package cbor wraps github.com/fxamacker/cbor/v2 as a codec.Codec
// implementation. fxamacker exposes an Encoder and Decoder that stream
// individual CBOR items, so this codec also implements StreamingCodec.
package cbor

import (
	"io"
	"slices"

	gocbor "github.com/fxamacker/cbor/v2"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Security caps for hardened decoding. The library's default Unmarshal
// uses package defaults that allow up to INT32_MAX array elements —
// attacker-controlled input can trigger memory exhaustion before any
// sanity check. Values here match the library's own "security tips"
// README section.
const (
	// maxCBORArrayElements caps the slot count in any single CBOR array.
	maxCBORArrayElements int = 1 << 20
	// maxCBORMapPairs caps the key/value pair count in any single CBOR map.
	maxCBORMapPairs int = 1 << 20
	// maxCBORNestedLevels caps CBOR container nesting depth to defuse
	// deeply-nested-structure DoS attempts.
	maxCBORNestedLevels int = 32
)

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables (hoisted).
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&cborCodec{})

	//: MIME table hoisted.
	mimeTypes = []string{"application/cbor"}

	//: extension table hoisted for the same reason.
	extensions = []string{".cbor"}

	//: decMode is the reusable hardened decoder used by every Unmarshal
	//: call; built eagerly via mustHardenedDecMode so a mis-configuration
	//: crashes at package load rather than silently passing through.
	decMode gocbor.DecMode = mustHardenedDecMode()

	//: encMode is the reusable encoder mode. fxamacker amortises the
	//: per-call EncOptions resolution into one immutable EncMode value
	//: so every Marshal hits the cached resolver instead of rebuilding
	//: it. Default options (no sorting override, no float shortening)
	//: keep wire bytes byte-identical to gocbor.Marshal.
	encMode gocbor.EncMode = mustEncMode()
)

// mustEncMode builds the reusable EncMode with default options.
// gocbor.EncOptions{}.EncMode() only fails on self-contradictory options;
// the defaults are always valid so the panic guards a future library
// upgrade that tightens validation.
func mustEncMode() gocbor.EncMode {
	//: default EncOptions preserve gocbor.Marshal's wire format byte-for-byte.
	m, err := gocbor.EncOptions{}.EncMode()
	//: EncMode only fails on self-contradictory options — fail loud.
	if err != nil {
		//: crash at package load so a misconfigured default never reaches runtime.
		panic("service/codec/cbor: EncMode construction failed: " + err.Error())
	}
	//: publish the reusable encoder.
	return m
}

// mustHardenedDecMode builds the reusable DecMode with security caps and
// panics on the defensive error path. DecOptions.DecMode() only fails when
// the options themselves are self-contradictory — caps here are within the
// library's accepted range so the panic is practically unreachable, but
// guards a future library upgrade that tightens validation.
func mustHardenedDecMode() gocbor.DecMode {
	//: caps chosen per the fxamacker/cbor README Security Tips section.
	opts := gocbor.DecOptions{
		MaxArrayElements: maxCBORArrayElements,
		MaxMapPairs:      maxCBORMapPairs,
		MaxNestedLevels:  maxCBORNestedLevels,
	}
	//: build the reusable decoder; panic on the defensive error branch.
	m, err := opts.DecMode()
	//: DecMode only fails on self-contradictory options — fail loud.
	if err != nil {
		//: crash at package load so a misconfigured default never reaches runtime.
		panic("service/codec/cbor: hardened DecMode construction failed: " + err.Error())
	}
	//: publish the hardened decoder for cborCodec.Unmarshal.
	return m
}

// cborCodec is the concrete Codec implementation for CBOR.
type cborCodec struct{}

// New returns a CBOR codec instance.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*cborCodec) Name() string {
	//: canonical identifier.
	return "cbor"
}

// MIMETypes lists every MIME alias.
func (*cborCodec) MIMETypes() []string {
	//: hand back the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
func (*cborCodec) Extensions() []string {
	//: hand back the package-level slice.
	return slices.Clone(extensions)
}

// Marshal serialises v as CBOR bytes.
func (*cborCodec) Marshal(v any) (encoded []byte, err error) {
	//: route through the hoisted encMode so fxamacker reuses the cached
	//: resolver instead of rebuilding default EncOptions on every call.
	out, merr := encMode.Marshal(v)
	//: success fast-path.
	if merr == nil {
		//: return the encoded bytes verbatim.
		return out, nil
	}
	//: wrap the library error for reason-based matching.
	return nil, errs.Wrap(merr, errs.WrapParams{
		Code:    CodeCBORMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "CBOR encoding failed",
		Private: "service/codec/cbor.Marshal: fxamacker/cbor/v2 returned an error",
	})
}

// Unmarshal parses data as CBOR into v.
func (*cborCodec) Unmarshal(data []byte, v any) error {
	//: route through the hardened DecMode so attacker-controlled input
	//: cannot trigger memory exhaustion via huge arrays, huge maps, or
	//: deeply-nested structures.
	uerr := decMode.Unmarshal(data, v)
	//: success fast-path.
	if uerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library error.
	return errs.Wrap(uerr, errs.WrapParams{
		Code:    CodeCBORUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "CBOR decoding failed",
		Private: "service/codec/cbor.Unmarshal: fxamacker/cbor/v2 returned an error",
	})
}

// NewEncoder wraps w in a streaming codec.Encoder. Routes through the
// hoisted encMode so fxamacker reuses the cached resolver.
func (*cborCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: encMode.NewEncoder reuses the cached resolver — matches Marshal.
	return &cborEncoder{inner: encMode.NewEncoder(w)}
}

// NewDecoder wraps r in a streaming codec.Decoder. Routes through the
// hardened decMode so the security caps apply to streaming Unmarshal too
// — fixes the streaming-decode hardening gap where the package-level
// gocbor.NewDecoder bypassed maxCBORArrayElements / maxCBORMapPairs /
// maxCBORNestedLevels.
func (*cborCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: decMode.NewDecoder applies the same caps as Unmarshal.
	return &cborDecoder{inner: decMode.NewDecoder(r)}
}
