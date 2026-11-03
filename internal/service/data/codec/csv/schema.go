package csv

import (
	"encoding"
	"reflect"
	"strings"
	"sync"

	corecsv "github.com/kitsunium/sdk/internal/core/data/codec/csv"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The cell conversions a column can take.
const (
	// kindString is a string field: the cell as it is.
	kindString cellKind = iota
	// kindInt is a signed integer field: strconv.ParseInt / AppendInt, base 10.
	kindInt
	// kindUint is an unsigned integer field: strconv.ParseUint / AppendUint.
	kindUint
	// kindFloat is a float field: strconv.ParseFloat / AppendFloat 'g', -1.
	kindFloat
	// kindBool is a bool field: strconv.ParseBool / AppendBool.
	kindBool
	// kindText is a field whose type reads or writes itself as text:
	// encoding.TextUnmarshaler / TextMarshaler — time.Time among them.
	kindText
)

// The tag the typed mapping reads, and its one option.
const (
	// tagKey is the struct tag naming a field's column.
	tagKey = "csv"
	// optionalOption marks a column a file may lack.
	optionalOption = "optional"
)

// Interface types the schema checks fields against.
var (
	// textMarshalerType is encoding.TextMarshaler.
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	// textUnmarshalerType is encoding.TextUnmarshaler.
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	// schemas caches each struct type's tableSchema (or the error building it).
	schemas sync.Map
)

// cellKind is how a column's cells convert to and from its field.
type cellKind uint8

// columnSpec is one column of a typed table: its header name and the field
// behind it.
type columnSpec struct {
	// name is the header cell: the tag's name, else the Go field's.
	name string
	// index is the path to the field for reflect.Value.FieldByIndex.
	index []int
	// required is whether a file must carry the column (no ",optional").
	required bool
	// kind is the cell conversion.
	kind cellKind
	// ptr is whether the field is a pointer to its cell type: nil writes an
	// empty cell, and an empty cell reads as nil.
	ptr bool
	// typ is the cell type: the field's, or what it points to.
	typ reflect.Type
}

// tableSchema is the columns of a struct type, in field order.
type tableSchema struct {
	// columns are the mapped fields, in the order they are declared.
	columns []columnSpec
	// names are the columns' header cells, in order: the header Marshal writes.
	names []string
	// byName finds a column by its header cell.
	byName map[string]int
}

// schemaResult is what the cache holds for a type: its schema or why it
// has none.
type schemaResult struct {
	// schema is the type's columns, when it has some.
	schema *tableSchema
	// err is the refusal, when it has none.
	err error
}

// schemaOf returns the columns of struct type t, built once per type. A type
// with no mapped field, a field whose type no cell converts to, or two fields
// naming one column is refused with VALUE_INVALID — which the codec package's
// facade reads as "promote the value", as it always did for a struct.
func schemaOf(t reflect.Type) (*tableSchema, error) {
	//: built once per type.
	if cached, ok := schemas.Load(t); ok {
		//: the schema, or the refusal, as first built.
		res, _ := cached.(schemaResult)
		return res.schema, res.err
	}
	//: the columns, field by field.
	s := &tableSchema{byName: map[string]int{}}
	err := s.collect(t, nil)
	//: a struct mapping nothing is not a table.
	if err == nil && len(s.columns) == 0 {
		err = schemaInvalid("the struct maps no field to a column")
	}
	//: keep the first build: concurrent builders agree anyway.
	res := schemaResult{schema: s, err: err}
	//: a refused type has no schema.
	if err != nil {
		res.schema = nil
	}
	schemas.LoadOrStore(t, res)
	//: the schema or the refusal.
	return res.schema, res.err
}

// collect adds t's fields, prefix being the index path to t: exported fields
// in declaration order, an exported embedded struct's fields in its place.
func (s *tableSchema) collect(t reflect.Type, prefix []int) error {
	//: each field of the struct.
	for i := range t.NumField() {
		//: mapped, lent through, or skipped.
		if err := s.field(t.Field(i), append(append(make([]int, 0, len(prefix)+1), prefix...), i)); err != nil {
			return err
		}
	}
	//: every field mapped.
	return nil
}

// field maps one struct field, index being its path from the root: skipped
// by its tag or when unexported, an embedded struct without a column name of
// its own lending its fields in its place, any other field a column.
func (s *tableSchema) field(f reflect.StructField, index []int) error {
	//: the tag's name and options.
	name, opts, tagged := strings.Cut(f.Tag.Get(tagKey), ",")
	//: skipped by its tag, or unexported.
	if name == "-" && !tagged || !f.IsExported() {
		return nil
	}
	//: an embedded struct lends its fields.
	if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct && !isText(f.Type) {
		//: its fields, in its place.
		return s.collect(f.Type, index)
	}
	//: the column the field is written under.
	return s.add(f, name, opts, index)
}

// add maps field f to a column: name from the tag, else the Go name.
func (s *tableSchema) add(f reflect.StructField, name, opts string, index []int) error {
	//: the Go name when the tag names nothing.
	if name == "" {
		name = f.Name
	}
	//: one field per column.
	if _, dup := s.byName[name]; dup {
		//: two fields would read and write one column.
		return schemaInvalid("two fields map to one column")
	}
	//: the cell type, through one pointer.
	col := columnSpec{name: name, index: index, required: !hasOption(opts, optionalOption), typ: f.Type}
	//: a pointer field: nil is an empty cell.
	if col.typ.Kind() == reflect.Pointer {
		col.ptr, col.typ = true, col.typ.Elem()
	}
	//: how its cells convert.
	kind, ok := kindOf(col.typ)
	//: a map, a slice, a struct that is not text: no cell holds it.
	if !ok {
		//: refused, naming no value.
		return schemaInvalid("a field's type has no cell conversion")
	}
	col.kind = kind
	//: the column, in declaration order.
	s.byName[name] = len(s.columns)
	s.columns = append(s.columns, col)
	s.names = append(s.names, name)
	//: mapped.
	return nil
}

// hasOption reports whether the comma-separated opts holds option.
func hasOption(opts, option string) bool {
	//: each option in turn.
	for opts != "" {
		//: the next one.
		var opt string
		opt, opts, _ = strings.Cut(opts, ",")
		//: found.
		if opt == option {
			return true
		}
	}
	//: absent.
	return false
}

// kindOf is the conversion a cell type takes: text first, so a named string
// with its own MarshalText writes through it, then its kind.
func kindOf(t reflect.Type) (cellKind, bool) {
	//: a type reading or writing itself as text.
	if isText(t) {
		return kindText, true
	}
	//: the basic kinds.
	switch t.Kind() {
	//: the cell itself.
	case reflect.String:
		return kindString, true
	//: base-10 integers.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return kindInt, true
	//: base-10 unsigned integers; uintptr is not data.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return kindUint, true
	//: shortest round-trip floats.
	case reflect.Float32, reflect.Float64:
		return kindFloat, true
	//: true / false.
	case reflect.Bool:
		return kindBool, true
	//: nothing else fits a cell.
	default:
		return 0, false
	}
}

// isText reports whether t, or a pointer to it, reads or writes itself as
// text.
func isText(t reflect.Type) bool {
	//: either direction suffices: the other is checked when it is used.
	pt := reflect.PointerTo(t)
	//: a marshaler on the value or the pointer, or an unmarshaler.
	return t.Implements(textMarshalerType) || pt.Implements(textMarshalerType) || pt.Implements(textUnmarshalerType)
}

// schemaInvalid refuses a type the typed mapping cannot write or read, with
// VALUE_INVALID: the codec package's facade promotes such a value.
func schemaInvalid(private string) error {
	//: the dedicated sentinel, naming no value.
	return valueInvalid("service/data/codec/csv: " + private)
}

// headerMismatch refuses a header that lacks a required column, or names a
// column twice; column is the struct's name for it, never a cell of the file,
// carried as "name": "column" is the family's key for a position (#1125).
func headerMismatch(problem, column string) error {
	//: the sentinel, with what is wrong and which column.
	return errs.Wrap(corecsv.HeaderMismatch, errs.WrapParams{}, errs.String("problem", problem), errs.String("name", column))
}
