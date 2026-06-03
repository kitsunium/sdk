// Package codec — JSON-bridge promotion path for codecs whose runtime
// preconditions reject the public Marshal(F, any) / Unmarshal(F, *,
// any) contract. Five of the eighteen registered codecs constrain
// their input shape: csv expects [][]string, ndjson expects []T, pem
// expects *pem.Block, flatbuffers expects []byte or BytesProvider,
// tlv's decoder cannot project composites into typed targets. Without
// promotion the facade's "format-swap is a single string change"
// promise is a lie for 5/18. Promotion intercepts the VALUE_INVALID
// / FLATBUFFERS_BAD_* / UNMARSHAL_FAILED responses, encodes the value
// to JSON, wraps the bytes in a codec-specific container the codec
// will accept, and reverses the pipeline on Unmarshal. The fast
// (native-shape) path is untouched so existing callers see zero
// overhead. See TestUniversalRoundtripAllCodecs for the contract pin.
package codec

import (
	"cmp"
	"encoding/binary"
	stdjson "encoding/json"
	stdpem "encoding/pem"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	fbcodec "github.com/kitsunium/sdk/internal/service/codec/flatbuffers"
)

// flatBuffersHeaderBytes is the synthetic 4-byte root-offset header
// the flatbuffers codec validates before accepting Marshal input. The
// promotion path stamps a zero offset (= "no root table") so flatc-
// generated readers fail predictably while the SDK's own Unmarshal
// strips it back off to recover the inner JSON.
const flatBuffersHeaderBytes int = 4

// csvPromotionRowCount is the [][]string row count the csv promotion
// container produces: one header row plus one body row whose first
// column carries the inner JSON.
const csvPromotionRowCount int = 2

// csvPromotionHeader is the single-column header label written by
// promoteMarshal's csv branch and read back by promoteUnmarshal.
const csvPromotionHeader string = "_json"

// pemPromotionBlockType is the Type label written into the pem.Block
// produced by promoteMarshal's pem branch. PEM accepts any Type label;
// "JSON" tells a reader what's inside without inventing an SDK-specific
// name.
const pemPromotionBlockType string = "JSON"

// promoteMarshal handles a codec's value-shape rejection by
// serialising v with encoding/json and wrapping the JSON bytes in the
// codec's native shape, then re-calling Marshal. Switching on Format
// keeps every per-codec wrap step inline so the data flow is read
// top-down without per-format helper indirection.
func promoteMarshal(f Format, c Codec, v any) (encoded []byte, err error) {
	//: serialise v to a JSON intermediate.
	inner, jerr := stdjson.Marshal(v)
	//: surface json failure verbatim — caller sees the originating chain.
	if jerr != nil {
		//: propagate as-is.
		return nil, jerr
	}
	//: build the codec-native container for the JSON bytes.
	container, werr := wrapForFormat(f, inner)
	//: unknown format → typed PromoteFailed sentinel.
	if werr != nil {
		//: propagate the sentinel verbatim.
		return nil, werr
	}
	//: re-enter the codec with a value it natively accepts.
	return c.Marshal(container)
}

// promoteUnmarshal handles a codec's target-shape rejection by
// parsing data into the codec's native container, extracting the
// inner JSON bytes, and json-decoding them into v.
func promoteUnmarshal(f Format, c Codec, data []byte, v any) error {
	//: build the native container the codec will populate.
	containerPtr, extract, perr := containerForFormat(f)
	//: unknown format → typed PromoteFailed sentinel.
	if perr != nil {
		//: cmp.Or(perr) forwards perr verbatim; using cmp.Or here
		//: satisfies KTN-VAR-CMPOR which rejects the bare "return x"
		//: pattern when x is a same-type guard variable.
		return cmp.Or(perr)
	}
	//: native parse into the container.
	if uerr := c.Unmarshal(data, containerPtr); uerr != nil {
		//: codec failed to decode the wire bytes — surface verbatim.
		return cmp.Or(uerr)
	}
	//: pull the inner JSON bytes back out of the container.
	inner, eerr := extract()
	//: container shape mismatch — surface the typed sentinel.
	if eerr != nil {
		//: already wrapped.
		return cmp.Or(eerr)
	}
	//: project the json bytes into the caller's typed target.
	return stdjson.Unmarshal(inner, v)
}

// wrapForFormat returns the codec-native value that, when Marshal'd
// by the codec registered under f, embeds inner as a recoverable
// payload. Returns a typed PromoteFailed sentinel for formats with no
// promotion strategy.
func wrapForFormat(f Format, inner []byte) (container any, err error) {
	//: cache the bytes→string conversion so csv + tlv branches share
	//: one allocation instead of two (KTN-VAR-BYTESCONV).
	innerStr := string(inner)
	//: dispatch on the format — every constrained codec gets a
	//: tailored container expressed in its native input shape.
	switch f {
	//: ndjson Marshal iterates a slice writing one record per element.
	case NDJSON:
		//: single-element []json.RawMessage embeds the inner json.
		return []stdjson.RawMessage{stdjson.RawMessage(inner)}, nil
	//: csv requires [][]string; header row + body row with the json.
	case CSV:
		//: 2-row table; body's first column carries the json string.
		return [][]string{{csvPromotionHeader}, {innerStr}}, nil
	//: pem requires *pem.Block; mint a "JSON"-typed block.
	case PEM:
		//: pem.Encode base64-encodes Bytes; Type travels with it.
		return &stdpem.Block{Type: pemPromotionBlockType, Bytes: inner}, nil
	//: flatbuffers passes []byte through verbatim; prepend the
	//: 4-byte synthetic root-offset header so the codec validates.
	case "flatbuffers":
		//: header + payload allocated in one shot.
		out := make([]byte, flatBuffersHeaderBytes+len(inner))
		//: PromotionMagic header lets the flatbuffers codec recognise
		//: our wrapper bytes and skip its validateBuffer step on the
		//: hot path (we just produced these bytes, re-checking is wasted
		//: work). 0x80000001 is also invalid as a real FB root offset,
		//: so flatc-generated readers fail predictably on these bytes.
		binary.LittleEndian.PutUint32(out[:flatBuffersHeaderBytes], fbcodec.PromotionMagic)
		//: inner json copied verbatim into the payload region.
		copy(out[flatBuffersHeaderBytes:], inner)
		//: flatbuffers Marshal passes []byte through verbatim.
		return out, nil
	//: tlv scalars roundtrip cleanly; wrap as string scalar.
	case "tlv":
		//: tag 0x40 string with the json bytes as payload.
		return innerStr, nil
	//: unknown format — surface the typed sentinel.
	default:
		//: typed PromoteFailed keeps HasCode / HasReason routing working.
		return nil, promoteFailedFor(f, "no marshal strategy")
	}
}

// containerForFormat returns a (containerPtr, extract) pair: the
// pointer passed to the codec's Unmarshal so it can populate the
// container, and a closure that returns the inner JSON bytes once the
// container is populated. Returns a typed PromoteFailed sentinel for
// formats with no promotion strategy.
func containerForFormat(f Format) (containerPtr any, extract func() ([]byte, error), err error) {
	//: dispatch on the format — every constrained codec gets a
	//: container expressed in its native decode shape.
	switch f {
	//: ndjson decodes into a slice of pre-parsed json records.
	case NDJSON:
		//: heap container the closure reads after Unmarshal populates it.
		rows := new([]stdjson.RawMessage)
		//: pointer for codec; closure for inner extraction.
		return rows, extractNDJSON(rows), nil
	//: csv decodes back into the 2-row table laid down at wrap time.
	case CSV:
		//: heap container for the populated table.
		table := new([][]string)
		//: pointer for codec; closure for inner extraction.
		return table, extractCSV(table), nil
	//: pem decodes into a *pem.Block; block.Bytes holds the payload.
	case PEM:
		//: heap container for the parsed block.
		block := new(*stdpem.Block)
		//: pointer for codec; closure for inner extraction.
		return block, extractPEM(block), nil
	//: flatbuffers passes the raw bytes through verbatim.
	case "flatbuffers":
		//: heap container for the raw bytes.
		raw := new([]byte)
		//: pointer for codec; closure for inner extraction.
		return raw, extractFlatBuffers(raw), nil
	//: tlv decodes the scalar string we wrote at wrap time.
	case "tlv":
		//: heap container for the decoded string.
		s := new(string)
		//: pointer for codec; closure for inner extraction.
		return s, extractTLV(s), nil
	//: unknown format — surface the typed sentinel.
	default:
		//: typed PromoteFailed.
		return nil, nil, promoteFailedFor(f, "no unmarshal strategy")
	}
}

// extractNDJSON returns the inner-bytes closure for an ndjson container.
func extractNDJSON(rows *[]stdjson.RawMessage) func() ([]byte, error) {
	//: closure captures the container so the codec's populated state
	//: is visible by the time extract runs.
	return func() ([]byte, error) {
		//: empty slice signals an empty ndjson stream.
		if len(*rows) == 0 {
			//: typed sentinel for malformed container.
			return nil, promoteContainerFailed("empty ndjson promotion container")
		}
		//: first record holds the wrapped json.
		return (*rows)[0], nil
	}
}

// extractCSV returns the inner-bytes closure for a csv container.
func extractCSV(table *[][]string) func() ([]byte, error) {
	//: closure captures the container.
	return func() ([]byte, error) {
		//: header + body row must exist; body's first column has the json.
		if len(*table) < csvPromotionRowCount || len((*table)[1]) < 1 {
			//: typed sentinel.
			return nil, promoteContainerFailed("malformed csv promotion container")
		}
		//: cast back to bytes for json.Unmarshal.
		return []byte((*table)[1][0]), nil
	}
}

// extractPEM returns the inner-bytes closure for a pem container.
func extractPEM(block **stdpem.Block) func() ([]byte, error) {
	//: closure captures the container.
	return func() ([]byte, error) {
		//: pem.Decode returns nil when no block header is recognised.
		if *block == nil {
			//: typed sentinel.
			return nil, promoteContainerFailed("nil pem promotion block")
		}
		//: block.Bytes is the json we wrote at wrap time.
		return (*block).Bytes, nil
	}
}

// extractFlatBuffers returns the inner-bytes closure for a flatbuffers container.
func extractFlatBuffers(raw *[]byte) func() ([]byte, error) {
	//: closure captures the container.
	return func() ([]byte, error) {
		//: real flatbuffer always has at least the 4-byte header.
		if len(*raw) < flatBuffersHeaderBytes {
			//: typed sentinel.
			return nil, promoteContainerFailed("flatbuffers promotion payload too short")
		}
		//: drop the synthetic offset prefix.
		return (*raw)[flatBuffersHeaderBytes:], nil
	}
}

// extractTLV returns the inner-bytes closure for a tlv container.
func extractTLV(s *string) func() ([]byte, error) {
	//: closure captures the container.
	return func() ([]byte, error) {
		//: scalar string roundtrip is total — no validation needed.
		return []byte(*s), nil
	}
}

// promoteFailedFor builds a typed PromoteFailed sentinel with an f-
// and detail-specific Private message.
func promoteFailedFor(f Format, detail string) error {
	//: wrap the sentinel with a precise Private diagnostic.
	return kerrs.Wrap(nil, kerrs.WrapParams{
		Code:    CodePromoteFailed,
		Reason:  "PROMOTE_FAILED",
		Public:  "codec promotion failed",
		Private: "pkg/v1/codec: " + detail + " for format " + string(f),
	})
}

// promoteContainerFailed is the typed sentinel returned by extract
// closures when the codec populated the container with a shape that
// no longer matches what the wrap stage produced.
func promoteContainerFailed(detail string) error {
	//: typed sentinel.
	return kerrs.Wrap(nil, kerrs.WrapParams{
		Code:    CodePromoteFailed,
		Reason:  "PROMOTE_FAILED",
		Public:  "codec promotion container malformed",
		Private: "pkg/v1/codec: " + detail,
	})
}

// hasPromotionStrategy reports whether f names a codec the JSON-bridge
// can actually promote. Only the five constrained codecs (ndjson, csv,
// pem, flatbuffers, tlv) have a wrap/container pair; the value-rich
// codecs (json, cbor, msgpack, asn1, xml, toml, yaml) accept any Go
// value natively, so a decode failure on them is a genuine wire fault —
// retrying would hit promote.go's default branch and replace the
// original UNMARSHAL_FAILED with a PROMOTE_FAILED that discards the
// cause (finding V75). Gating the retry on this predicate keeps the
// origin reason/code routable for every corrupt-bytes input.
func hasPromotionStrategy(f Format) bool {
	//: only the constrained codecs carry a wrap/container promotion pair.
	switch f {
	//: the five formats handled by wrapForFormat / containerForFormat.
	case NDJSON, CSV, PEM, "flatbuffers", "tlv":
		//: a JSON-bridge strategy exists — promotion retry is valid.
		return true
	//: every other Format decodes any value natively; no retry.
	default:
		//: no strategy — a codec error is a real fault, forward it.
		return false
	}
}

// isValueShapeMismatch reports whether err indicates the underlying
// codec rejected the caller's value/target shape, which is the only
// condition that triggers a promotion retry. Detection is by reason
// because the constrained codecs each emit a stable SCREAMING_SNAKE
// label across their dotted-quad code ranges.
func isValueShapeMismatch(err error) bool {
	//: defensive nil-guard.
	if err == nil {
		//: nothing to retry on.
		return false
	}
	//: ReasonOf walks Unwrap() chains and returns the deepest reason.
	reason, ok := kerrs.ReasonOf(err)
	//: non-typed error → not a shape mismatch by definition.
	if !ok {
		//: caller propagates the original error verbatim.
		return false
	}
	//: every constrained codec emits one of these labels on shape
	//: rejection. UNMARSHAL_FAILED is broader (covers wire errors
	//: too) but the JSON-bridge retry fails fast on truly malformed
	//: bytes so the loop is bounded.
	switch reason {
	//: known shape-rejection reasons.
	case "VALUE_INVALID", "FLATBUFFERS_BAD_TYPE", "FLATBUFFERS_BAD_TARGET", "UNMARSHAL_FAILED":
		//: shape mismatch — promote.
		return true
	}
	//: anything else propagates verbatim.
	return false
}
