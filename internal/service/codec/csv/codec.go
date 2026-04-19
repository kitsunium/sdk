// Package csv wraps encoding/csv as a codec.Codec implementation.
// The CSV format serialises a matrix of strings; the codec accepts
// [][]string for Marshal and writes into *[][]string on Unmarshal.
package csv

import (
	"bytes"
	stdcsv "encoding/csv"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Codec is the CSV singleton, registered with core/codec at package load.
// Binding the registration result to a named var is more idiomatic than
// `var _ = codec.Register(...)` and keeps us clear of init() (KTN-FUNC-NOINIT).
var Codec codec.Codec = codec.Register(&csvCodec{})

// csvCodec is the concrete Codec implementation for CSV.
type csvCodec struct{}

// New returns a CSV codec instance.
//
// Returns:
//   - codec.Codec: a fresh stateless codec.
func New() (c codec.Codec) {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
//
// Returns:
//   - string: always "csv".
func (*csvCodec) Name() (name string) {
	//: canonical identifier.
	return "csv"
}

// MIMETypes lists every MIME alias.
//
// Returns:
//   - []string: canonical MIME first.
func (*csvCodec) MIMETypes() (mimes []string) {
	//: RFC 4180 registers text/csv.
	return []string{"text/csv"}
}

// Extensions lists every file extension.
//
// Returns:
//   - []string: canonical extension first.
func (*csvCodec) Extensions() (exts []string) {
	//: canonical extension.
	return []string{".csv"}
}

// Marshal serialises a [][]string into RFC 4180 CSV bytes.
//
// Params:
//   - v: must be a [][]string (or pointer to one) — CSV serialises a matrix.
//
// Returns:
//   - []byte: encoded CSV.
//   - error: ValueInvalid if v is not a [][]string; MarshalFailed on writer failure.
func (*csvCodec) Marshal(v any) (data []byte, err error) {
	//: accept both direct and pointer form to keep call sites flexible.
	records, ok := extractRecords(v)
	//: type-gate failure surfaces the dedicated sentinel.
	if !ok {
		//: caller passed something that is not [][]string — loud failure.
		return nil, errs.Wrap(nil, errs.WrapParams{
			Code:    CodeCSVValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "CSV codec requires a [][]string value",
			Private: "service/codec/csv.Marshal: argument is not [][]string",
		})
	}
	//: write into a buffer so the caller gets []byte (not a writer).
	var buf bytes.Buffer
	w := stdcsv.NewWriter(&buf)
	//: WriteAll flushes on return; any error is surfaced via Error().
	if werr := w.WriteAll(records); werr != nil {
		//: wrap the stdlib error.
		return nil, errs.Wrap(werr, errs.WrapParams{
			Code:    CodeCSVMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "CSV encoding failed",
			Private: "service/codec/csv.Marshal: WriteAll returned an error",
		})
	}
	//: hand back the buffered bytes.
	return buf.Bytes(), nil
}

// Unmarshal parses data as CSV into *[][]string.
//
// Params:
//   - data: CSV bytes.
//   - v: pointer destination — must be *[][]string.
//
// Returns:
//   - error: ValueInvalid if target is not *[][]string; UnmarshalFailed on reader failure.
func (*csvCodec) Unmarshal(data []byte, v any) (err error) {
	//: target must be *[][]string so we can populate it.
	dst, ok := v.(*[][]string)
	//: type-gate failure surfaces the dedicated sentinel.
	if !ok {
		//: caller passed something that is not *[][]string — loud failure.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeCSVValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "CSV codec requires a [][]string value",
			Private: "service/codec/csv.Unmarshal: target is not *[][]string",
		})
	}
	//: ReadAll consumes every record from the reader.
	r := stdcsv.NewReader(bytes.NewReader(data))
	recs, rerr := r.ReadAll()
	//: success fast-path.
	if rerr == nil {
		//: publish the decoded records through the caller's pointer.
		*dst = recs
		//: nothing to wrap.
		return nil
	}
	//: wrap the stdlib error.
	return errs.Wrap(rerr, errs.WrapParams{
		Code:    CodeCSVUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "CSV decoding failed",
		Private: "service/codec/csv.Unmarshal: ReadAll returned an error",
	})
}

// extractRecords coerces v into a [][]string, accepting both direct and
// pointer forms.
//
// Params:
//   - v: candidate value.
//
// Returns:
//   - [][]string: the extracted matrix when available.
//   - bool: true iff v is a [][]string or *[][]string.
func extractRecords(v any) (records [][]string, ok bool) {
	//: direct form is the common case.
	if direct, directOk := v.([][]string); directOk {
		//: hand back the direct slice.
		return direct, true
	}
	//: pointer form also accepted for symmetry with Unmarshal.
	if ptr, ptrOk := v.(*[][]string); ptrOk && ptr != nil {
		//: dereference once.
		return *ptr, true
	}
	//: anything else is a programming error for this codec.
	return nil, false
}
