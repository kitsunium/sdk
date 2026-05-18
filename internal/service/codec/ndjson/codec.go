// Package ndjson implements newline-delimited JSON as a codec.Codec.
// Each record is a JSON value followed by a single '\n' — the format is
// intentionally line-oriented so that partial reads remain parseable.
// The codec accepts slice values: Marshal emits one record per element,
// Unmarshal splits on '\n' and decodes each non-empty line into a new
// slice element.
package ndjson

import (
	"bufio"
	"bytes"
	stdjson "encoding/json"
	"reflect"
	"slices"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// scannerInitialCapacity is the starting buffer size for line scanning.
const scannerInitialCapacity int = 64 * 1024

// scannerMaxCapacity caps a single NDJSON record at 10 MiB; beyond that the
// record likely represents a consumer bug or a corrupt stream.
const scannerMaxCapacity int = 10 * 1024 * 1024

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables (hoisted to satisfy KTN-VAR-CONSTSLICE).
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&ndjsonCodec{})

	//: MIME table hoisted to satisfy KTN-VAR-CONSTSLICE.
	mimeTypes = []string{"application/x-ndjson", "application/jsonl"}

	//: extension table hoisted for the same reason.
	extensions = []string{".ndjson", ".jsonl"}
)

// ndjsonCodec is the concrete Codec implementation for NDJSON.
type ndjsonCodec struct{}

// New returns an NDJSON codec instance.
//
// Returns:
//   - codec.Codec: a fresh stateless codec.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
//
// Returns:
//   - string: always "ndjson".
func (*ndjsonCodec) Name() string {
	//: canonical identifier.
	return "ndjson"
}

// MIMETypes lists every MIME alias.
//
// Returns:
//   - []string: canonical MIME first.
func (*ndjsonCodec) MIMETypes() []string {
	//: hand back the package-level slice.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
//
// Returns:
//   - []string: canonical extension first.
func (*ndjsonCodec) Extensions() []string {
	//: hand back the package-level slice.
	return slices.Clone(extensions)
}

// Marshal encodes a slice v as NDJSON bytes.
//
// Params:
//   - v: slice or pointer-to-slice value; each element becomes one record.
//
// Returns:
//   - []byte: the NDJSON-encoded bytes (trailing newline included).
//   - error: ValueInvalid when v is not a slice; MarshalFailed on encode failure.
func (*ndjsonCodec) Marshal(v any) (encoded []byte, err error) {
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

// Unmarshal parses NDJSON data into v, which must be a pointer to a slice.
//
// Params:
//   - data: NDJSON bytes.
//   - v: pointer to a slice — each non-empty line decodes into a new element.
//
// Returns:
//   - error: ValueInvalid when v is not a *slice; UnmarshalFailed on decode failure.
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

// decodeLines reads data line-by-line and decodes each non-empty line into
// a freshly-allocated element of sliceType.
//
// Params:
//   - data: raw NDJSON bytes.
//   - sliceType: reflect.Type of the destination slice (e.g. []Event).
//
// Returns:
//   - reflect.Value: a slice of sliceType containing the decoded elements.
//   - error: UnmarshalFailed wrapping the stdlib cause on a per-record failure.
func decodeLines(data []byte, sliceType reflect.Type) (result reflect.Value, err error) {
	//: split on '\n'; bufio.Scanner ignores the trailing empty line for us.
	scanner := bufio.NewScanner(bytes.NewReader(data))
	//: allow large records; default 64 KiB would reject legitimate payloads.
	scanner.Buffer(make([]byte, 0, scannerInitialCapacity), scannerMaxCapacity)
	//: build a fresh slice so we can accumulate decoded elements.
	elemType := sliceType.Elem()
	out := reflect.MakeSlice(sliceType, 0, 0)
	//: iterate over lines; skip empty ones to match the de facto dialect.
	for scanner.Scan() {
		//: drop whitespace-only lines (common when writers flush eagerly).
		line := bytes.TrimSpace(scanner.Bytes())
		//: skip empty lines.
		if len(line) == 0 {
			//: nothing to decode here.
			continue
		}
		//: allocate a destination element and decode into it.
		elem := reflect.New(elemType)
		//: delegate per-record JSON decoding to the stdlib.
		if uerr := stdjson.Unmarshal(line, elem.Interface()); uerr != nil {
			//: wrap the stdlib error.
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
	//: scanner.Err reports only I/O failures — bytes.Reader never errors.
	if serr := scanner.Err(); serr != nil {
		//: wrap defensively even though this is effectively unreachable.
		return reflect.Value{}, errs.Wrap(serr, errs.WrapParams{
			Code:    CodeNDJSONUnmarshalFailed,
			Reason:  "UNMARSHAL_FAILED",
			Public:  "NDJSON decoding failed",
			Private: "service/codec/ndjson.Unmarshal: bufio.Scanner returned an error",
		})
	}
	//: hand back the accumulated slice.
	return out, nil
}

// Append encodes the slice v as NDJSON and appends the bytes to dst.
// Implements the optional codec.Appender interface so hot-path callers
// can stream records into a recycled buffer (one '\n'-terminated line per
// element).
//
// Params:
//   - dst: caller-supplied buffer; encoded bytes are appended onto it.
//   - v: slice value; each element becomes one NDJSON record.
//
// Returns:
//   - []byte: the (possibly re-allocated) buffer with encoded NDJSON.
//   - error: ValueInvalid when v is not a slice; MarshalFailed otherwise.
func (*ndjsonCodec) Append(dst []byte, v any) (appended []byte, err error) {
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
//
// Params:
//   - v: candidate value.
//
// Returns:
//   - reflect.Value: the underlying slice/array value when ok.
//   - bool: true iff v is (a pointer to) a slice or array.
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
//
// Params:
//   - v: candidate value.
//
// Returns:
//   - reflect.Value: the pointer's reflect.Value when ok.
//   - bool: true iff v is a non-nil pointer to a slice.
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
