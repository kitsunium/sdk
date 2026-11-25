package frozen

import "iter"

// Integer is the constraint on an integer key or value: any integer type,
// held in the image at its own width.
type Integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// Map is a read-only map from integer keys to integer values, built by
// NewMap or loaded by LoadMap. Each key and its value are one record of the
// image, so a present key costs one read past its tag. The zero value is an
// empty Map. Safe for concurrent use.
type Map[K, V Integer] struct {
	t table
}

// mapShape is the shape of a Map[K, V].
func mapShape[K, V Integer]() shape {
	kw, ks := widthOf[K]()
	vw, vs := widthOf[V]()
	return shape{kind: kindInts, kw: kw, vw: vw, signs: signs(ks, vs)}
}

// NewMap builds a Map holding values[i] under keys[i]. It refuses keys and
// values of different lengths (LengthMismatch), a key given twice
// (DuplicateKey, naming the second index), and more keys than an image
// addresses (TooLarge). Neither slice is kept.
func NewMap[K, V Integer](keys []K, values []V) (*Map[K, V], error) {
	if err := lengths(len(keys), len(values)); err != nil {
		return nil, err
	}
	s := mapShape[K, V]()
	kmask := mask(s.kw)
	t, err := build(s, keySet{
		n:    len(keys),
		hash: func(seed uint64, i int) uint64 { return hashInt(seed, uint64(keys[i])&kmask) },
		same: func(i, j int) bool { return keys[i] == keys[j] },
		write: func(i int, rec, _ []byte, _ uint32) uint32 {
			storew(rec, s.kw, uint64(keys[i]))
			storew(rec[s.kw:], s.vw, uint64(values[i]))
			return 0
		},
	})
	if err != nil {
		return nil, err
	}
	return &Map[K, V]{t: t}, nil
}

// LoadMap reads image as a Map[K, V], in place and verified under check: the
// map reads image for as long as it lives, so the caller keeps it alive and
// unchanged. It refuses bytes that are not an image (ImageCorrupt), a
// layout of another version (ImageVersion), and an image of other key or
// value types (ImageShape).
func LoadMap[K, V Integer](image []byte, check Check) (*Map[K, V], error) {
	t, err := load(image, mapShape[K, V](), check)
	if err != nil {
		return nil, err
	}
	return &Map[K, V]{t: t}, nil
}

// Get returns the value held under key and whether the map holds key.
func (m *Map[K, V]) Get(key K) (V, bool) {
	r, ok := m.t.findInt(uint64(key))
	if !ok {
		return 0, false
	}
	return V(m.t.intValue(r)), true
}

// Len reports how many keys the map holds.
func (m *Map[K, V]) Len() int { return int(m.t.n) }

// All yields every key and its value in slot order, which is the image's
// and no other.
func (m *Map[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for x := range m.t.n {
			r := uint64(x) * uint64(m.t.stride)
			if !yield(K(m.t.intKey(r)), V(m.t.intValue(r))) {
				return
			}
		}
	}
}

// AppendBinary appends the map's image to b.
func (m *Map[K, V]) AppendBinary(b []byte) ([]byte, error) {
	return m.t.appendTo(b, mapShape[K, V]()), nil
}

// MarshalBinary returns a copy of the map's image.
func (m *Map[K, V]) MarshalBinary() ([]byte, error) { return m.t.appendTo(nil, mapShape[K, V]()), nil }

// StringMap is a read-only map from string keys to integer values — a
// symbol table — built by NewStringMap or loaded by LoadStringMap. The zero
// value is an empty StringMap. Safe for concurrent use.
type StringMap[V Integer] struct {
	t table
}

// stringMapShape is the shape of a StringMap[V].
func stringMapShape[V Integer]() shape {
	vw, vs := widthOf[V]()
	return shape{kind: kindStrings, vw: vw, signs: signs(false, vs)}
}

// NewStringMap builds a StringMap holding values[i] under keys[i], refusing
// what NewMap refuses; the keys' bytes are copied into the image, and the
// slices are not kept.
func NewStringMap[V Integer](keys []string, values []V) (*StringMap[V], error) {
	if err := lengths(len(keys), len(values)); err != nil {
		return nil, err
	}
	s := stringMapShape[V]()
	t, err := build(s, keySet{
		n: len(keys), blob: totalLen(keys, nil) + uint64(len(keys))*uint64(s.head()+uint32(s.vw)),
		hash: func(seed uint64, i int) uint64 { return hashString(seed, keys[i]) },
		same: func(i, j int) bool { return keys[i] == keys[j] },
		write: func(i int, rec, blob []byte, off uint32) uint32 {
			byteOrder.PutUint32(rec, off)
			at := head(blob, keys[i], s.head())
			storew(blob[at:], s.vw, uint64(values[i]))
			return at + uint32(s.vw)
		},
	})
	if err != nil {
		return nil, err
	}
	return &StringMap[V]{t: t}, nil
}

// LoadStringMap reads image as a StringMap[V], in place and verified under
// check, on LoadMap's terms.
func LoadStringMap[V Integer](image []byte, check Check) (*StringMap[V], error) {
	t, err := load(image, stringMapShape[V](), check)
	if err != nil {
		return nil, err
	}
	return &StringMap[V]{t: t}, nil
}

// Get returns the value held under key and whether the map holds key.
func (m *StringMap[V]) Get(key string) (V, bool) {
	off, ok := m.t.findString(key)
	if !ok {
		return 0, false
	}
	return V(m.t.entryValue(off)), true
}

// Len reports how many keys the map holds.
func (m *StringMap[V]) Len() int { return int(m.t.n) }

// All yields every key and its value in slot order, which is the image's
// and no other.
func (m *StringMap[V]) All() iter.Seq2[string, V] {
	return func(yield func(string, V) bool) {
		for x := range m.t.n {
			off := m.t.entry(x)
			if !yield(m.t.stringKey(off), V(m.t.entryValue(off))) {
				return
			}
		}
	}
}

// AppendBinary appends the map's image to b.
func (m *StringMap[V]) AppendBinary(b []byte) ([]byte, error) {
	return m.t.appendTo(b, stringMapShape[V]()), nil
}

// MarshalBinary returns a copy of the map's image.
func (m *StringMap[V]) MarshalBinary() ([]byte, error) {
	return m.t.appendTo(nil, stringMapShape[V]()), nil
}

// Dict is a read-only map from string keys to string values, built by
// NewDict or loaded by LoadDict. The zero value is an empty Dict. Safe for
// concurrent use.
type Dict struct {
	t table
}

// dictShape is the shape of a Dict.
var dictShape = shape{kind: kindText}

// NewDict builds a Dict holding values[i] under keys[i], refusing what
// NewMap refuses; the keys' and values' bytes are copied into the image.
func NewDict(keys, values []string) (*Dict, error) {
	if err := lengths(len(keys), len(values)); err != nil {
		return nil, err
	}
	t, err := build(dictShape, keySet{
		n: len(keys), blob: totalLen(keys, values) + uint64(len(keys))*uint64(dictShape.head()),
		hash: func(seed uint64, i int) uint64 { return hashString(seed, keys[i]) },
		same: func(i, j int) bool { return keys[i] == keys[j] },
		write: func(i int, rec, blob []byte, off uint32) uint32 {
			byteOrder.PutUint32(rec, off)
			byteOrder.PutUint32(blob[width4:], uint32(len(values[i])))
			at := head(blob, keys[i], dictShape.head())
			return at + uint32(copy(blob[at:], values[i]))
		},
	})
	if err != nil {
		return nil, err
	}
	return &Dict{t: t}, nil
}

// LoadDict reads image as a Dict, in place and verified under check, on
// LoadMap's terms.
func LoadDict(image []byte, check Check) (*Dict, error) {
	t, err := load(image, dictShape, check)
	if err != nil {
		return nil, err
	}
	return &Dict{t: t}, nil
}

// Get returns the value held under key and whether the dict holds key. The
// value is a view of the image: it costs no allocation.
func (d *Dict) Get(key string) (string, bool) {
	r, ok := d.t.findString(key)
	if !ok {
		return "", false
	}
	return d.t.textValue(r), true
}

// Len reports how many keys the dict holds.
func (d *Dict) Len() int { return int(d.t.n) }

// All yields every key and its value in slot order, which is the image's
// and no other.
func (d *Dict) All() iter.Seq2[string, string] {
	return func(yield func(string, string) bool) {
		for x := range d.t.n {
			off := d.t.entry(x)
			if !yield(d.t.stringKey(off), d.t.textValue(off)) {
				return
			}
		}
	}
}

// AppendBinary appends the dict's image to b.
func (d *Dict) AppendBinary(b []byte) ([]byte, error) { return d.t.appendTo(b, dictShape), nil }

// MarshalBinary returns a copy of the dict's image.
func (d *Dict) MarshalBinary() ([]byte, error) { return d.t.appendTo(nil, dictShape), nil }

// totalLen is the bytes of every key and value, in 64 bits so the sum of a
// huge input does not wrap.
func totalLen(keys, values []string) uint64 {
	var n uint64
	for _, k := range keys {
		n += uint64(len(k))
	}
	for _, v := range values {
		n += uint64(len(v))
	}
	return n
}

// appendTo appends t's image to b; the zero table's is the image of an empty
// table of shape s.
func (t *table) appendTo(b []byte, s shape) []byte {
	if t.image == nil {
		e := make([]byte, headerSize)
		empty := new(table)
		empty.shape = s
		writeHeader(e, empty)
		return append(b, e...)
	}
	return append(b, t.image...)
}

// head writes a string entry's key length and bytes at blob, returning the
// offset past the key.
func head(blob []byte, key string, hs uint32) uint32 {
	byteOrder.PutUint32(blob, uint32(len(key)))
	return hs + uint32(copy(blob[hs:], key))
}
