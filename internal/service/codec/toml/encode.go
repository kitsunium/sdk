// Package toml — the encode: a Go map or struct written as a TOML document.
// The layout is the one the previous library wrote, byte for byte on the
// values it accepted: a table's plain keys first, then its sub-tables and
// arrays of tables, each under its header; struct fields in declaration order
// and map keys sorted; strings literal when they can be, basic otherwise.
package toml

import (
	"cmp"
	"math"
	"reflect"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/recycler"
)

// The problems an encode refuses a value for.
const (
	// problemRoot: the value is not a map or a struct.
	problemRoot string = "a TOML document is a table: the value must be a map or a struct"
	// problemNil: a nil interface, or a nil pointer at the root.
	problemNil string = "a nil value has no TOML representation"
	// problemUnsupported: a channel, a function, a complex number or an
	// unsafe pointer.
	problemUnsupported string = "the type has no TOML representation"
	// problemUintRange: an unsigned integer above the largest int64.
	problemUintRange string = "the unsigned integer is larger than a TOML integer can hold"
	// problemKeyType: a map key that is not a string, a number or a
	// TextMarshaler.
	problemKeyType string = "the map's key type cannot be written as a TOML key"
	// problemKeyClash: two map keys written as the same TOML key.
	problemKeyClash string = "two map keys are written as the same TOML key"
	// problemTooDeepEncode: a value nested past maxDepth, or a cycle.
	problemTooDeepEncode string = "the value is nested too deep, or refers to itself"
	// problemNotUTF8: a string TOML cannot carry.
	problemNotUTF8 string = "a string is not valid UTF-8"
	// problemMarshalText: a TextMarshaler that failed.
	problemMarshalText string = "a MarshalText method returned an error"
)

// maxRetainedEncoderEntries is the entry capacity above which an encoder is
// dropped rather than pooled.
const maxRetainedEncoderEntries int = 4096

// commentPrefix starts a commented-out line.
const commentPrefix string = "# "

// entry is one key of a table being encoded.
type entry struct {
	// key is the TOML key.
	key string
	// value is the Go value.
	value reflect.Value
	// opts are the struct tag's options, zero for a map entry.
	opts fieldOptions
}

// encoder writes one document. It is pooled with its scratch slices.
type encoder struct {
	// buf is the document being written.
	buf []byte
	// keys is the dotted key of the table being written.
	keys []string
	// free holds entry slices for reuse, one per table level in flight.
	free [][]entry
	// depth is how many tables and arrays enclose the value being written.
	depth int32
	// lastWasHeader says the last line written was a table header.
	lastWasHeader bool
}

// encoderPool recycles encoders.
var encoderPool = recycler.NewCappedPool[*encoder](
	func() *encoder { return new(encoder) },
	(*encoder).reset,
	(*encoder).retained,
	maxRetainedEncoderEntries,
)

// reset empties the encoder for the next document.
func (e *encoder) reset() {
	e.buf = nil
	e.keys = e.keys[:0]
	e.depth = 0
	e.lastWasHeader = false
	//: the entries hold reflect.Values: cleared so they pin no caller memory.
	for i := range e.free {
		clear(e.free[i][:cap(e.free[i])])
	}
}

// retained is the entry capacity the encoder would keep in the pool.
func (e *encoder) retained() int {
	total := 0
	//: each pooled slice.
	for _, s := range e.free {
		total += cap(s)
	}
	//: the total.
	return total
}

// encodeDocument appends the document v encodes to dst.
func encodeDocument(dst []byte, v any) ([]byte, error) {
	e := encoderPool.Get()
	defer encoderPool.Put(e)
	e.buf = dst
	root, ok := resolve(reflect.ValueOf(v))
	//: nil, or a pointer to nothing.
	if !ok {
		//: refused.
		return dst, encodeFail(problemNil, reflect.TypeOf(v))
	}
	//: only a table is a document.
	if infoOf(root.Type()).is(typeValue) {
		//: refused.
		return dst, encodeFail(problemRoot, root.Type())
	}
	//: the root table.
	if err := e.table(root, false); err != nil {
		//: refused, dst unchanged.
		return dst, err
	}
	//: the document.
	return e.buf, nil
}

// resolve follows pointers and interfaces to a concrete value, and reports
// false when one of them is nil.
func resolve(v reflect.Value) (reflect.Value, bool) {
	//: until a value that is neither.
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		//: nil ends the chain.
		if v.IsNil() {
			//: nothing.
			return v, false
		}
		v = v.Elem()
	}
	//: the value, or invalid for an untyped nil.
	return v, v.IsValid()
}

// encodeFail returns MARSHAL_FAILED naming problem and the Go type concerned.
func encodeFail(problem string, t reflect.Type) error {
	name := "nil"
	//: an untyped nil has no type.
	if t != nil {
		name = t.String()
	}
	//: the sentinel's code and messages, with the problem as fields.
	return errs.Wrap(MarshalFailed, errs.WrapParams{}, errs.String(fieldProblem, problem), errs.String(fieldType, name))
}

// table writes the key-values of table v, a map or a struct, then its
// sub-tables and arrays of tables under their headers.
func (e *encoder) table(v reflect.Value, commented bool) error {
	//: a cycle, or a structure nested past the cap.
	if e.depth >= maxDepth {
		//: refused.
		return encodeFail(problemTooDeepEncode, v.Type())
	}
	e.depth++
	defer func() { e.depth-- }()
	entries, err := e.collect(v)
	//: a map key that cannot be written.
	if err != nil {
		//: refused.
		return err
	}
	defer e.release(entries)
	//: the key-values come first: after a header, a key belongs to it.
	if err := e.keyValues(entries, commented); err != nil {
		//: refused.
		return err
	}
	//: then the sub-tables.
	return e.subTables(entries, commented)
}

// keyValues writes every entry that is not a table.
func (e *encoder) keyValues(entries []entry, commented bool) error {
	//: each entry, in order.
	for i := range entries {
		//: a table waits for the second pass.
		if e.isTable(&entries[i]) {
			continue
		}
		//: the key-value line.
		if err := e.keyValue(&entries[i], commented); err != nil {
			//: refused.
			return err
		}
	}
	//: written.
	return nil
}

// subTables writes every entry that is a table or an array of tables, each
// under its header.
func (e *encoder) subTables(entries []entry, commented bool) error {
	//: each entry, in order.
	for i := range entries {
		ent := &entries[i]
		//: a key-value was written by the first pass.
		if !e.isTable(ent) {
			continue
		}
		e.keys = append(e.keys, ent.key)
		err := e.subTable(ent, commented || ent.opts.commented)
		e.keys = e.keys[:len(e.keys)-1]
		//: refused.
		if err != nil {
			//: refused.
			return err
		}
	}
	//: written.
	return nil
}

// subTable writes one table entry: a [header] and its table, or one
// [[header]] and table per element of an array of tables.
func (e *encoder) subTable(ent *entry, commented bool) error {
	v, _ := resolve(ent.value)
	//: an array of tables.
	if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
		//: one header per element.
		return e.arrayOfTables(v, ent.opts.comment, commented)
	}
	e.header(ent.opts.comment, commented, false)
	//: the table under its header.
	return e.table(v, commented)
}

// arrayOfTables writes each element of v under its own [[header]]. The comment
// is written above the first only.
func (e *encoder) arrayOfTables(v reflect.Value, comment string, commented bool) error {
	//: each element.
	for i := range v.Len() {
		element, _ := resolve(v.Index(i))
		e.header(comment, commented, true)
		comment = ""
		//: the element's table.
		if err := e.table(element, commented); err != nil {
			//: refused.
			return err
		}
	}
	//: written.
	return nil
}

// header writes a [table] or [[array of tables]] header for the current key,
// after an empty line unless it follows another header or opens the document.
func (e *encoder) header(comment string, commented, array bool) {
	//: a blank line separates a header from the content before it.
	if len(e.buf) > 0 && !e.lastWasHeader {
		e.buf = append(e.buf, charNewline)
	}
	e.buf = appendComment(e.buf, comment)
	//: a commented table comments out its header too.
	if commented {
		e.buf = append(e.buf, commentPrefix...)
	}
	e.buf = append(e.buf, charOpenBracket)
	//: [[ for an array of tables.
	if array {
		e.buf = append(e.buf, charOpenBracket)
	}
	//: the dotted key.
	for i, part := range e.keys {
		//: dots between the parts.
		if i > 0 {
			e.buf = append(e.buf, charDot)
		}
		e.buf = appendKey(e.buf, part)
	}
	e.buf = append(e.buf, charCloseBracket)
	//: ]] for an array of tables.
	if array {
		e.buf = append(e.buf, charCloseBracket)
	}
	e.buf = append(e.buf, charNewline)
	e.lastWasHeader = true
}

// appendComment writes each line of comment as a TOML comment.
func appendComment(b []byte, comment string) []byte {
	//: each line.
	for comment != "" {
		line, rest, _ := cutLine(comment)
		b = append(b, commentPrefix...)
		b = append(b, line...)
		b = append(b, charNewline)
		comment = rest
	}
	//: the comment.
	return b
}

// cutLine splits s at its first newline.
func cutLine(s string) (line, rest string, found bool) {
	//: each byte.
	for i := range len(s) {
		//: the first newline.
		if s[i] == charNewline {
			//: before and after it.
			return s[:i], s[i+1:], true
		}
	}
	//: no newline.
	return s, "", false
}

// keyValue writes one key = value line.
func (e *encoder) keyValue(ent *entry, commented bool) error {
	commented = commented || ent.opts.commented
	e.buf = appendComment(e.buf, ent.opts.comment)
	start := len(e.buf)
	//: a commented key-value is a comment.
	if commented {
		e.buf = append(e.buf, commentPrefix...)
	}
	e.buf = appendKey(e.buf, ent.key)
	e.buf = append(e.buf, " = "...)
	var err error
	e.buf, err = e.appendValue(e.buf, ent.value, ent.opts)
	//: refused.
	if err != nil {
		//: refused.
		return err
	}
	//: a commented value written across lines is commented on every line.
	if commented {
		e.buf = commentLines(e.buf, start)
	}
	e.buf = append(e.buf, charNewline)
	e.lastWasHeader = false
	//: written.
	return nil
}

// commentLines prefixes every line after the first of b[start:] with the
// comment prefix.
func commentLines(b []byte, start int) []byte {
	region := slices.Clone(b[start:])
	b = b[:start]
	//: each byte of the region, a prefix after each newline.
	for _, c := range region {
		b = append(b, c)
		//: the next line is commented too.
		if c == charNewline {
			b = append(b, commentPrefix...)
		}
	}
	//: the commented region.
	return b
}

// isTable reports whether an entry is written under a header: a map or a
// struct, or a non-empty array whose every element is one, unless the field
// asked to be inline.
func (e *encoder) isTable(ent *entry) bool {
	//: an inline field is a key-value whatever it holds.
	if ent.opts.inline {
		//: a key-value.
		return false
	}
	v, ok := resolve(ent.value)
	//: a nil value is written as a value.
	if !ok {
		//: a key-value.
		return false
	}
	//: a table.
	if !infoOf(v.Type()).is(typeValue) {
		//: under a header.
		return true
	}
	//: an array of tables.
	return isArrayOfTables(v)
}

// isArrayOfTables reports whether v is a non-empty slice or array whose every
// element is a map or a struct.
func isArrayOfTables(v reflect.Value) bool {
	//: only a non-empty slice or array.
	if (v.Kind() != reflect.Slice && v.Kind() != reflect.Array) || v.Len() == 0 {
		//: no.
		return false
	}
	//: every element.
	for i := range v.Len() {
		element, ok := resolve(v.Index(i))
		//: a nil element, or a value.
		if !ok || infoOf(element.Type()).is(typeValue) {
			//: no.
			return false
		}
	}
	//: yes.
	return true
}

// collect returns the entries of table v: a struct's fields, nil ones and the
// ones the tag omits left out, or a map's entries sorted by key.
func (e *encoder) collect(v reflect.Value) ([]entry, error) {
	entries := e.acquire()
	//: a map.
	if v.Kind() == reflect.Map {
		//: its entries, sorted.
		return e.mapEntries(entries, v)
	}
	info := infoOf(v.Type())
	//: a tag naming a key that is not UTF-8.
	if info.is(typeBadNames) {
		//: refused.
		return entries, encodeFail(problemNotUTF8, v.Type())
	}
	//: a struct.
	return structEntries(entries, v, info), nil
}

// acquire returns an empty entry slice, reused when one is free.
func (e *encoder) acquire() []entry {
	//: a slice an earlier table returned.
	if n := len(e.free); n > 0 {
		s := e.free[n-1]
		e.free = e.free[:n-1]
		//: emptied.
		return s[:0]
	}
	//: a fresh one.
	return nil
}

// release returns an entry slice for reuse.
func (e *encoder) release(s []entry) {
	//: a slice that never grew holds nothing worth keeping.
	if cap(s) > 0 {
		e.free = append(e.free, s)
	}
}

// structEntries appends the fields of struct v that are written.
func structEntries(entries []entry, v reflect.Value, info *typeInfo) []entry {
	//: each field, in declaration order.
	for i := range info.fields {
		f := &info.fields[i]
		fv, ok := fieldOf(v, f.index)
		//: a nil embedded pointer on the way, or a field the tag omits.
		if !ok || omitted(fv, f.opts) {
			continue
		}
		entries = append(entries, entry{key: f.name, value: fv, opts: f.opts})
	}
	//: the entries.
	return entries
}

// fieldOf returns the field of v at index, reporting false when a nil
// embedded pointer is on the way.
func fieldOf(v reflect.Value, index []int) (reflect.Value, bool) {
	//: each step.
	for i, step := range index {
		//: an embedded pointer is followed.
		if i > 0 && v.Kind() == reflect.Pointer {
			//: nil: the fields under it are not there.
			if v.IsNil() {
				//: absent.
				return v, false
			}
			v = v.Elem()
		}
		v = v.Field(step)
	}
	//: the field.
	return v, true
}

// omitted reports whether a struct field is left out: a nil pointer, map or
// interface always, an empty or zero value when the tag says so.
func omitted(v reflect.Value, opts fieldOptions) bool {
	switch v.Kind() {
	//: nil has nothing to write.
	case reflect.Pointer, reflect.Map, reflect.Interface:
		//: nil.
		if v.IsNil() {
			//: left out.
			return true
		}
	//: other kinds are never nil.
	default:
	}
	//: the tag's options.
	return (opts.omitEmpty && isEmpty(v)) || (opts.omitZero && isZero(v))
}

// mapEntries appends the entries of map v, sorted by key, leaving out nil
// interface values; a nil pointer is written as its type's zero value.
func (e *encoder) mapEntries(entries []entry, v reflect.Value) ([]entry, error) {
	t := v.Type()
	// One reusable key and one slice of values: MapIter.Key and Value would
	// allocate a copy of every key and value that is not pointer-shaped.
	key := reflect.New(t.Key()).Elem()
	values := reflect.MakeSlice(reflect.SliceOf(t.Elem()), v.Len(), v.Len())
	var iter reflect.MapIter
	iter.Reset(v)
	//: each entry.
	for i := 0; iter.Next(); i++ {
		key.SetIterKey(&iter)
		value := values.Index(i)
		value.SetIterValue(&iter)
		//: a nil interface value is left out.
		if value.Kind() == reflect.Interface && value.IsNil() {
			continue
		}
		name, err := mapKeyString(key)
		//: a key that cannot be written.
		if err != nil {
			//: refused.
			return entries, err
		}
		//: TOML documents are UTF-8.
		if !utf8.ValidString(name) {
			//: refused.
			return entries, encodeFail(problemNotUTF8, t)
		}
		entries = append(entries, entry{key: name, value: nilToZero(value)})
	}
	slices.SortFunc(entries, func(a, b entry) int { return cmp.Compare(a.key, b.key) })
	//: the entries, checked for two keys written alike.
	return entries, keyClash(entries, t)
}

// nilToZero returns the zero value of a nil pointer's element, and any other
// value unchanged.
func nilToZero(v reflect.Value) reflect.Value {
	//: a nil pointer in a map is written as the zero value it points to.
	if v.Kind() == reflect.Pointer && v.IsNil() {
		//: the zero value.
		return reflect.Zero(v.Type().Elem())
	}
	//: unchanged.
	return v
}

// keyClash refuses sorted entries with two equal keys, which a map whose keys
// are written alike — two TextMarshalers, say — produces.
func keyClash(entries []entry, t reflect.Type) error {
	//: neighbours are equal when keys are.
	for i := 1; i < len(entries); i++ {
		//: two entries, one key.
		if entries[i].key == entries[i-1].key {
			//: refused.
			return encodeFail(problemKeyClash, t)
		}
	}
	//: distinct.
	return nil
}

// mapKeyString returns the TOML key a map key is written as.
func mapKeyString(k reflect.Value) (string, error) {
	info := infoOf(k.Type())
	//: a type that writes its own text.
	if info.is(typeTextMarshaler) || info.is(typeTextMarshalerPtr) {
		text, err := marshalText(k, info)
		//: the text, or the method's refusal.
		return string(text), err
	}
	switch k.Kind() {
	//: the common case.
	case reflect.String:
		return k.String(), nil
	//: a signed integer, in decimal.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(k.Int(), int(decimalBase)), nil
	//: an unsigned integer, in decimal.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(k.Uint(), int(decimalBase)), nil
	//: a float, in its shortest decimal form.
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(k.Float(), 'f', -1, k.Type().Bits()), nil
	//: nothing else.
	default:
		return "", encodeFail(problemKeyType, k.Type())
	}
}

// isEmpty implements omitempty: false, 0, "", a nil pointer or interface, an
// empty array, slice or map, a zero time or local value, and a struct whose
// every written field is empty.
func isEmpty(v reflect.Value) bool {
	switch v.Kind() {
	//: the scalar kinds are empty at their zero.
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.String, reflect.Pointer, reflect.Interface:
		return v.IsZero()
	//: containers are empty without elements.
	case reflect.Array, reflect.Slice, reflect.Map:
		return v.Len() == 0
	//: a struct.
	case reflect.Struct:
		return isEmptyStruct(v)
	//: the kinds TOML cannot write are never empty.
	default:
		return false
	}
}

// isEmptyStruct reports whether struct v is empty: a value-like struct when it
// is its zero, a table when every field it would write is empty.
func isEmptyStruct(v reflect.Value) bool {
	info := infoOf(v.Type())
	//: time.Time, the local types, a TextMarshaler: their fields are private.
	if info.is(typeValue) {
		//: empty at its zero.
		return v.IsZero()
	}
	//: each field that would be written.
	for i := range info.fields {
		fv, ok := fieldOf(v, info.fields[i].index)
		//: one that is not empty.
		if ok && !isEmpty(fv) {
			//: not empty.
			return false
		}
	}
	//: empty.
	return true
}

// isZero implements omitzero: the type's own IsZero when it has one, the
// reflect zero otherwise.
func isZero(v reflect.Value) bool {
	//: no IsZero method.
	if !infoOf(v.Type()).is(typeZeroer) {
		//: the zero value of the type.
		return v.IsZero()
	}
	//: a value receiver.
	if z, ok := reflect.TypeAssert[interface{ IsZero() bool }](v); ok {
		//: the type's own answer.
		return z.IsZero()
	}
	ptr := reflect.New(v.Type())
	ptr.Elem().Set(v)
	z, ok := reflect.TypeAssert[interface{ IsZero() bool }](ptr)
	//: a pointer receiver's answer, on a copy.
	return ok && z.IsZero()
}

// appendFloat writes a float as the previous library wrote it: inf, -inf,
// nan, or the shortest decimal that round-trips, with ".0" when it has
// neither a fraction nor an exponent.
func appendFloat(b []byte, f float64, bits int) []byte {
	switch {
	//: not a number.
	case math.IsNaN(f):
		return append(b, wordNaN...)
	//: positive infinity.
	case math.IsInf(f, 1):
		return append(b, wordInf...)
	//: negative infinity.
	case math.IsInf(f, -1):
		return append(b, '-', 'i', 'n', 'f')
	//: a finite value.
	default:
		start := len(b)
		b = strconv.AppendFloat(b, f, 'f', -1, bits)
		//: a TOML float needs a fraction or an exponent.
		if !slices.Contains(b[start:], charDot) {
			b = append(b, '.', '0')
		}
		//: the float.
		return b
	}
}
