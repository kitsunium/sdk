package csv

import (
	"io"
	"reflect"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corecsv "github.com/kitsunium/sdk/internal/core/data/codec/csv"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// csvEncoder writes one record per Encode (#902): a []string as it is, a
// struct as a row of its columns, the header first — before the first struct
// written, unless the codec has NoHeader.
type csvEncoder struct {
	// w is the stream; the encoder does not own it.
	w io.Writer
	// c is the codec whose delimiter, mitigation and header setting apply.
	c *csvCodec
	// buf is the record being written, reused across Encode calls.
	buf []byte
	// headerDone is set once the header is written, or the first record was
	// data: a header after data would be a row.
	headerDone bool
}

// NewEncoder wraps w in a streaming codec.Encoder writing one record per
// Encode.
func (c *csvCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: the buffer grows to the longest record written.
	return &csvEncoder{w: w, c: c}
}

// Encode writes v as one record: a []string as encoding/csv's Writer writes
// it, a struct or a non-nil pointer to one as a row of its columns — the
// header written before the first struct unless NoHeader. Anything else is
// VALUE_INVALID and writes nothing; a failing writer is MARSHAL_FAILED.
func (e *csvEncoder) Encode(v any) error {
	//: the record, built in the reused buffer.
	var err error
	e.buf, err = e.appendValue(e.buf[:0], v)
	//: refused before a byte is written.
	if err != nil {
		return err
	}
	//: one write per record.
	if _, werr := e.w.Write(e.buf); werr != nil {
		//: the writer failed.
		return errs.Wrap(werr, errs.WrapParams{
			Code:    corecsv.CodeCSVMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "CSV encoding failed",
			Private: "service/data/codec/csv.Encoder.Encode: the writer failed",
		})
	}
	//: one record written.
	return nil
}

// appendValue appends v's record to dst — preceded by the header when v is
// the first struct written.
func (e *csvEncoder) appendValue(dst []byte, v any) ([]byte, error) {
	//: the codec's delimiter, mitigation and header setting.
	f := e.c.format()
	//: an untyped record: data, so no header comes after it.
	if record, ok := v.([]string); ok {
		e.headerDone = true
		return appendRecord(dst, record, f.comma, f.escape), nil
	}
	//: a struct, or a non-nil pointer to one.
	rv := reflect.ValueOf(v)
	//: through the pointer.
	if rv.Kind() == reflect.Pointer && !rv.IsNil() {
		rv = rv.Elem()
	}
	//: anything else.
	if rv.Kind() != reflect.Struct {
		return dst, valueInvalid("service/data/codec/csv.Encoder.Encode: argument is neither []string nor a struct")
	}
	//: the struct's columns.
	schema, err := schemaOf(rv.Type())
	if err != nil {
		return dst, err
	}
	//: the header before the first row.
	if !e.headerDone && !f.noHeader {
		dst = appendRecord(dst, schema.names, f.comma, f.escape)
	}
	//: the row; a refused cell leaves the header unwritten too.
	out, err := appendRow(dst, rv, false, schema, f)
	if err != nil {
		return dst[:0], err
	}
	//: the header is behind us.
	e.headerDone = true
	return out, nil
}

// Close is a no-op: the encoder buffers nothing between calls and does not
// own the writer.
func (*csvEncoder) Close() error {
	//: every Encode wrote its record already.
	return nil
}
