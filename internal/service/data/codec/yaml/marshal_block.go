package yaml

import (
	"cmp"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The order map keys of different kinds are written in.
const (
	// rankNull is a nil key.
	rankNull keyRank = iota
	// rankBool is a boolean key.
	rankBool
	// rankInt is a signed integer key.
	rankInt
	// rankUint is an unsigned integer key.
	rankUint
	// rankFloat is a float key.
	rankFloat
	// rankString is a string key.
	rankString
	// rankOther is any other key, which keyText refuses.
	rankOther
)

// keyRank orders keys of different kinds.
type keyRank uint8

// renderedKey is a mapping key's text, and whether it is a string (to quote
// as needed) or a number or boolean (written as it is).
type renderedKey struct {
	// text is the key's text.
	text string
	// isString says the text is a string.
	isString bool
}

// openBlock writes what precedes a block collection's first entry, placed
// at, and returns the column of its entries and whether the first entry
// continues the current line.
func (e *encoder) openBlock(col int, at place) (inner int, firstInline bool) {
	switch at {
	//: the root's entries start the document.
	case atRoot:
		//: column 0.
		return col + indentStep, false
	//: a key's collection starts on the next line, one step deeper.
	case afterKey:
		e.buf.WriteByte('\n')
		//: one step deeper.
		return col + indentStep, false
	//: an entry's collection starts on the entry's line.
	default:
		e.buf.WriteByte(' ')
		//: one step deeper, the first entry already placed.
		return col + indentStep, true
	}
}

// writeIndent writes n spaces.
func (e *encoder) writeIndent(n int) {
	//: one space at a time is what bytes.Buffer does anyway.
	for range n {
		e.buf.WriteByte(' ')
	}
}

// writeEntryStart writes the indentation of a collection's entry: none for
// the first entry of a collection that continues its parent's line.
func (e *encoder) writeEntryStart(inner int, first, firstInline bool) {
	//: the first entry of an inline collection is already placed.
	if !first || !firstInline {
		e.writeIndent(inner)
	}
}

// writeStringMap writes a map[string]any, keys sorted.
func (e *encoder) writeStringMap(m map[string]any, col int, at place, flow bool) error {
	//: one level deeper.
	if err := e.enter(); err != nil {
		//: refused.
		return err
	}
	defer e.leave()
	//: an empty mapping.
	if len(m) == 0 {
		e.writeScalarText("{}", at)
		//: written.
		return nil
	}
	keys := slices.Sorted(maps.Keys(m))
	//: in flow style.
	if flow {
		e.openFlow(at)
		//: the flow mapping.
		return e.finishFlow(e.writeFlowStringMap(m, keys))
	}
	inner, firstInline := e.openBlock(col, at)
	//: every entry.
	for i, k := range keys {
		e.writeEntryStart(inner, i == 0, firstInline)
		//: the key, then the value.
		if err := e.writeKeyThen(k, func() error { return e.writeAny(m[k], inner, afterKey, false) }); err != nil {
			//: refused.
			return err
		}
	}
	//: written.
	return nil
}

// writeKeyThen writes a string key and its ":", then calls value.
func (e *encoder) writeKeyThen(key string, value func() error) error {
	//: the key.
	if err := e.writeKey(key); err != nil {
		//: refused.
		return err
	}
	//: the value.
	return value()
}

// writeList writes a []any.
func (e *encoder) writeList(list []any, col int, at place, flow bool) error {
	//: one level deeper.
	if err := e.enter(); err != nil {
		//: refused.
		return err
	}
	defer e.leave()
	//: an empty sequence.
	if len(list) == 0 {
		e.writeScalarText("[]", at)
		//: written.
		return nil
	}
	//: in flow style.
	if flow {
		e.openFlow(at)
		//: the flow sequence.
		return e.finishFlow(e.writeFlowList(list))
	}
	inner, firstInline := e.openBlock(col, at)
	//: every entry.
	for i, item := range list {
		e.writeEntryStart(inner, i == 0, firstInline)
		e.buf.WriteByte('-')
		//: the entry.
		if err := e.writeAny(item, inner, afterDash, false); err != nil {
			//: refused.
			return err
		}
	}
	//: written.
	return nil
}

// writeMap writes a map, keys sorted: numbers by value, strings by bytes.
func (e *encoder) writeMap(v reflect.Value, col int, at place, flow bool) error {
	//: an empty mapping.
	if v.Len() == 0 {
		e.writeScalarText("{}", at)
		//: written.
		return nil
	}
	//: a block mapping with plain string keys, without reflect's per-entry
	//: allocations.
	if !flow && textKeyed(v.Type().Key()) {
		//: keys read once, sorted by bytes.
		return e.writeTextKeyedMap(v, col, at)
	}
	keys := v.MapKeys()
	slices.SortFunc(keys, compareKeys)
	//: in flow style.
	if flow {
		e.openFlow(at)
		//: the flow mapping.
		return e.finishFlow(e.writeFlowMap(v, keys))
	}
	inner, firstInline := e.openBlock(col, at)
	//: every entry.
	for i, k := range keys {
		e.writeEntryStart(inner, i == 0, firstInline)
		//: the key, then the value.
		if err := e.writeKeyValueThen(k, func() error { return e.writeValue(v.MapIndex(k), inner, afterKey, false) }); err != nil {
			//: refused.
			return err
		}
	}
	//: written.
	return nil
}

// textKeyed reports whether a map key type is a string kind whose text is its
// value: no hook rewrites it.
func textKeyed(key reflect.Type) bool {
	//: a string kind.
	if key.Kind() != reflect.String {
		//: no.
		return false
	}
	//: a predeclared string has no hook.
	if !mayHaveHooks(key) {
		//: yes.
		return true
	}
	h := hooksOf(key)
	//: no hook rewrites the key.
	return !h.has(hookMarshaler) && !h.has(hookTextMarshaler)
}

// writeTextKeyedMap writes a non-empty map whose keys are plain strings, keys
// sorted by bytes. MapKeys and MapIndex each allocate per entry; here the
// keys are read into one slice and the values into one reflected slice, so
// the cost is a handful of allocations whatever the map's size.
func (e *encoder) writeTextKeyedMap(v reflect.Value, col int, at place) error {
	size := v.Len()
	keys := make([]string, 0, size)
	values := reflect.MakeSlice(reflect.SliceOf(v.Type().Elem()), size, size)
	key := reflect.New(v.Type().Key()).Elem()
	iter := v.MapRange()
	//: every entry, in the map's order.
	for i := 0; iter.Next(); i++ {
		key.SetIterKey(iter)
		keys = append(keys, key.String())
		values.Index(i).SetIterValue(iter)
	}
	order := make([]int, size)
	//: the identity permutation, sorted by key.
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return strings.Compare(keys[a], keys[b]) })
	inner, firstInline := e.openBlock(col, at)
	//: every entry, in key order.
	for i, j := range order {
		e.writeEntryStart(inner, i == 0, firstInline)
		//: the key, then the value.
		if err := e.writeKeyThen(keys[j], func() error { return e.writeValue(values.Index(j), inner, afterKey, false) }); err != nil {
			//: refused.
			return err
		}
	}
	//: written.
	return nil
}

// writeStruct writes a struct's fields in declaration order, omitempty
// fields left out when empty, then its inline map's entries, keys sorted.
func (e *encoder) writeStruct(v reflect.Value, col int, at place, flow bool) error {
	info := structInfoOf(v.Type())
	//: a malformed tag.
	if info.tagError != "" {
		//: refused, naming the type.
		return marshalError(info.tagError, v.Type().String())
	}
	inline, err := inlineKeys(v, info)
	//: an inline key colliding with a field.
	if err != nil {
		//: refused.
		return err
	}
	//: no field to write.
	if !hasVisibleField(v, info) && len(inline) == 0 {
		e.writeScalarText("{}", at)
		//: written.
		return nil
	}
	//: in flow style.
	if flow {
		e.openFlow(at)
		//: the flow mapping.
		return e.finishFlow(e.writeFlowStruct(v, info, inline))
	}
	inner, firstInline := e.openBlock(col, at)
	pending, err := e.writeStructFields(v, info, inner, firstInline)
	//: a field failed.
	if err != nil {
		//: refused.
		return err
	}
	//: the inline map's entries.
	return e.writeInlineEntries(v, info, inline, inner, pending)
}

// writeStructFields writes v's visible fields as block entries and reports
// whether the first entry is still to come, placed inline.
func (e *encoder) writeStructFields(v reflect.Value, info *structInfo, inner int, firstInline bool) (bool, error) {
	pending := firstInline
	//: every field.
	for _, f := range info.fields {
		fv, ok := fieldForRead(v, f.index)
		//: absent behind a nil inlined pointer, or empty and omitempty.
		if !ok || (f.omitEmpty && isEmpty(fv)) {
			continue
		}
		e.writeEntryStart(inner, pending, firstInline)
		pending = false
		//: the key, then the value.
		if err := e.writeKeyThen(f.key, func() error { return e.writeValue(fv, inner, afterKey, f.flow) }); err != nil {
			//: refused.
			return false, err
		}
	}
	//: whether the inline entries open the collection.
	return pending, nil
}

// writeInlineEntries writes the entries of v's inline map, keys in the order
// given. pending says the first of them continues its parent's line.
func (e *encoder) writeInlineEntries(v reflect.Value, info *structInfo, keys []reflect.Value, inner int, pending bool) error {
	m, _ := fieldForRead(v, info.inlineMap)
	//: every entry.
	for _, k := range keys {
		e.writeEntryStart(inner, pending, pending)
		pending = false
		//: the key, then the value.
		if err := e.writeKeyValueThen(k, func() error { return e.writeValue(m.MapIndex(k), inner, afterKey, false) }); err != nil {
			//: refused.
			return err
		}
	}
	//: written.
	return nil
}

// inlineKeys returns the keys of v's inline map, sorted, refusing one a field
// declares.
func inlineKeys(v reflect.Value, info *structInfo) ([]reflect.Value, error) {
	m, ok := fieldForRead(v, info.inlineMap)
	//: no inline map, or an empty one.
	if info.inlineMap == nil || !ok || m.Len() == 0 {
		//: none.
		return nil, nil
	}
	keys := m.MapKeys()
	slices.SortFunc(keys, compareKeys)
	//: every key against the fields.
	for _, k := range keys {
		//: a field declares it.
		if _, clash := info.byKey[k.String()]; clash {
			//: refused, as yaml.v3 refuses it.
			return nil, marshalError("an inline map key is also a field's key", v.Type().String())
		}
	}
	//: the keys.
	return keys, nil
}

// hasVisibleField reports whether writing v writes at least one field.
func hasVisibleField(v reflect.Value, info *structInfo) bool {
	//: the first field written answers.
	for _, f := range info.fields {
		fv, ok := fieldForRead(v, f.index)
		//: written.
		if ok && (!f.omitEmpty || !isEmpty(fv)) {
			//: yes.
			return true
		}
	}
	//: none.
	return false
}

// fieldForRead returns the field at index in v, and false when a nil inlined
// pointer on the way means the field is absent.
func fieldForRead(v reflect.Value, index []int) (reflect.Value, bool) {
	//: one step per index.
	for k, i := range index {
		//: an inlined pointer to a struct.
		if k > 0 && v.Kind() == reflect.Pointer {
			//: absent.
			if v.IsNil() {
				//: not there.
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	//: the field.
	return v, true
}

// isEmpty reports whether omitempty leaves v out, as yaml.v3 decides it:
// IsZero when the type has it, otherwise by kind.
func isEmpty(v reflect.Value) bool {
	//: the type's own IsZero.
	if mayHaveHooks(v.Type()) && hooksOf(v.Type()).has(hookIsZeroer) {
		//: its answer.
		return zeroByHook(v)
	}
	//: by kind.
	return emptyByKind(v)
}

// zeroByHook asks v's IsZero; a nil pointer is empty without asking it.
func zeroByHook(v reflect.Value) bool {
	//: nil.
	if isNilValue(v) {
		//: empty.
		return true
	}
	z, ok := isZeroHook(v)
	//: its answer.
	return ok && z.IsZero()
}

// emptyByKind reports whether v is empty by its kind: nil, zero, an empty
// string, slice or map, or a struct whose exported fields are all empty. An
// array is never empty, as in yaml.v3.
func emptyByKind(v reflect.Value) bool {
	switch v.Kind() {
	//: something with a length.
	case reflect.String, reflect.Slice, reflect.Map:
		//: empty when zero.
		return v.Len() == 0
	//: nil.
	case reflect.Interface, reflect.Pointer:
		//: empty when nil.
		return v.IsNil()
	//: a struct.
	case reflect.Struct:
		//: every exported field empty.
		return structEmpty(v)
	//: a boolean or a number.
	default:
		//: empty when zero; any other kind never is.
		return isScalarKind(v.Kind()) && v.IsZero()
	}
}

// isScalarKind reports whether k is a boolean or a number kind.
func isScalarKind(k reflect.Kind) bool {
	//: Bool through Float64, in reflect's order.
	return k >= reflect.Bool && k <= reflect.Float64
}

// structEmpty reports whether every exported field of v is empty.
func structEmpty(v reflect.Value) bool {
	//: every field.
	for i := range v.NumField() {
		//: an exported one that is not empty.
		if v.Type().Field(i).IsExported() && !isEmpty(v.Field(i)) {
			//: not empty.
			return false
		}
	}
	//: all empty.
	return true
}

// writeSequence writes a slice or an array.
func (e *encoder) writeSequence(v reflect.Value, col int, at place, flow bool) error {
	//: an empty sequence.
	if v.Len() == 0 {
		e.writeScalarText("[]", at)
		//: written.
		return nil
	}
	//: in flow style.
	if flow {
		e.openFlow(at)
		//: the flow sequence.
		return e.finishFlow(e.writeFlowSequence(v))
	}
	inner, firstInline := e.openBlock(col, at)
	//: every entry.
	for i := range v.Len() {
		e.writeEntryStart(inner, i == 0, firstInline)
		e.buf.WriteByte('-')
		//: the entry.
		if err := e.writeValue(v.Index(i), inner, afterDash, false); err != nil {
			//: refused.
			return err
		}
	}
	//: written.
	return nil
}

// writeKey writes a string mapping key and its ":". A key is a scalar on one
// line of at most 1024 bytes, so every reader takes it for an implicit key.
func (e *encoder) writeKey(key string) error {
	start := e.buf.Len()
	//: the key's text.
	if err := e.appendStringKey(key, false); err != nil {
		//: refused.
		return err
	}
	//: YAML's bound on an implicit key, in characters as the decoder counts.
	if utf8.RuneCount(e.buf.Bytes()[start:]) > maxKeyRunes {
		//: refused.
		return marshalError("a mapping key longer than 1024 characters cannot be written as an implicit key", "")
	}
	e.buf.WriteByte(':')
	//: written.
	return nil
}

// appendStringKey writes key's text: plain when it reads back as the same
// string, double-quoted otherwise.
func (e *encoder) appendStringKey(key string, flow bool) error {
	//: not UTF-8.
	if !validText(key) {
		//: refused.
		return marshalError("a string is not valid UTF-8", "")
	}
	//: plain.
	if plainSafe(key, flow) {
		e.buf.WriteString(key)
		//: written.
		return nil
	}
	appendDoubleQuoted(e.buf, key)
	//: written.
	return nil
}

// writeKeyValueThen writes a reflected mapping key and its ":", then calls
// value.
func (e *encoder) writeKeyValueThen(k reflect.Value, value func() error) error {
	text, err := keyText(k, 0)
	//: not a scalar.
	if err != nil {
		//: refused.
		return err
	}
	//: a string key, quoted when it must be.
	if text.isString {
		//: the key, then the value.
		return e.writeKeyThen(text.text, value)
	}
	e.buf.WriteString(text.text)
	e.buf.WriteByte(':')
	//: the value.
	return value()
}

// keyText renders the map key k: a string, a number, a boolean, or the text
// a hook gives. Anything else — a struct, a pointer, a collection — would be
// a complex key, which the subset refuses.
func keyText(k reflect.Value, depth int) (renderedKey, error) {
	//: a hook returning a key returning a key…
	if depth > maxDepth {
		//: refused.
		return renderedKey{}, marshalError("a mapping key nests deeper than the encoder accepts", "")
	}
	//: an interface key holds the real one.
	if k.Kind() == reflect.Interface {
		//: nil, or what it holds.
		return interfaceKeyText(k, depth)
	}
	//: a hook.
	if key, hooked, err := hookedKeyText(k, depth); hooked || err != nil {
		//: its text.
		return key, err
	}
	//: by kind.
	return kindKeyText(k)
}

// interfaceKeyText renders an interface key: null when nil, what it holds
// otherwise.
func interfaceKeyText(k reflect.Value, depth int) (renderedKey, error) {
	//: nil.
	if k.IsNil() {
		//: the null key.
		return renderedKey{text: "null"}, nil
	}
	//: what it holds.
	return keyText(k.Elem(), depth+1)
}

// hookedKeyText renders a key through its MarshalText or MarshalYAML, and
// reports whether it has one.
func hookedKeyText(k reflect.Value, depth int) (renderedKey, bool, error) {
	t := k.Type()
	//: a predeclared type has no method.
	if !mayHaveHooks(t) {
		//: no hook.
		return renderedKey{}, false, nil
	}
	r, hooked, err := applyHooks(k)
	//: no hook, or a failed one.
	if !hooked || err != nil {
		//: as it is.
		return renderedKey{}, hooked, err
	}
	//: MarshalYAML's replacement, as a key.
	if r.shape == shapeAny {
		key, kerr := keyText(reflect.ValueOf(&r.replacement).Elem(), depth+1)
		//: the replacement's text.
		return key, true, kerr
	}
	//: the text.
	return renderedKey{text: r.text, isString: true}, true, nil
}

// kindKeyText renders a key by its kind.
func kindKeyText(k reflect.Value) (renderedKey, error) {
	switch k.Kind() {
	//: a string.
	case reflect.String:
		//: quoted as needed.
		return renderedKey{text: k.String(), isString: true}, nil
	//: a boolean.
	case reflect.Bool:
		//: true or false.
		return renderedKey{text: strconv.FormatBool(k.Bool())}, nil
	//: a signed integer.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		//: in decimal.
		return renderedKey{text: strconv.FormatInt(k.Int(), decimalBase)}, nil
	//: an unsigned integer.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		//: in decimal.
		return renderedKey{text: strconv.FormatUint(k.Uint(), decimalBase)}, nil
	//: a float.
	case reflect.Float32, reflect.Float64:
		//: as the encoder writes floats.
		return renderedKey{text: string(appendFloat(nil, k.Float(), k.Type().Bits()))}, nil
	//: a struct, a pointer, an array: a complex key.
	default:
		//: refused, naming the type.
		return renderedKey{}, marshalError("a mapping key must be a string, a number or a boolean", k.Type().String())
	}
}

// rankOf orders keys of different kinds: null, booleans, numbers, strings,
// then anything else.
func rankOf(k reflect.Value) keyRank {
	switch k.Kind() {
	//: null.
	case reflect.Invalid:
		//: first.
		return rankNull
	//: a boolean.
	case reflect.Bool:
		//: second.
		return rankBool
	//: a signed integer.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		//: a number.
		return rankInt
	//: an unsigned integer.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		//: a number.
		return rankUint
	//: a float.
	case reflect.Float32, reflect.Float64:
		//: a number.
		return rankFloat
	//: a string.
	case reflect.String:
		//: after the numbers.
		return rankString
	//: anything else.
	default:
		//: last.
		return rankOther
	}
}

// compareKeys orders two map keys deterministically: by rank, then by value.
func compareKeys(a, b reflect.Value) int {
	a, b = unwrapKey(a), unwrapKey(b)
	ra, rb := rankOf(a), rankOf(b)
	//: different kinds.
	if ra != rb {
		//: by rank.
		return cmp.Compare(ra, rb)
	}
	//: by value.
	return compareSameRank(a, b, ra)
}

// unwrapKey returns what an interface key holds.
func unwrapKey(k reflect.Value) reflect.Value {
	//: through interfaces.
	for k.Kind() == reflect.Interface {
		k = k.Elem()
	}
	//: the key.
	return k
}

// compareSameRank orders two keys of the same rank by value.
func compareSameRank(a, b reflect.Value, rank keyRank) int {
	switch rank {
	//: false before true.
	case rankBool:
		//: as integers.
		return cmp.Compare(boolOrder(a.Bool()), boolOrder(b.Bool()))
	//: signed.
	case rankInt:
		//: numerically.
		return cmp.Compare(a.Int(), b.Int())
	//: unsigned.
	case rankUint:
		//: numerically.
		return cmp.Compare(a.Uint(), b.Uint())
	//: floats.
	case rankFloat:
		//: numerically, NaN first.
		return cmp.Compare(a.Float(), b.Float())
	//: strings.
	case rankString:
		//: by bytes.
		return strings.Compare(a.String(), b.String())
	//: null, or a kind keyText refuses anyway.
	default:
		//: equal.
		return 0
	}
}

// boolOrder maps false to 0 and true to 1.
func boolOrder(b bool) int {
	//: true.
	if b {
		//: after false.
		return 1
	}
	//: false.
	return 0
}

// writeString writes a string, placed at: a literal block when it spans
// lines and every reader would read the block back, plain when it reads back
// as the same string everywhere, double-quoted otherwise.
func (e *encoder) writeString(s string, col int, at place) error {
	//: YAML is Unicode text; arbitrary bytes have no representation.
	if !validText(s) {
		//: refused.
		return marshalError("a string is not valid UTF-8", "")
	}
	//: a multi-line string, as a literal block.
	if literalFits(s, at) {
		e.writeLiteral(s, col, at)
		//: written.
		return nil
	}
	e.separate(at)
	//: plain.
	if plainSafe(s, false) {
		e.buf.WriteString(s)
	} else {
		appendDoubleQuoted(e.buf, s)
	}
	e.buf.WriteByte('\n')
	//: written.
	return nil
}

// literalFits reports whether s is written as a literal block placed at: it
// spans lines, a block carries it verbatim, and at the root its first line
// shows its indentation — libyaml reads a root indicator differently from the
// specification, so the encoder never writes one there.
func literalFits(s string, at place) bool {
	//: a multi-line string a block carries, outside the root's corner case.
	return strings.IndexByte(s, '\n') >= 0 && literalSafe(s) && (at != atRoot || !needsIndentIndicator(s))
}

// writeLiteral writes s as a literal block scalar: its header, then each line
// one step deeper than the parent, with the chomping indicator that keeps
// exactly s's trailing line breaks.
func (e *encoder) writeLiteral(s string, col int, at place) {
	content := col + indentStep
	//: the root's block sits one step in, the indentation yaml.v3 requires.
	if at == atRoot {
		content = indentStep
	}
	body := strings.TrimRight(s, "\n")
	trailing := len(s) - len(body)
	e.writeLiteralHeader(s, trailing, at)
	//: the lines.
	for line := range strings.SplitSeq(body, "\n") {
		//: an empty line carries no indentation.
		if line != "" {
			e.writeIndent(content)
			e.buf.WriteString(line)
		}
		e.buf.WriteByte('\n')
	}
	//: kept trailing breaks beyond the last line's own.
	for range trailing - 1 {
		e.buf.WriteByte('\n')
	}
}

// writeLiteralHeader writes a literal block's header: "|", the indentation
// indicator when the first line cannot show the indentation, and the
// chomping indicator for trailing line breaks.
func (e *encoder) writeLiteralHeader(s string, trailing int, at place) {
	e.separate(at)
	e.buf.WriteByte('|')
	//: an indentation indicator, when the first line cannot show it.
	if needsIndentIndicator(s) {
		e.buf.WriteByte(literalIndicator)
	}
	switch {
	//: no trailing break: strip.
	case trailing == 0:
		e.buf.WriteByte('-')
	//: more than one: keep.
	case trailing > 1:
		e.buf.WriteByte('+')
	}
	e.buf.WriteByte('\n')
}

// needsIndentIndicator reports whether a literal block of s needs an
// indentation indicator: its first line starts with a blank or is empty, so
// the reader cannot detect the indentation from it.
func needsIndentIndicator(s string) bool {
	//: a space, a tab or a line break first.
	return s != "" && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n')
}

// literalSafe reports whether every reader reads s back from a literal block:
// it holds a line with content, and no character a block cannot carry
// verbatim — a carriage return, a control character, a byte order mark, or a
// character YAML 1.1 reads as a line break.
func literalSafe(s string) bool {
	//: only line breaks: nothing for the block to hold.
	if strings.TrimRight(s, "\n") == "" {
		//: quoted instead.
		return false
	}
	//: every character a block carries.
	return !strings.ContainsFunc(s, func(r rune) bool { return r != '\n' && r != '\t' && !printable(r) })
}
