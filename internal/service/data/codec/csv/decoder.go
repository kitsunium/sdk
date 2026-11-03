package csv

import (
	stdcsv "encoding/csv"
	"errors"
	"io"
	"reflect"
	"slices"

	"github.com/kitsunium/sdk/internal/core/data/codec"
)

// columnMap maps each column of a file to a column of the schema, or -1 for
// a column the struct does not map — ignored.
type columnMap []int

// decoderFlags is the decoder's state, one bit each.
type decoderFlags uint8

// The decoder's state bits.
const (
	// flagReady reports that record holds a record no Decode has taken yet.
	flagReady decoderFlags = 1 << iota
	// flagHeaderDone is set once the stream's header question is settled:
	// read as a header by a Decode into a struct, or handed out as data.
	flagHeaderDone
	// flagReported is set once Decode has returned err, so More reports false.
	flagReported
)

// csvDecoder reads one record per Decode (#902), from encoding/csv's
// incremental Reader: into a *[]string, the record as it is; into a pointer to
// a struct, the record converted through the header — the first record of
// the stream, read when the first Decode into a struct finds it waiting.
type csvDecoder struct {
	// r is encoding/csv's reader, reusing its record slice.
	r *stdcsv.Reader
	// noHeader is the codec's setting: a struct's columns are its fields in
	// order, and no record is a header.
	noHeader bool
	// record is the record More or Decode read ahead.
	record []string
	// recordNo counts the records read, the header included.
	recordNo int
	// flags are the ready, header-done and reported bits.
	flags decoderFlags
	// header is the header record, kept for the columns of each struct type.
	header []string
	// cols maps the header for colsType, the struct type decoded last.
	cols columnMap
	// colsType is the struct type cols was built for.
	colsType reflect.Type
	// err ends the stream: io.EOF or malformed CSV.
	err error
}

// NewDecoder wraps r in a streaming codec.Decoder handing one record per
// Decode, into a *[]string or a pointer to a struct.
func (c *csvCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: encoding/csv's reader, the codec's delimiter set.
	f := c.format()
	cr := stdcsv.NewReader(r)
	cr.Comma, cr.ReuseRecord = f.comma, true
	//: nothing read yet.
	return &csvDecoder{r: cr, noHeader: f.noHeader}
}

// Decode stores the next record in v: a *[]string receives it as it is — the
// header too, when the stream starts with one; a pointer to a struct receives
// it converted through the header, which the first such Decode reads first
// unless the codec has NoHeader. It returns io.EOF, unwrapped, once drained;
// UNMARSHAL_FAILED for malformed CSV, which ends the stream; HEADER_MISMATCH
// and CELL_INVALID as Unmarshal does; VALUE_INVALID for another target.
func (d *csvDecoder) Decode(v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: the untyped target.
	if dst, ok := v.(*[]string); ok && dst != nil {
		//: the next record, any record.
		if !d.take() {
			return d.err
		}
		//: the record is data: no header comes after it.
		d.flags |= flagHeaderDone
		//: the caller's copy; the reader reuses its slice.
		*dst = slices.Clone(d.record)
		return nil
	}
	//: a pointer to a struct.
	row, schema, err := structTarget(v)
	//: anything else.
	if err != nil {
		return err
	}
	//: the columns for this struct type, the header read first if it waits.
	cols, err := d.columnsFor(row.Type(), schema)
	//: no header, or one that does not fit.
	if err != nil {
		return err
	}
	//: the next data record.
	if !d.take() {
		return d.err
	}
	//: converted through the columns.
	return fillRow(row, d.record, cols, schema, rowAt{recordNo: d.recordNo, reader: d.r})
}

// More reports whether Decode has something to return: a record, or a failure
// the caller has not been told of yet. It reads ahead one record — a header
// still unread counts, so a stream holding only a header reports true and its
// first Decode into a struct returns io.EOF.
func (d *csvDecoder) More() bool {
	//: read ahead once.
	d.advance()
	//: a record, or a failure Decode has yet to report.
	return d.flags&flagReady != 0 || (d.flags&flagReported == 0 && !errors.Is(d.err, io.EOF))
}

// take hands the waiting record to the caller, reading it first if needed;
// false when the stream has ended, Decode then returning d.err.
func (d *csvDecoder) take() bool {
	//: read ahead unless More already did.
	d.advance()
	//: no record: the end, or the failure, from now on.
	if d.flags&flagReady == 0 {
		d.flags |= flagReported
		return false
	}
	//: taken.
	d.flags &^= flagReady
	return true
}

// advance reads the next record unless one is waiting or the stream ended.
func (d *csvDecoder) advance() {
	//: a record waits, or the stream is over.
	if d.flags&flagReady != 0 || d.err != nil {
		return
	}
	//: the next record.
	record, err := d.r.Read()
	//: the end of the stream.
	if errors.Is(err, io.EOF) {
		d.err = io.EOF
		return
	}
	//: malformed CSV ends the stream.
	if err != nil {
		d.err = unmarshalFailed(err)
		return
	}
	//: waiting for Decode.
	d.record = record
	d.flags |= flagReady
	d.recordNo++
}

// columnsFor returns the column map for struct type t: built from the header
// — read now if it is still waiting — or positional with NoHeader, and kept
// for the next Decode into the same type.
func (d *csvDecoder) columnsFor(t reflect.Type, schema *tableSchema) (columnMap, error) {
	//: the last type's map.
	if t == d.colsType && d.cols != nil {
		return d.cols, nil
	}
	//: the stream's header, read once, before the first data record.
	if d.flags&flagHeaderDone == 0 && !d.noHeader {
		//: no record at all: nothing to decode.
		if !d.take() {
			return nil, d.err
		}
		//: kept for every struct type decoded later.
		d.header = slices.Clone(d.record)
		d.flags |= flagHeaderDone
	}
	//: the columns: by header name, or the fields in order.
	header := d.header
	//: without a header, the width is the schema's.
	if d.noHeader {
		header = schema.names
	}
	//: mapped and checked as Unmarshal maps them.
	cols, err := mapColumns(header, schema, d.noHeader || d.header == nil)
	//: refused.
	if err != nil {
		return nil, err
	}
	//: kept for the next Decode.
	d.cols, d.colsType = cols, t
	return cols, nil
}

// structTarget resolves v, a non-nil pointer to a struct, to the struct and
// its columns.
func structTarget(v any) (reflect.Value, *tableSchema, error) {
	//: a non-nil pointer to a struct.
	rv := reflect.ValueOf(v)
	//: anything else is not a stream target.
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		//: the dedicated sentinel.
		return reflect.Value{}, nil, valueInvalid("service/data/codec/csv.Decoder.Decode: target is neither *[]string nor a pointer to a struct")
	}
	//: the struct's columns.
	schema, err := schemaOf(rv.Elem().Type())
	//: the struct and its columns, or the refusal.
	return rv.Elem(), schema, err
}
