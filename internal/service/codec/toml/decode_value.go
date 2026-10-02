// Package toml — the typed half of the decode: tables into structs and maps,
// arrays into slices and arrays, through reflection.
package toml

import (
	"encoding"
	"reflect"
	"strconv"
)

// value decodes node n into v.
func (d *decoder) value(n int32, v reflect.Value) error {
	//: pointers are followed, and allocated when nil.
	for v.Kind() == reflect.Pointer {
		//: a nil pointer gets a fresh value.
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		v = v.Elem()
	}
	//: an interface takes the untyped value.
	if v.Kind() == reflect.Interface {
		//: stored, or refused when the interface cannot hold it.
		return d.intoInterface(n, v)
	}
	switch d.p.nodes[n].kind {
	//: a table.
	case kindTable:
		return d.table(n, v)
	//: an array of either kind.
	case kindArray, kindArrayOfTables:
		return d.array(n, v)
	//: a scalar.
	default:
		return d.scalar(n, v)
	}
}

// intoInterface stores node n into the interface v: the untyped value, merged
// into a map v already holds when n is a table.
func (d *decoder) intoInterface(n int32, v reflect.Value) error {
	var current any
	//: the value the interface already holds, if any.
	if !v.IsNil() {
		current = v.Elem().Interface()
	}
	var decoded any
	//: a table merges into a map already there.
	if d.p.nodes[n].kind == kindTable {
		decoded = d.anyInto(n, current)
	} else {
		decoded = d.anyValue(n)
	}
	dv := reflect.ValueOf(decoded)
	//: an interface with methods holds only a value that has them.
	if !dv.Type().AssignableTo(v.Type()) {
		//: refused.
		return d.fail(n, problemMismatch, v.Type())
	}
	v.Set(dv)
	//: stored.
	return nil
}

// table decodes table n into v, a struct or a map.
func (d *decoder) table(n int32, v reflect.Value) error {
	switch v.Kind() {
	//: a struct: each key to its field.
	case reflect.Struct:
		return d.structFields(n, v)
	//: a map: each key to an entry.
	case reflect.Map:
		return d.mapEntries(n, v)
	//: nothing else holds a table.
	default:
		return d.fail(n, problemMismatch, v.Type())
	}
}

// structFields decodes each key of table n into the field of struct v it
// names. A key no field names is ignored.
func (d *decoder) structFields(n int32, v reflect.Value) error {
	info := infoOf(v.Type())
	nodes := d.p.nodes
	//: each key, in document order.
	for c := nodes[n].first; c != noNode; c = nodes[c].next {
		f, ok := info.lookup(d.p.keyBytes(&nodes[c]))
		//: an unknown key.
		if !ok {
			continue
		}
		fv, err := d.field(c, v, f.index)
		//: an embedded pointer that cannot be allocated.
		if err != nil {
			//: refused.
			return err
		}
		//: the value into the field.
		if err := d.value(c, fv); err != nil {
			//: refused.
			return err
		}
	}
	//: decoded.
	return nil
}

// field returns the field of v at index, allocating the embedded pointers on
// the way.
func (d *decoder) field(c int32, v reflect.Value, index []int) (reflect.Value, error) {
	//: each step of the path.
	for i, step := range index {
		//: an embedded pointer is followed, and allocated when nil.
		if i > 0 && v.Kind() == reflect.Pointer {
			//: a nil pointer needs a value, which reflect allocates only when it may.
			if v.IsNil() {
				//: an unexported embedded type cannot be set.
				if !v.CanSet() {
					//: refused.
					return reflect.Value{}, d.fail(c, problemEmbedded, v.Type())
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(step)
	}
	//: the field.
	return v, nil
}

// mapEntries decodes each key of table n into an entry of map v. An entry
// already there is decoded into, so a table merges into the map it meets.
func (d *decoder) mapEntries(n int32, v reflect.Value) error {
	t := v.Type()
	//: a nil map is created at the table's size.
	if v.IsNil() {
		v.Set(reflect.MakeMapWithSize(t, int(d.p.nodes[n].count)))
	}
	key := reflect.New(t.Key()).Elem()
	elem := reflect.New(t.Elem()).Elem()
	nodes := d.p.nodes
	//: each key, in document order.
	for c := nodes[n].first; c != noNode; c = nodes[c].next {
		//: the key, converted to the map's key type.
		if err := d.mapKey(c, key); err != nil {
			//: refused.
			return err
		}
		elem.SetZero()
		//: an existing entry is the starting point.
		if existing := v.MapIndex(key); existing.IsValid() {
			elem.Set(existing)
		}
		//: the value into the entry.
		if err := d.value(c, elem); err != nil {
			//: refused.
			return err
		}
		v.SetMapIndex(key, elem)
	}
	//: decoded.
	return nil
}

// mapKey converts the key of node c into key, a value of the map's key type:
// a string kind, an integer or a float spelled in decimal, or a type whose
// pointer implements encoding.TextUnmarshaler.
func (d *decoder) mapKey(c int32, key reflect.Value) error {
	raw := d.p.keyBytes(&d.p.nodes[c])
	//: a type that reads its own text.
	if infoOf(key.Type()).is(typeTextUnmarshaler) {
		//: through UnmarshalText.
		return d.unmarshalText(c, key, raw)
	}
	text := d.p.internKey(raw)
	var ok bool
	switch key.Kind() {
	//: the common case.
	case reflect.String:
		key.SetString(text)
		ok = true
	//: a signed integer key.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		ok = setIntKey(key, text)
	//: an unsigned integer key.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		ok = setUintKey(key, text)
	//: a float key.
	case reflect.Float32, reflect.Float64:
		ok = setFloatKey(key, text)
	//: no other key type.
	default:
	}
	//: a key the type cannot hold.
	if !ok {
		//: refused.
		return d.fail(c, problemMapKey, key.Type())
	}
	//: converted.
	return nil
}

// setIntKey parses raw as a decimal integer into the signed key.
func setIntKey(key reflect.Value, raw string) bool {
	i, err := strconv.ParseInt(raw, int(decimalBase), 64)
	//: malformed, or too large for the key type.
	if err != nil || key.OverflowInt(i) {
		//: refused.
		return false
	}
	key.SetInt(i)
	//: converted.
	return true
}

// setUintKey parses raw as a decimal integer into the unsigned key.
func setUintKey(key reflect.Value, raw string) bool {
	u, err := strconv.ParseUint(raw, int(decimalBase), 64)
	//: malformed, or too large for the key type.
	if err != nil || key.OverflowUint(u) {
		//: refused.
		return false
	}
	key.SetUint(u)
	//: converted.
	return true
}

// setFloatKey parses raw as a float into the float key.
func setFloatKey(key reflect.Value, raw string) bool {
	f, err := strconv.ParseFloat(raw, key.Type().Bits())
	//: malformed.
	if err != nil {
		//: refused.
		return false
	}
	key.SetFloat(f)
	//: converted.
	return true
}

// unmarshalText hands raw to the UnmarshalText of v, which is addressable.
func (d *decoder) unmarshalText(c int32, v reflect.Value, raw []byte) error {
	u, ok := reflect.TypeAssert[encoding.TextUnmarshaler](v.Addr())
	//: describe found the method; the assertion only fails for an
	//: unaddressable value, which the decoder never passes.
	if !ok {
		//: refused.
		return d.fail(c, problemMismatch, v.Type())
	}
	//: the type's own reading of the text.
	if err := u.UnmarshalText(raw); err != nil {
		//: refused, the type's error kept underneath.
		return d.wrapFail(c, err, v.Type())
	}
	//: read.
	return nil
}

// array decodes array n, static or of tables, into v: a slice, which is
// replaced, or an array, filled from the start and zeroed past the document's
// elements, which beyond its length are dropped.
func (d *decoder) array(n int32, v reflect.Value) error {
	count := int(d.p.nodes[n].count)
	switch v.Kind() {
	//: a slice of exactly the document's length.
	case reflect.Slice:
		slice := reflect.MakeSlice(v.Type(), count, count)
		//: each element.
		if err := d.elements(n, slice); err != nil {
			//: refused.
			return err
		}
		v.Set(slice)
		//: decoded.
		return nil
	//: a Go array of fixed length.
	case reflect.Array:
		v.SetZero()
		//: each element that fits.
		return d.elements(n, v)
	//: nothing else holds an array.
	default:
		return d.fail(n, problemMismatch, v.Type())
	}
}

// elements decodes the elements of array n into the first elements of v.
func (d *decoder) elements(n int32, v reflect.Value) error {
	limit := v.Len()
	i := 0
	nodes := d.p.nodes
	//: each element, until v is full.
	for c := nodes[n].first; c != noNode && i < limit; c = nodes[c].next {
		//: the element into its slot.
		if err := d.value(c, v.Index(i)); err != nil {
			//: refused.
			return err
		}
		i++
	}
	//: decoded.
	return nil
}
