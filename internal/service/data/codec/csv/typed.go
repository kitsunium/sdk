package csv

import (
	"bytes"
	"encoding"
	stdcsv "encoding/csv"
	"errors"
	"io"
	"reflect"
	"strconv"
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corecsv "github.com/kitsunium/sdk/internal/core/data/codec/csv"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The typed mapping's fixed strings.
const (
	// utf8BOM is the byte-order mark a spreadsheet may put before the header;
	// it is not part of the first column's name.
	utf8BOM = "\uFEFF"
	// rowEstimate is the bytes Marshal reserves per row of a typed table before
	// it knows the cells.
	rowEstimate int = 64
	// maxCellScratch is the room reserved for one cell's formatting.
	maxCellScratch int = 64
)

// typedTable is a typed value resolved for writing: its rows and their
// struct's columns.
type typedTable struct {
	// rows is the slice or array of structs, or of pointers to them.
	rows reflect.Value
	// ptr is whether each row is a pointer to the struct.
	ptr bool
	// schema is the struct's columns.
	schema *tableSchema
}

// tableOf resolves v — a slice or array of structs or of pointers to them,
// directly or through a non-nil pointer — to the table Marshal writes. Any
// other value is VALUE_INVALID.
func tableOf(v any) (typedTable, error) {
	//: one pointer is looked through.
	rv := reflect.ValueOf(v)
	//: a non-nil pointer to the rows.
	if rv.Kind() == reflect.Pointer && !rv.IsNil() {
		rv = rv.Elem()
	}
	//: rows, not a single value.
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		//: not a table.
		return typedTable{}, valueInvalid("service/data/codec/csv: argument is neither [][]string nor a slice of structs")
	}
	//: the struct each row is, through one pointer.
	elem, ptr := rv.Type().Elem(), false
	//: pointer rows.
	if elem.Kind() == reflect.Pointer {
		elem, ptr = elem.Elem(), true
	}
	//: struct rows only.
	if elem.Kind() != reflect.Struct {
		//: not a table.
		return typedTable{}, valueInvalid("service/data/codec/csv: argument is neither [][]string nor a slice of structs")
	}
	//: the struct's columns.
	schema, err := schemaOf(elem)
	//: refused.
	if err != nil {
		return typedTable{}, err
	}
	//: the table.
	return typedTable{rows: rv, ptr: ptr, schema: schema}, nil
}

// appendTable appends t as CSV: the header from the column names unless
// noHeader, then one record per row, each cell written as appendField writes
// a cell — quoted when it must be, the formula mitigation applied when escape.
func appendTable(dst []byte, t typedTable, f format) ([]byte, error) {
	//: the header row.
	if !f.noHeader {
		dst = appendRecord(dst, t.schema.names, f.comma, f.escape)
	}
	//: one record per row.
	for i := range t.rows.Len() {
		//: the row's record.
		var err error
		dst, err = appendRow(dst, t.rows.Index(i), t.ptr, t.schema, f)
		//: a cell refused.
		if err != nil {
			return dst, err
		}
	}
	//: the table.
	return dst, nil
}

// appendRow appends one row's record. A nil pointer row writes empty cells.
func appendRow(dst []byte, row reflect.Value, ptr bool, schema *tableSchema, f format) ([]byte, error) {
	//: a nil row: every cell empty.
	if ptr && row.IsNil() {
		//: the delimiters and the terminator only.
		for range len(schema.columns) - 1 {
			dst = appendComma(dst, f.comma)
		}
		//: the record's end.
		return append(dst, '\n'), nil
	}
	//: the struct itself.
	if ptr {
		row = row.Elem()
	}
	//: the cell scratch, reused across the row's cells.
	var scratch [maxCellScratch]byte
	//: each column in order.
	for i := range schema.columns {
		//: the delimiter between cells.
		if i > 0 {
			dst = appendComma(dst, f.comma)
		}
		//: the cell as text.
		cell, err := formatCell(scratch[:0], row.FieldByIndex(schema.columns[i].index), &schema.columns[i])
		//: a field that does not write itself.
		if err != nil {
			return dst, err
		}
		//: the cell, quoted and prefixed as it needs.
		dst = appendCell(dst, cell, f)
	}
	//: the record's end.
	return append(dst, '\n'), nil
}

// appendCell appends a formatted cell as appendField writes it. The string
// does not escape, so a short cell is converted on the stack.
func appendCell(dst, cell []byte, f format) []byte {
	//: one conversion, read twice.
	text := string(cell)
	//: quoted and prefixed as it needs.
	return appendField(dst, text, specFor(text, f.comma, f.escape))
}

// formatCell appends the text of field fv, of column col, to buf.
func formatCell(buf []byte, fv reflect.Value, col *columnSpec) ([]byte, error) {
	//: a pointer field: nil is the empty cell.
	if col.ptr {
		//: the empty cell.
		if fv.IsNil() {
			return buf, nil
		}
		fv = fv.Elem()
	}
	//: by the column's conversion.
	switch col.kind {
	//: the string itself.
	case kindString:
		return append(buf, fv.String()...), nil
	//: base 10.
	case kindInt:
		return strconv.AppendInt(buf, fv.Int(), 10), nil
	//: base 10.
	case kindUint:
		return strconv.AppendUint(buf, fv.Uint(), 10), nil
	//: the shortest text that reads back as the same float.
	case kindFloat:
		return strconv.AppendFloat(buf, fv.Float(), 'g', -1, fv.Type().Bits()), nil
	//: true / false.
	case kindBool:
		return strconv.AppendBool(buf, fv.Bool()), nil
	//: the type's own text.
	default:
		return appendText(buf, fv)
	}
}

// appendText appends what fv's MarshalText writes — on the value, or on its
// address. A type that only reads itself as text cannot be written:
// MARSHAL_FAILED.
func appendText(buf []byte, fv reflect.Value) ([]byte, error) {
	//: the marshaler on the value, else on its address.
	m, ok := reflect.TypeAssert[encoding.TextMarshaler](fv)
	//: a pointer receiver is reached through the address.
	if !ok && fv.CanAddr() {
		m, ok = reflect.TypeAssert[encoding.TextMarshaler](fv.Addr())
	}
	//: nothing writes it.
	if !ok {
		//: refused, naming no value.
		return buf, marshalFailed("a field's type reads itself as text but does not write itself")
	}
	//: the type's text.
	text, err := m.MarshalText()
	//: its own failure, kept as the cause.
	if err != nil {
		return buf, errs.Wrap(err, errs.WrapParams{
			Code:    corecsv.CodeCSVMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "CSV encoding failed",
			Private: "service/data/codec/csv: a field's MarshalText returned an error",
		})
	}
	//: the cell.
	return append(buf, text...), nil
}

// appendComma appends the delimiter, UTF-8 encoded.
func appendComma(dst []byte, comma rune) []byte {
	//: one rune, as encoding/csv's WriteRune writes it.
	return utf8.AppendRune(dst, comma)
}

// marshalFailed is MARSHAL_FAILED with a private explanation.
func marshalFailed(private string) error {
	//: the sentinel's code and texts.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    corecsv.CodeCSVMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "CSV encoding failed",
		Private: "service/data/codec/csv: " + private,
	})
}

// rowsTarget is a typed Unmarshal target: the slice the rows are appended
// to, and their struct's columns.
type rowsTarget struct {
	// slice is the settable []T or []*T.
	slice reflect.Value
	// ptr is whether each row is a pointer to the struct.
	ptr bool
	// elem is the struct type.
	elem reflect.Type
	// schema is the struct's columns.
	schema *tableSchema
}

// rowsTargetOf resolves v — a non-nil pointer to a slice of structs or of
// pointers to them — to the target Unmarshal fills. Anything else is
// VALUE_INVALID.
func rowsTargetOf(v any) (rowsTarget, error) {
	//: a non-nil pointer to a slice.
	rv := reflect.ValueOf(v)
	//: anything else is not a typed target.
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Slice {
		//: the dedicated sentinel.
		return rowsTarget{}, valueInvalid("service/data/codec/csv.Unmarshal: target is neither *[][]string nor a pointer to a slice of structs")
	}
	//: the row type, through one pointer.
	elem, ptr := rv.Elem().Type().Elem(), false
	//: pointer rows.
	if elem.Kind() == reflect.Pointer {
		elem, ptr = elem.Elem(), true
	}
	//: struct rows only.
	if elem.Kind() != reflect.Struct {
		//: the dedicated sentinel.
		return rowsTarget{}, valueInvalid("service/data/codec/csv.Unmarshal: target is neither *[][]string nor a pointer to a slice of structs")
	}
	//: the struct's columns.
	schema, err := schemaOf(elem)
	//: refused.
	if err != nil {
		return rowsTarget{}, err
	}
	//: the target.
	return rowsTarget{slice: rv.Elem(), ptr: ptr, elem: elem, schema: schema}, nil
}

// mapColumns reads the header: each cell names a column of the schema or is
// ignored; a column the schema maps twice, or a required one missing, is
// HEADER_MISMATCH. With noHeader the columns are the fields, in order, and
// header is the first record, read for its width only.
func mapColumns(header []string, schema *tableSchema, noHeader bool) (columnMap, error) {
	//: one entry per column of the file.
	cols := make(columnMap, len(header))
	//: which schema columns the file holds.
	seen := make([]bool, len(schema.columns))
	//: each column of the file.
	for i, name := range header {
		//: positional without a header.
		fi, ok := i, i < len(schema.columns)
		//: by name with one; a spreadsheet's BOM is not part of the first name.
		if !noHeader {
			fi, ok = schema.byName[trimBOM(name, i)]
		}
		//: a column the struct does not map.
		if !ok {
			cols[i] = -1
			continue
		}
		//: a column named twice.
		if seen[fi] {
			return nil, headerMismatch("a column appears twice in the header", schema.columns[fi].name)
		}
		cols[i], seen[fi] = fi, true
	}
	//: every required column present.
	for fi, col := range schema.columns {
		//: missing.
		if col.required && !seen[fi] {
			return nil, headerMismatch("a required column is missing", col.name)
		}
	}
	//: the mapping.
	return cols, nil
}

// trimBOM drops a UTF-8 byte-order mark from the first header cell.
func trimBOM(name string, i int) string {
	//: only the file's first bytes can carry one.
	if i == 0 && len(name) >= len(utf8BOM) && name[:len(utf8BOM)] == utf8BOM {
		return name[len(utf8BOM):]
	}
	//: as written.
	return name
}

// rowAt is where a row comes from: the record's number and the reader that
// read it last, which places its cells (#1125).
type rowAt struct {
	// recordNo counts from the file's first record, the header included.
	recordNo int
	// reader places a field of that record.
	reader fieldPosser
}

// fillRow sets the fields of row — an addressable struct — from record,
// through cols. A cell that does not convert is CELL_INVALID, placed at the
// cell's line and column as at's reader gives them (#1125), naming the record
// number and the column's name, never the cell.
func fillRow(row reflect.Value, record []string, cols columnMap, schema *tableSchema, at rowAt) error {
	//: each cell of the record.
	for i, cell := range record {
		//: a column the struct does not map.
		if i >= len(cols) || cols[i] < 0 {
			continue
		}
		//: the column and its field.
		col := &schema.columns[cols[i]]
		//: the cell into the field.
		if err := parseCell(cell, row.FieldByIndex(col.index), col); err != nil {
			//: which record and column, never the cell.
			line, column := at.reader.FieldPos(i)
			fields := codec.PositionFields(codec.PositionValue{Line: line, Column: column})
			return errs.Wrap(corecsv.CellInvalid, errs.WrapParams{}, append(fields, errs.Int("record", at.recordNo), errs.String("name", col.name))...)
		}
	}
	//: every mapped cell set.
	return nil
}

// parseCell sets fv, the field of column col, from cell. An empty cell sets a
// pointer field to nil; a number or a bool must parse.
func parseCell(cell string, fv reflect.Value, col *columnSpec) error {
	//: a pointer field: the empty cell is nil, anything else a new value.
	if col.ptr {
		//: nil.
		if cell == "" {
			fv.SetZero()
			return nil
		}
		//: a value to set, allocated once.
		if fv.IsNil() {
			fv.Set(reflect.New(col.typ))
		}
		fv = fv.Elem()
	}
	//: the value itself.
	return parseValue(cell, fv, col.kind)
}

// parseValue sets fv, a cell type's value, from cell as kind converts it.
func parseValue(cell string, fv reflect.Value, kind cellKind) error {
	//: by the column's conversion.
	switch kind {
	//: the cell itself.
	case kindString:
		fv.SetString(cell)
		return nil
	//: base 10, the field's width.
	case kindInt:
		n, err := strconv.ParseInt(cell, 10, fv.Type().Bits())
		fv.SetInt(n)
		return err
	//: base 10, the field's width.
	case kindUint:
		n, err := strconv.ParseUint(cell, 10, fv.Type().Bits())
		fv.SetUint(n)
		return err
	//: the field's width.
	case kindFloat:
		n, err := strconv.ParseFloat(cell, fv.Type().Bits())
		fv.SetFloat(n)
		return err
	//: strconv.ParseBool's spellings.
	case kindBool:
		b, err := strconv.ParseBool(cell)
		fv.SetBool(b)
		return err
	//: the type's own reading.
	default:
		return unmarshalText(cell, fv)
	}
}

// unmarshalText sets fv through its address's UnmarshalText.
func unmarshalText(cell string, fv reflect.Value) error {
	//: the unmarshaler on the field's address.
	u, ok := reflect.TypeAssert[encoding.TextUnmarshaler](fv.Addr())
	//: a type that only writes itself: no cell reads into it.
	if !ok {
		return schemaInvalid("a field's type writes itself as text but does not read itself")
	}
	//: the type's own reading.
	return u.UnmarshalText([]byte(cell))
}

// unmarshalTable decodes data into the typed target: the header (unless
// noHeader) maps the columns, each later record appends a row. A table that
// is exactly the codec package's promotion shape, read into a struct that
// maps no "_json" column, is VALUE_INVALID so the facade reads it as it
// wrote it.
func unmarshalTable(data []byte, t rowsTarget, f format) error {
	//: a reader over the bytes, the delimiter set.
	r := stdcsv.NewReader(bytes.NewReader(data))
	r.Comma, r.ReuseRecord = f.comma, true
	//: a settable slice with room for the records the input can hold, capped
	//: (#1098): rows are decoded in place into its free slots.
	out := reflect.New(t.slice.Type()).Elem()
	out.Grow(recordsHint(data))
	//: the header, or the first record for its width.
	first, err := r.Read()
	//: no record at all: an empty table.
	if errors.Is(err, io.EOF) {
		t.slice.Set(out)
		return nil
	}
	//: malformed CSV.
	if err != nil {
		return unmarshalFailed(err)
	}
	//: the columns, from the first record.
	cols, err := firstColumns(first, t, f)
	if err != nil {
		return err
	}
	//: without a header, the first record is a row.
	if f.noHeader {
		//: the row, decoded in place.
		if err = decodeRowInto(out, first, cols, t, rowAt{recordNo: 1, reader: r}); err != nil {
			return err
		}
	}
	//: the rows after it, published only once whole.
	if err = decodeRows(r, out, cols, t); err != nil {
		return err
	}
	t.slice.Set(out)
	return nil
}

// firstColumns maps the columns from a table's first record: its header, or
// with noHeader the fields in order. The facade's promotion container is
// VALUE_INVALID unless the struct maps its "_json" column.
func firstColumns(first []string, t rowsTarget, f format) (columnMap, error) {
	//: the facade's promotion container, for it to unwrap.
	if !f.noHeader && isPromotionHeader(first) && !hasColumn(t.schema, promotionHeaderCell) {
		return nil, valueInvalid("service/data/codec/csv.Unmarshal: the table is the codec package's promotion container")
	}
	//: the columns.
	return mapColumns(first, t.schema, f.noHeader)
}

// fieldPosser places a field of the record read last: encoding/csv's Reader.
type fieldPosser interface {
	// FieldPos returns the 1-based line and byte column where field starts.
	FieldPos(field int) (line, column int)
}

// reader is what decodeRows reads records from: encoding/csv's Reader.
type reader interface {
	fieldPosser
	// Read returns the next record, or io.EOF.
	Read() (record []string, err error)
}

// decodeRows decodes every record left in r into out, one row each.
func decodeRows(r reader, out reflect.Value, cols columnMap, t rowsTarget) error {
	//: record numbers count from the first record of the file.
	for recordNo := 2; ; recordNo++ {
		//: the next record.
		record, err := r.Read()
		//: the end of the table.
		if errors.Is(err, io.EOF) {
			return nil
		}
		//: malformed CSV.
		if err != nil {
			return unmarshalFailed(err)
		}
		//: the row, decoded in place.
		if err = decodeRowInto(out, record, cols, t, rowAt{recordNo: recordNo, reader: r}); err != nil {
			return err
		}
	}
}

// decodeRowInto grows out, a settable slice, by one zero row — its storage
// never held a row, so it is zero — and fills it from record: in place for a
// struct row, through a new struct for a pointer row.
func decodeRowInto(out reflect.Value, record []string, cols columnMap, t rowsTarget, at rowAt) error {
	//: one more slot, grown geometrically when full.
	out.Grow(1)
	out.SetLen(out.Len() + 1)
	//: the slot.
	slot := out.Index(out.Len() - 1)
	//: a pointer row points at a new struct.
	if t.ptr {
		slot.Set(reflect.New(t.elem))
		slot = slot.Elem()
	}
	//: its fields from the record.
	return fillRow(slot, record, cols, t.schema, at)
}

// isPromotionHeader reports whether header is the codec package's promotion
// container's: one "_json" cell.
func isPromotionHeader(header []string) bool {
	//: exactly the one cell.
	return len(header) == promotionColumnCount && header[0] == promotionHeaderCell
}

// hasColumn reports whether schema maps a column called name.
func hasColumn(schema *tableSchema, name string) bool {
	//: by name.
	_, ok := schema.byName[name]
	return ok
}

// unmarshalFailed wraps encoding/csv's reading failure, placed where it
// says.
func unmarshalFailed(err error) error {
	//: the stdlib error, kept as the cause.
	return errs.Wrap(err, errs.WrapParams{
		Code:    corecsv.CodeCSVUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "CSV decoding failed",
		Private: "service/data/codec/csv: encoding/csv.Reader.Read returned an error",
	}, parsePosition(err)...)
}

// parsePosition returns the place encoding/csv's ParseError gives err — the
// 1-based line and byte column — as the family's fields (#1125), or none for
// any other failure.
func parsePosition(err error) []errs.FieldValue {
	pe, ok := errors.AsType[*stdcsv.ParseError](err)
	//: a reader's failure, not a parse error: nowhere to place it.
	if !ok {
		return nil
	}
	//: where the stdlib stopped.
	return codec.PositionFields(codec.PositionValue{Line: pe.Line, Column: pe.Column})
}

// format is how a codec instance writes and reads: its delimiter, the
// formula mitigation, and whether a typed table has a header row.
type format struct {
	// comma is the delimiter.
	comma rune
	// escape applies the OWASP formula mitigation when writing.
	escape bool
	// noHeader is whether a typed table has no header row: its columns are
	// the fields, in order.
	noHeader bool
}
