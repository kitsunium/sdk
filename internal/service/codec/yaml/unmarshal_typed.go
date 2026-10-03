// Package yaml — decoding into typed targets, by reflection.
package yaml

import (
	"encoding"
	"math"
	"reflect"
	"strconv"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// timeLayouts are the forms a time.Time is read from: RFC 3339 — the form
// the encoder writes — then the timestamp forms yaml.v3 accepted, so a
// configuration that read a date before still reads it.
var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-1-2T15:4:5.999999999Z07:00",
	"2006-1-2t15:4:5.999999999Z07:00",
	"2006-1-2 15:4:5.999999999",
	"2006-1-2",
}

// mapDecoding is the reusable state of one map being decoded: a key and an
// element value set and copied into the map per entry, and the keys seen when
// two texts may decode to one key.
type mapDecoding struct {
	// seen holds the keys decoded so far, for a key type that is not a string.
	seen map[any]bool
	// key is the key of the entry being decoded.
	key reflect.Value
	// elem is the value of the entry being decoded.
	elem reflect.Value
	// textKeys says a key is its text: a string kind with no text hook.
	textKeys bool
}

// mayHaveHooks reports whether t can carry a method: a predeclared scalar
// type and an unnamed slice, map or pointer cannot, which spares the hooks
// cache a lookup for nearly every value of a document.
func mayHaveHooks(t reflect.Type) bool {
	//: a defined type, or a struct that may promote methods.
	return t.PkgPath() != "" || t.Kind() == reflect.Struct
}

// decode decodes node i into v, which is settable.
func (d *decoder) decode(i int32, v reflect.Value) error {
	current := d.node(i)
	//: null.
	if current.isNull() {
		//: a pointer, map, slice or interface becomes nil; anything else is left.
		decodeNull(v)
		//: decoded.
		return nil
	}
	v = allocateThrough(v)
	//: a hook, when the type can have one.
	if mayHaveHooks(v.Type()) && v.CanAddr() {
		//: the hook decided.
		if handled, err := d.decodeHooked(i, current, v); handled {
			//: its result.
			return err
		}
	}
	//: by kind.
	return d.decodeKind(i, current, v)
}

// allocateThrough follows v's pointers, allocating the nil ones, and returns
// the value they lead to.
func allocateThrough(v reflect.Value) reflect.Value {
	//: through pointers.
	for v.Kind() == reflect.Pointer {
		//: a nil pointer gets a value to point at.
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		v = v.Elem()
	}
	//: the value.
	return v
}

// decodeKind decodes node i into v by v's kind.
func (d *decoder) decodeKind(i int32, n *node, v reflect.Value) error {
	switch v.Kind() {
	//: an interface.
	case reflect.Interface:
		//: whatever the node is.
		return d.decodeInterface(i, n, v)
	//: a map.
	case reflect.Map:
		//: from a mapping.
		return d.decodeMap(n, v)
	//: a struct.
	case reflect.Struct:
		//: from a mapping.
		return d.decodeStruct(n, v)
	//: a slice.
	case reflect.Slice:
		//: from a sequence.
		return d.decodeSlice(n, v)
	//: an array.
	case reflect.Array:
		//: from a sequence of its length.
		return d.decodeArray(n, v)
	//: a scalar kind.
	default:
		//: from a scalar.
		return d.decodeScalar(n, v)
	}
}

// decodeNull applies null to v: nil for a pointer, a map, a slice or an
// interface; nothing for any other kind, as encoding/json and yaml.v3 do.
func decodeNull(v reflect.Value) {
	//: the kinds with a nil.
	if nullable(v.Kind()) {
		v.SetZero()
	}
}

// nullable reports whether a value of kind k can be nil.
func nullable(k reflect.Kind) bool {
	switch k {
	//: the kinds with a nil.
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		//: yes.
		return true
	//: every other.
	default:
		//: no.
		return false
	}
}

// decodeHooked decodes node i through a hook of v's type when it has one,
// reporting whether it did: UnmarshalYAML for any node; for a scalar, a time,
// UnmarshalText, a duration. yaml.v3's UnmarshalYAML(*yaml.Node) is refused
// rather than skipped.
func (d *decoder) decodeHooked(i int32, n *node, v reflect.Value) (bool, error) {
	t := v.Type()
	h := hooksOf(t)
	switch {
	//: UnmarshalYAML(func(any) error).
	case h.has(hookUnmarshaler):
		//: the hook decodes.
		return true, d.callUnmarshaler(i, n, v)
	//: yaml.v3's UnmarshalYAML(*yaml.Node), which names a yaml.v3 type.
	case h.has(hookForeignUnmarshaler):
		//: refused rather than silently decoded by reflection.
		return true, d.mismatch(n, "the type's UnmarshalYAML is not UnmarshalYAML(func(any) error)", t)
	//: the text hooks read scalars only.
	case n.kind != kindScalar:
		//: no hook.
		return false, nil
	}
	//: a scalar's hooks.
	return d.decodeScalarHooked(n, v, h)
}

// decodeScalarHooked decodes the scalar n through a time, UnmarshalText or a
// duration, reporting whether v's type has one of them.
func (d *decoder) decodeScalarHooked(n *node, v reflect.Value, h typeHooks) (bool, error) {
	switch t := v.Type(); {
	//: a time, from the forms yaml.v3 read.
	case t == timeType:
		//: parsed.
		return true, d.decodeTime(n, v)
	//: UnmarshalText.
	case h.has(hookTextUnmarshaler):
		//: the hook reads the text.
		return true, d.callUnmarshalText(n, v)
	//: a duration, from its text.
	case t == durationType:
		//: parsed.
		return true, d.decodeDuration(n, v)
	//: no hook.
	default:
		//: by kind.
		return false, nil
	}
}

// callUnmarshaler calls v's UnmarshalYAML with a function decoding node i.
func (d *decoder) callUnmarshaler(i int32, n *node, v reflect.Value) error {
	//: a hook that decodes into itself forever.
	if d.hooks >= maxDepth {
		//: refused.
		return d.mismatch(n, "UnmarshalYAML hooks nest deeper than the decoder accepts", v.Type())
	}
	u, ok := unmarshalYAMLHook(v.Addr())
	//: hooksOf said it implements the hook.
	if !ok {
		//: by kind after all.
		return d.decodeKind(i, n, v)
	}
	d.hooks++
	defer func() { d.hooks-- }()
	//: the hook failed.
	if err := u.UnmarshalYAML(func(target any) error { return d.decodeHookTarget(i, n, target) }); err != nil {
		//: wrapped, with its position.
		return d.hookFailed(n, err, v.Type())
	}
	//: decoded.
	return nil
}

// decodeHookTarget decodes node i into the target an UnmarshalYAML hook
// passed its function.
func (d *decoder) decodeHookTarget(i int32, n *node, target any) error {
	value := reflect.ValueOf(target)
	//: the function needs somewhere to write.
	if value.Kind() != reflect.Pointer || value.IsNil() {
		//: refused.
		return d.mismatch(n, "UnmarshalYAML's function needs a non-nil pointer", nil)
	}
	//: the node, into the hook's target.
	return d.decode(i, value.Elem())
}

// callUnmarshalText calls v's UnmarshalText with the scalar n's text.
func (d *decoder) callUnmarshalText(n *node, v reflect.Value) error {
	u, ok := reflect.TypeAssert[encoding.TextUnmarshaler](v.Addr())
	//: hooksOf said it implements the hook.
	if !ok {
		//: by kind after all.
		return d.decodeScalar(n, v)
	}
	//: the hook refused the text.
	if err := u.UnmarshalText([]byte(n.value)); err != nil {
		//: wrapped, with its position.
		return d.hookFailed(n, err, v.Type())
	}
	//: decoded.
	return nil
}

// hookFailed wraps the error a type's own hook returned. A cause that is
// already an SDK error keeps its code; any other becomes UnmarshalFailed.
func (d *decoder) hookFailed(n *node, cause error, t reflect.Type) error {
	//: the hook's error, located.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeYAMLUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "YAML decoding failed",
		Private: "service/codec/yaml: a type's own decoding hook returned an error",
	}, append(at(int(n.line), d.p.columnOf(int(n.off))), errs.String("type", t.String()))...)
}

// decodeTime reads a time.Time from a scalar: RFC 3339, or one of the
// timestamp forms yaml.v3 read.
func (d *decoder) decodeTime(n *node, v reflect.Value) error {
	//: RFC 3339 first, then the others.
	for _, layout := range timeLayouts {
		//: this one.
		if parsed, err := time.Parse(layout, n.value); err == nil {
			v.Set(reflect.ValueOf(parsed))
			//: decoded.
			return nil
		}
	}
	//: none.
	return d.mismatch(n, "a time is written in RFC 3339, as in 2006-01-02T15:04:05Z", v.Type())
}

// decodeDuration reads a time.Duration from its text. A bare number is
// refused, as yaml.v3 refused it: its unit would be a guess.
func (d *decoder) decodeDuration(n *node, v reflect.Value) error {
	parsed, err := time.ParseDuration(n.value)
	//: not a duration with its unit.
	if err != nil {
		//: refused.
		return d.mismatch(n, "a duration is written with its unit, as in 30s, 15m or 24h", v.Type())
	}
	v.SetInt(int64(parsed))
	//: decoded.
	return nil
}

// decodeInterface decodes node i into the interface v: the untyped value for
// an empty interface, or into what a non-empty one points at.
func (d *decoder) decodeInterface(i int32, n *node, v reflect.Value) error {
	//: a non-empty interface.
	if v.NumMethod() != 0 {
		//: into what it points at, if anything.
		return d.decodeIntoHeld(i, n, v)
	}
	value, err := d.toAny(i)
	//: refused.
	if err != nil {
		return err
	}
	//: nil stays the zero interface.
	if value == nil {
		v.SetZero()
		//: decoded.
		return nil
	}
	v.Set(reflect.ValueOf(value))
	//: decoded.
	return nil
}

// decodeIntoHeld decodes node i into the pointer a non-empty interface holds;
// with nothing held there is nothing to decode into.
func (d *decoder) decodeIntoHeld(i int32, n *node, v reflect.Value) error {
	//: a non-nil pointer held.
	if !v.IsNil() && v.Elem().Kind() == reflect.Pointer && !v.Elem().IsNil() {
		//: the pointed-at value.
		return d.decode(i, v.Elem())
	}
	//: nothing to decode into.
	return d.mismatch(n, describe(n)+" cannot be decoded into a non-empty interface", v.Type())
}

// decodeMap decodes the mapping n into the map v, merging into an existing
// map. Two keys that decode to one Go key are refused, as two keys that read
// the same are.
func (d *decoder) decodeMap(n *node, v reflect.Value) error {
	t := v.Type()
	//: only a mapping fills a map.
	if n.kind != kindMapping {
		//: refused.
		return d.mismatch(n, describe(n)+" cannot be decoded into a map", t)
	}
	//: a nil map is made.
	if v.IsNil() {
		v.Set(reflect.MakeMapWithSize(t, int(n.count/2)))
	}
	m := mapDecoding{
		key:      reflect.New(t.Key()).Elem(),
		elem:     reflect.New(t.Elem()).Elem(),
		textKeys: t.Key().Kind() == reflect.String && !hooksOf(t.Key()).has(hookTextUnmarshaler),
	}
	//: every entry.
	for j := int32(0); j < n.count; j += 2 {
		//: its key and value.
		if err := d.decodeMapEntry(n, j, v, &m); err != nil {
			//: refused.
			return err
		}
	}
	//: decoded.
	return nil
}

// decodeMapEntry decodes the j-th key of the mapping n and its value into the
// map v.
func (d *decoder) decodeMapEntry(n *node, j int32, v reflect.Value, m *mapDecoding) error {
	//: the key.
	if err := d.decodeMapKey(d.p.child(n, j), v.Type(), m); err != nil {
		//: refused.
		return err
	}
	m.elem.SetZero()
	//: the value.
	if err := d.decode(d.p.child(n, j+1), m.elem); err != nil {
		//: refused.
		return err
	}
	v.SetMapIndex(m.key, m.elem)
	//: decoded.
	return nil
}

// decodeMapKey decodes the key node keyIndex into m.key: a string key is the
// key's text; a key of another kind is decoded as a scalar and checked
// against the keys before it, since 1 and 0x1 decode to one int.
func (d *decoder) decodeMapKey(keyIndex int32, t reflect.Type, m *mapDecoding) error {
	keyNode := d.node(keyIndex)
	m.key.SetZero()
	//: a string key is the key's text.
	if m.textKeys {
		m.key.SetString(keyNode.value)
		//: decoded.
		return nil
	}
	//: a null key means nothing to a key of a scalar kind.
	if keyNode.isNull() && !nullable(t.Key().Kind()) {
		//: refused.
		return d.mismatch(keyNode, "a null key cannot be decoded into "+t.Key().String(), t)
	}
	//: the key.
	if err := d.decode(keyIndex, m.key); err != nil {
		//: refused.
		return err
	}
	//: decoded twice.
	return d.checkKeyCollision(keyNode, m)
}

// checkKeyCollision refuses a decoded key the mapping has already decoded.
func (d *decoder) checkKeyCollision(keyNode *node, m *mapDecoding) error {
	//: the set of keys this mapping has decoded.
	if m.seen == nil {
		m.seen = map[any]bool{}
	}
	key := m.key.Interface()
	//: seen before.
	if m.seen[key] {
		//: refused at the second occurrence.
		return d.refuse(DuplicateKey, keyNode)
	}
	m.seen[key] = true
	//: new.
	return nil
}

// decodeStruct decodes the mapping n into the struct v: each key into the
// field that declares it, the others into the inline map if there is one, or
// nowhere — as yaml.v3 ignores an unknown key.
func (d *decoder) decodeStruct(n *node, v reflect.Value) error {
	t := v.Type()
	//: only a mapping fills a struct.
	if n.kind != kindMapping {
		//: refused.
		return d.mismatch(n, describe(n)+" cannot be decoded into a struct", t)
	}
	info := structInfoOf(t)
	//: a malformed tag.
	if info.tagError != "" {
		//: refused.
		return d.mismatch(n, info.tagError, t)
	}
	//: every entry.
	for j := int32(0); j < n.count; j += 2 {
		//: into its field, the inline map, or nowhere.
		if err := d.decodeStructEntry(n, j, v, info); err != nil {
			//: refused.
			return err
		}
	}
	//: decoded.
	return nil
}

// decodeStructEntry decodes the j-th entry of the mapping n into the struct v.
func (d *decoder) decodeStructEntry(n *node, j int32, v reflect.Value, info *structInfo) error {
	keyText := d.node(d.p.child(n, j)).value
	valueIndex := d.p.child(n, j+1)
	//: a declared field.
	if f, ok := info.byKey[keyText]; ok {
		//: into the field.
		return d.decode(valueIndex, fieldForWrite(v, info.fields[f].index))
	}
	//: no inline map: an unknown key is ignored.
	if info.inlineMap == nil {
		//: nowhere.
		return nil
	}
	//: the inline map collects it.
	return d.decodeInlineEntry(keyText, valueIndex, fieldForWrite(v, info.inlineMap))
}

// decodeInlineEntry decodes one entry into the inline map m.
func (d *decoder) decodeInlineEntry(key string, valueIndex int32, m reflect.Value) error {
	//: a nil map is made.
	if m.IsNil() {
		m.Set(reflect.MakeMap(m.Type()))
	}
	elem := reflect.New(m.Type().Elem()).Elem()
	//: the value.
	if err := d.decode(valueIndex, elem); err != nil {
		//: refused.
		return err
	}
	m.SetMapIndex(reflect.ValueOf(key).Convert(m.Type().Key()), elem)
	//: decoded.
	return nil
}

// fieldForWrite returns the field at index in v, allocating the nil pointers
// of inlined structs on the way.
func fieldForWrite(v reflect.Value, index []int) reflect.Value {
	//: one step per index.
	for k, i := range index {
		//: an inlined pointer to a struct, allocated when nil.
		if k > 0 && v.Kind() == reflect.Pointer {
			v = allocateThrough(v)
		}
		v = v.Field(i)
	}
	//: the field.
	return v
}

// decodeSlice decodes the sequence n into the slice v, replacing it.
func (d *decoder) decodeSlice(n *node, v reflect.Value) error {
	t := v.Type()
	//: only a sequence fills a slice.
	if n.kind != kindSequence {
		//: refused.
		return d.mismatch(n, describe(n)+" cannot be decoded into a slice", t)
	}
	//: an empty sequence is an empty slice, not a nil one, as in yaml.v3.
	if n.count == 0 {
		v.Set(reflect.MakeSlice(t, 0, 0))
		//: decoded.
		return nil
	}
	//: a new backing array, grown in place: reflect.MakeSlice would box a
	//: second allocation for the slice header.
	v.SetZero()
	v.Grow(int(n.count))
	v.SetLen(int(n.count))
	//: every entry.
	return d.decodeEntries(n, v)
}

// decodeArray decodes the sequence n into the array v, whose length it must
// match exactly.
func (d *decoder) decodeArray(n *node, v reflect.Value) error {
	//: only a sequence of the same length fills an array.
	if n.kind != kindSequence || int(n.count) != v.Len() {
		//: refused.
		return d.mismatch(n, describe(n)+" cannot fill an array of "+strconv.Itoa(v.Len())+" elements", v.Type())
	}
	v.SetZero()
	//: every entry.
	return d.decodeEntries(n, v)
}

// decodeEntries decodes the sequence n's entries into v's elements.
func (d *decoder) decodeEntries(n *node, v reflect.Value) error {
	//: every entry.
	for j := range n.count {
		//: into its element.
		if err := d.decode(d.p.child(n, j), v.Index(int(j))); err != nil {
			//: refused.
			return err
		}
	}
	//: decoded.
	return nil
}

// decodeScalar decodes the scalar n into v, whose kind is a string, a
// boolean or a number. A string takes any scalar's text as written; a
// boolean or a number takes only a plain scalar the core schema reads as one.
func (d *decoder) decodeScalar(n *node, v reflect.Value) error {
	t := v.Type()
	//: a collection into a scalar.
	if n.kind != kindScalar {
		//: refused.
		return d.mismatch(n, describe(n)+" cannot be decoded into "+t.Kind().String(), t)
	}
	//: the text, as written: 8080 into a string is "8080".
	if v.Kind() == reflect.String {
		v.SetString(n.value)
		//: decoded.
		return nil
	}
	//: a quoted scalar is a string.
	if n.style != stylePlain {
		//: refused.
		return d.mismatch(n, "a quoted string cannot be decoded into "+t.Kind().String(), t)
	}
	//: by the core schema.
	return d.decodeResolved(n, resolvePlain(n.value), v)
}

// decodeResolved decodes a plain scalar's core-schema reading into v.
func (d *decoder) decodeResolved(n *node, r scalarValue, v reflect.Value) error {
	switch v.Kind() {
	//: a boolean.
	case reflect.Bool:
		//: from true or false.
		return d.decodeBool(n, r, v)
	//: a signed integer.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		//: in range.
		return d.decodeInt(n, r, v)
	//: an unsigned integer.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		//: in range.
		return d.decodeUint(n, r, v)
	//: a float.
	case reflect.Float32, reflect.Float64:
		//: in range.
		return d.decodeFloat(n, r, v)
	//: a kind YAML has no value for.
	default:
		//: refused.
		return d.mismatch(n, "the target's kind cannot hold a YAML value", v.Type())
	}
}

// decodeBool decodes a boolean: true or false, never YAML 1.1's yes/no/on/off.
func (d *decoder) decodeBool(n *node, r scalarValue, v reflect.Value) error {
	//: true or false.
	if r.kind == resolvedBool {
		v.SetBool(r.b)
		//: decoded.
		return nil
	}
	//: a YAML 1.1 boolean, which YAML 1.2 reads as a string.
	if isYAML11Bool(n.value) {
		//: refused, saying what to write.
		return d.mismatch(n, "yes, no, on and off are strings in YAML 1.2: write true or false", v.Type())
	}
	//: anything else.
	return d.mismatch(n, describe(n)+" cannot be decoded into bool", v.Type())
}

// decodeInt decodes a signed integer, refusing a value its type cannot hold.
func (d *decoder) decodeInt(n *node, r scalarValue, v reflect.Value) error {
	i, err := d.intValue(n, r, v.Type())
	//: not an integer.
	if err != nil {
		//: refused.
		return err
	}
	//: past the target's width.
	if v.OverflowInt(i) {
		//: refused.
		return d.mismatch(n, "the integer is out of the target's range", v.Type())
	}
	v.SetInt(i)
	//: decoded.
	return nil
}

// intValue reads a plain scalar's reading as an int64: an integer, or a float
// that is a whole number held exactly.
func (d *decoder) intValue(n *node, r scalarValue, t reflect.Type) (int64, error) {
	switch r.kind {
	//: an integer.
	case resolvedInt:
		//: its value.
		return r.i, nil
	//: an integral float.
	case resolvedFloat:
		i, ok := wholeInt(r.f)
		//: a fraction, an infinity, or past int64.
		if !ok {
			//: refused.
			return 0, d.mismatch(n, "a float that is not a whole number cannot be decoded into an integer", t)
		}
		//: its value.
		return i, nil
	//: past int64.
	case resolvedUint:
		//: refused.
		return 0, d.mismatch(n, "the integer is out of the target's range", t)
	//: 0644, or past 64 bits.
	case resolvedLeadingZero, resolvedOutOfRange:
		//: refused.
		return 0, d.refuseResolved(n, r.kind, t)
	//: anything else.
	default:
		//: refused.
		return 0, d.mismatch(n, describe(n)+" cannot be decoded into an integer", t)
	}
}

// wholeInt returns f as an int64 when it is a whole number within range.
func wholeInt(f float64) (int64, bool) {
	//: a whole number in range.
	if f == math.Trunc(f) && f >= math.MinInt64 && f < math.MaxInt64 {
		//: exact.
		return int64(f), true
	}
	//: not one.
	return 0, false
}

// decodeUint decodes an unsigned integer, refusing a value its type cannot
// hold.
func (d *decoder) decodeUint(n *node, r scalarValue, v reflect.Value) error {
	u, err := d.uintValue(n, r, v.Type())
	//: not an unsigned integer.
	if err != nil {
		//: refused.
		return err
	}
	//: past the target's width.
	if v.OverflowUint(u) {
		//: refused.
		return d.mismatch(n, "the integer is out of the target's range", v.Type())
	}
	v.SetUint(u)
	//: decoded.
	return nil
}

// uintValue reads a plain scalar's reading as a uint64: an integer that is
// not negative, or a float that is such a whole number held exactly.
func (d *decoder) uintValue(n *node, r scalarValue, t reflect.Type) (uint64, error) {
	switch r.kind {
	//: an integer, not negative.
	case resolvedInt:
		//: negative.
		if r.i < 0 {
			//: refused.
			return 0, d.mismatch(n, "a negative integer cannot be decoded into an unsigned integer", t)
		}
		//: its value.
		return uint64(r.i), nil
	//: past int64.
	case resolvedUint:
		//: its value.
		return r.u, nil
	//: an integral float.
	case resolvedFloat:
		u, ok := wholeUint(r.f)
		//: a fraction, negative, an infinity, or past uint64.
		if !ok {
			//: refused.
			return 0, d.mismatch(n, "a float that is not a whole number cannot be decoded into an integer", t)
		}
		//: its value.
		return u, nil
	//: 0644, or past 64 bits.
	case resolvedLeadingZero, resolvedOutOfRange:
		//: refused.
		return 0, d.refuseResolved(n, r.kind, t)
	//: anything else.
	default:
		//: refused.
		return 0, d.mismatch(n, describe(n)+" cannot be decoded into an unsigned integer", t)
	}
}

// wholeUint returns f as a uint64 when it is a whole number within range.
func wholeUint(f float64) (uint64, bool) {
	//: a whole number in range.
	if f == math.Trunc(f) && f >= 0 && f < math.MaxUint64 {
		//: exact.
		return uint64(f), true
	}
	//: not one.
	return 0, false
}

// decodeFloat decodes a float, from a float or an integer.
func (d *decoder) decodeFloat(n *node, r scalarValue, v reflect.Value) error {
	f, err := d.floatValue(n, r, v)
	//: not a number.
	if err != nil {
		//: refused.
		return err
	}
	//: past the target's width.
	if v.OverflowFloat(f) {
		//: refused.
		return d.mismatch(n, "the float is out of the target's range", v.Type())
	}
	v.SetFloat(f)
	//: decoded.
	return nil
}

// floatValue reads a plain scalar's reading as a float for v: a float — a
// float32 parsed at 32 bits, so it is rounded once — or an integer.
func (d *decoder) floatValue(n *node, r scalarValue, v reflect.Value) (float64, error) {
	switch r.kind {
	//: a float.
	case resolvedFloat:
		//: at the target's width.
		return d.floatAt(n, r.f, v)
	//: an integer.
	case resolvedInt:
		//: its value.
		return float64(r.i), nil
	//: an integer past int64.
	case resolvedUint:
		//: its value.
		return float64(r.u), nil
	//: 0644, or past 64 bits.
	case resolvedLeadingZero, resolvedOutOfRange:
		//: refused.
		return 0, d.refuseResolved(n, r.kind, v.Type())
	//: anything else.
	default:
		//: refused.
		return 0, d.mismatch(n, describe(n)+" cannot be decoded into a float", v.Type())
	}
}

// floatAt returns the float f read for v: a finite decimal float32 is parsed
// again at 32 bits, so it is rounded once rather than twice.
func (d *decoder) floatAt(n *node, f float64, v reflect.Value) (float64, error) {
	//: a float64, an infinity, or NaN.
	if v.Kind() != reflect.Float32 || math.IsInf(f, 0) || math.IsNaN(f) {
		//: as read.
		return f, nil
	}
	f32, err := strconv.ParseFloat(n.value, float32Bits)
	//: past float32.
	if err != nil {
		//: refused.
		return 0, d.mismatch(n, "the float is out of the target's range", v.Type())
	}
	//: rounded once.
	return f32, nil
}
