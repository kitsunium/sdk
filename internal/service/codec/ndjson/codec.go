// Package ndjson implements newline-delimited JSON as a codec.Codec.
// Each record is a JSON value followed by a single '\n' — the format is
// intentionally line-oriented so that partial reads remain parseable.
// The codec accepts slice values: Marshal emits one record per element,
// Unmarshal splits on '\n' and decodes each non-empty line into a new
// slice element.
package ndjson

import (
	"bytes"
	stdjson "encoding/json"
	"reflect"
	"slices"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// scannerMaxCapacity caps a single NDJSON record at 10 MiB; beyond that the
// record likely represents a consumer bug or a corrupt stream. Enforced
// directly by the line splitter — no scanner buffer involved.
const scannerMaxCapacity int = 10 * 1024 * 1024

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables (hoisted).
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&ndjsonCodec{})

	//: MIME table hoisted.
	mimeTypes = []string{"application/x-ndjson", "application/jsonl"}

	//: extension table hoisted for the same reason.
	extensions = []string{".ndjson", ".jsonl"}
)

// ndjsonCodec is the concrete Codec implementation for NDJSON.
type ndjsonCodec struct{}

// New returns an NDJSON codec instance.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*ndjsonCodec) Name() string {
	//: canonical identifier.
	return "ndjson"
}

// MIMETypes lists every MIME alias.
func (*ndjsonCodec) MIMETypes() []string {
	//: hand back the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
func (*ndjsonCodec) Extensions() []string {
	//: hand back the package-level slice.
	return slices.Clone(extensions)
}

// Marshal encodes a slice v as NDJSON bytes. Pre-encoded raw payloads
// (`[]json.RawMessage` or `[][]byte` — the exact shape the promotion
// path in pkg/v1/codec/promote.go produces, and the natural shape for
// callers proxying already-JSON records) take a fast-path that bypasses
// the per-element stdjson.Marshal reflect walk.
func (*ndjsonCodec) Marshal(v any) (encoded []byte, err error) {
	//: pre-encoded fast-path — promotion + proxy callers hit this.
	if out, ok := marshalRawSliceTo(nil, v); ok {
		//: bytes are caller-owned now.
		return out, nil
	}
	//: resolve v to a reflect.Value that is a slice or array.
	slice, ok := asSlice(v)
	//: shape-the-input rejection.
	if !ok {
		//: loud failure — caller passed a non-slice.
		return nil, errs.Wrap(nil, errs.WrapParams{
			Code:    CodeNDJSONValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "NDJSON codec requires a slice value",
			Private: "service/codec/ndjson.Marshal: argument is not a slice",
		})
	}
	//: buffer the output so the caller gets []byte.
	var buf bytes.Buffer
	//: encode record-by-record; one '\n' per element.
	for i := range slice.Len() {
		//: delegate per-record JSON encoding to the stdlib.
		line, merr := stdjson.Marshal(slice.Index(i).Interface())
		//: surface any per-record failure immediately.
		if merr != nil {
			//: wrap the stdlib error for reason-based matching.
			return nil, errs.Wrap(merr, errs.WrapParams{
				Code:    CodeNDJSONMarshalFailed,
				Reason:  "MARSHAL_FAILED",
				Public:  "NDJSON encoding failed",
				Private: "service/codec/ndjson.Marshal: encoding/json returned an error",
			})
		}
		//: append the record and the mandatory newline.
		buf.Write(line)
		//: RFC-compliant NDJSON separator.
		buf.WriteByte('\n')
	}
	//: hand back the buffered bytes.
	return buf.Bytes(), nil
}

// marshalRawSliceTo writes a pre-encoded slice (one of []json.RawMessage
// or [][]byte) into dst as NDJSON. Returns (dst', true) on success;
// (nil, false) when v isn't a recognised raw shape OR any element
// contains an embedded '\n' (which would shred the line-delimited
// invariant, so falling back to the stdjson path is the safe choice).
// Used by both Marshal (dst=nil) and Append (dst=caller buffer).
func marshalRawSliceTo(dst []byte, v any) (encoded []byte, ok bool) {
	//: dispatch on concrete pre-encoded shapes only — anything else
	//: falls through so the caller runs the reflect-based slow path.
	switch rows := v.(type) {
	//: []json.RawMessage is the promotion-path canonical shape.
	case []stdjson.RawMessage:
		//: hand the bytes off via the shared writer.
		return appendNDJSONRaw(dst, rawMessageView(rows))
	//: [][]byte covers proxy callers handing pre-encoded JSON bytes.
	case [][]byte:
		//: hand the bytes off via the shared writer.
		return appendNDJSONRaw(dst, rows)
	}
	//: no recognised raw shape — caller falls back.
	return nil, false
}

// rawMessageView re-types a []json.RawMessage as [][]byte without
// copying. RawMessage is defined as []byte upstream so the conversion
// is free at runtime — keeps appendNDJSONRaw single-shape.
func rawMessageView(rows []stdjson.RawMessage) [][]byte {
	//: unsafe-free re-slice: each RawMessage is already a []byte.
	view := make([][]byte, len(rows))
	//: alias the backing storage per element.
	for i, r := range rows {
		//: each RawMessage IS a []byte under the hood.
		view[i] = r
	}
	//: caller iterates the slice header.
	return view
}

// appendNDJSONRaw appends rows + '\n' separators onto dst. Refuses any
// row carrying an embedded '\n' (returns ok=false) so the slow path
// can re-encode it via json.Marshal (which always emits compact JSON
// with escaped newlines).
func appendNDJSONRaw(dst []byte, rows [][]byte) (appended []byte, ok bool) {
	//: pre-grow dst by exactly the total bytes we'll write.
	total := len(rows)
	//: sum payload bytes per row (separator count already in total).
	for _, r := range rows {
		//: cumulative payload size.
		total += len(r)
	}
	//: capacity reservation kills the geometric grow cascade.
	dst = slices.Grow(dst, total)
	//: per-row write with embedded-newline validation.
	for _, r := range rows {
		//: bytes.IndexByte is SIMD on amd64/arm64 — cheap rejection.
		if bytes.IndexByte(r, '\n') >= 0 {
			//: caller falls back; we don't touch dst on failure.
			return nil, false
		}
		//: append the payload + the line separator.
		dst = append(dst, r...)
		dst = append(dst, '\n')
	}
	//: hand back the populated buffer.
	return dst, true
}

// Unmarshal parses NDJSON data into v, which must be a pointer to a slice.
func (*ndjsonCodec) Unmarshal(data []byte, v any) error {
	//: target must be a pointer to a slice.
	slicePtr, ok := asSlicePointer(v)
	//: shape-the-target rejection.
	if !ok {
		//: loud failure — caller passed a non-slice pointer.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeNDJSONValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "NDJSON codec requires a slice pointer target",
			Private: "service/codec/ndjson.Unmarshal: target is not a pointer to a slice",
		})
	}
	//: decode into a fresh slice we can then publish through the pointer.
	result, derr := decodeLines(data, slicePtr.Type().Elem())
	//: surface any decode / scan failure.
	if derr != nil {
		//: cause is already wrapped by decodeLines.
		return derr
	}
	//: publish the accumulated slice through the caller's pointer.
	slicePtr.Elem().Set(result)
	//: nothing to wrap.
	return nil
}

// decodeLines walks data line-by-line and decodes each non-empty line
// into a freshly-allocated element of sliceType. Avoids bufio.Scanner
// (which would copy each line into its internal buffer) by indexing on
// '\n' directly into the input slice — every produced sub-slice aliases
// data so no per-line copy occurs before stdjson.Unmarshal reads it.
// Pre-counts records so the output slice is sized once instead of
// re-grown via reflect.Append on every line.
func decodeLines(data []byte, sliceType reflect.Type) (result reflect.Value, err error) {
	//: pre-count records so the output slice is sized exactly once
	//: instead of paying reflect.Append's geometric re-grow per line.
	recordCount := countNDJSONRecords(data)
	//: build the result slice with the counted capacity.
	elemType := sliceType.Elem()
	out := reflect.MakeSlice(sliceType, 0, recordCount)
	//: walk every '\n'-terminated chunk of data without copying.
	cursor := 0
	//: loop drains the input cursor-by-cursor; bytes.IndexByte is SIMD-fast.
	for cursor < len(data) {
		//: locate the next newline; -1 means this is the trailing line.
		nlOffset := bytes.IndexByte(data[cursor:], '\n')
		//: derive the line and advance the cursor in one branch.
		var line []byte
		//: branch on terminator presence — trailing-line case has no '\n'.
		if nlOffset < 0 {
			//: no trailing newline — the remainder is the final record.
			line = data[cursor:]
			cursor = len(data)
		} else {
			//: include only the bytes before '\n'; cursor advances past it.
			line = data[cursor : cursor+nlOffset]
			cursor += nlOffset + 1
		}
		//: enforce the per-record cap; bigger records likely mean corruption.
		if len(line) > scannerMaxCapacity {
			//: surface the documented sentinel for oversized records.
			return reflect.Value{}, errs.Wrap(nil, errs.WrapParams{
				Code:    CodeNDJSONUnmarshalFailed,
				Reason:  "UNMARSHAL_FAILED",
				Public:  "NDJSON record exceeds size limit",
				Private: "service/codec/ndjson.Unmarshal: line length exceeds scannerMaxCapacity",
			}, errs.Int("len", len(line)), errs.Int("cap", scannerMaxCapacity))
		}
		//: trim only after the size check so a giant whitespace line still trips it.
		line = bytes.TrimSpace(line)
		//: skip empty / whitespace-only lines (de-facto NDJSON dialect).
		if len(line) == 0 {
			//: nothing to decode on this iteration.
			continue
		}
		//: allocate a destination element and decode into it.
		elem := reflect.New(elemType)
		//: delegate per-record JSON decoding to the stdlib.
		if uerr := stdjson.Unmarshal(line, elem.Interface()); uerr != nil {
			//: wrap the stdlib error for reason-based matching.
			return reflect.Value{}, errs.Wrap(uerr, errs.WrapParams{
				Code:    CodeNDJSONUnmarshalFailed,
				Reason:  "UNMARSHAL_FAILED",
				Public:  "NDJSON decoding failed",
				Private: "service/codec/ndjson.Unmarshal: encoding/json returned an error",
			})
		}
		//: append the decoded element to the result.
		out = reflect.Append(out, elem.Elem())
	}
	//: hand back the accumulated slice.
	return out, nil
}

// countNDJSONRecords estimates the number of decodable records in data
// by counting '\n' bytes plus one for a possible trailing line that has
// no terminator. Conservative upper bound — actual decode skips empty /
// whitespace-only lines, but over-allocating once is much cheaper than
// re-growing the result slice on every record via reflect.Append.
func countNDJSONRecords(data []byte) int {
	//: empty input means zero records.
	if len(data) == 0 {
		//: nothing to allocate for.
		return 0
	}
	//: bytes.Count uses SIMD on amd64/arm64 so this is effectively free.
	count := bytes.Count(data, []byte{'\n'})
	//: trailing record without a terminating '\n' adds one slot.
	if data[len(data)-1] != '\n' {
		//: include the orphan tail in the capacity hint.
		count++
	}
	//: caller uses this as the slice cap hint.
	return count
}

// Append encodes the slice v as NDJSON and appends the bytes to dst.
// Implements the optional codec.Appender interface so hot-path callers
// can stream records into a recycled buffer (one '\n'-terminated line per
// element). Shares the same pre-encoded fast-path as Marshal.
func (*ndjsonCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: pre-encoded fast-path — see Marshal for the same dispatch.
	if out, ok := marshalRawSliceTo(dst, v); ok {
		//: bytes appended in place; caller owns the buffer.
		return out, nil
	}
	//: resolve v to a reflect.Value that is a slice or array.
	slice, ok := asSlice(v)
	//: shape-the-input rejection — same sentinel as Marshal for parity.
	if !ok {
		//: leave dst untouched and surface the documented sentinel.
		return dst, errs.Wrap(nil, errs.WrapParams{
			Code:    CodeNDJSONValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "NDJSON codec requires a slice value",
			Private: "service/codec/ndjson.Append: argument is not a slice",
		})
	}
	//: snapshot dst's prior length so a mid-slice failure can roll back and
	//: honour the Appender contract's "return prior contents on error" rule.
	origLen := len(dst)
	//: encode record-by-record; one '\n' per element appended in place.
	for i := range slice.Len() {
		//: delegate per-record JSON encoding to the stdlib.
		line, merr := stdjson.Marshal(slice.Index(i).Interface())
		//: surface any per-record failure with dst restored to its prior length.
		if merr != nil {
			//: wrap the stdlib error for reason-based matching; slice off any
			//: partially-written records so callers never see a torn buffer.
			return dst[:origLen], errs.Wrap(merr, errs.WrapParams{
				Code:    CodeNDJSONMarshalFailed,
				Reason:  "MARSHAL_FAILED",
				Public:  "NDJSON encoding failed",
				Private: "service/codec/ndjson.Append: encoding/json returned an error",
			})
		}
		//: append the record and the mandatory newline.
		dst = append(dst, line...)
		dst = append(dst, '\n')
	}
	//: hand back the (possibly re-allocated) buffer.
	return dst, nil
}

// asSlice reports whether v resolves to a slice or array reflect.Value.
func asSlice(v any) (slice reflect.Value, ok bool) {
	//: obtain the reflect.Value; pointers get dereferenced once.
	rv := reflect.ValueOf(v)
	//: drill through a single pointer indirection, rejecting nil pointers.
	if rv.Kind() == reflect.Pointer {
		//: nil pointer cannot carry a slice — reject to avoid a panic.
		if rv.IsNil() {
			//: caller must pass a non-nil pointer or the value directly.
			return reflect.Value{}, false
		}
		//: dereference once.
		rv = rv.Elem()
	}
	//: accept both slices and fixed-length arrays.
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		//: caller gets the drilled-down value.
		return rv, true
	}
	//: anything else is a programming error for this codec.
	return reflect.Value{}, false
}

// asSlicePointer reports whether v is a non-nil *[]T.
func asSlicePointer(v any) (ptr reflect.Value, ok bool) {
	//: obtain the reflect.Value without dereferencing.
	rv := reflect.ValueOf(v)
	//: reject anything that is not a non-nil pointer to a slice in one guard.
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Slice {
		//: single-shot rejection — target must be addressable and settable.
		return reflect.Value{}, false
	}
	//: caller gets the pointer to drive Set on the pointee.
	return rv, true
}
