package frozen

import (
	"encoding/binary"
	"hash/crc32"
)

// The image's fixed header, little-endian, its CRC-32C over the bytes before
// headerSealed, where the CRC itself is stored:
//
//	0  magic "KFZN"     12 keys     24 seed       48 body CRC32C
//	4  version          16 buckets  32 blob size  52 reserved, zero
//	6  shape: kind, key width, value width, signs  40 image size  60 header CRC32C
//	10 reserved, zero   20 slots
//
// The sections follow, padded to four bytes: the pilots (a byte a bucket),
// the tags (a byte a key: its hash's low byte, read before anything else so
// an absent key is refused without touching a record), the remap (a uint32
// for each slot past n), the records (n of them, the shape's stride each) and
// the blob. An integer table's record is its key then its value; a string
// table's is the uint32 offset of its entry in the blob: the key's length as
// a uint32, a Dict's value length too, then the key's bytes and the value's.
const (
	headerSize   uint64 = 64
	headerSealed uint64 = 60
	version      uint16 = 1
	magic               = "KFZN"
)

// The widths, in bytes, an integer is stored at: a key, a value, a length or
// an offset.
const (
	width8 byte = 8
	width4 byte = 4
	width2 byte = 2
)

// The kinds of table, the shape's first byte.
const (
	kindInts    byte = 0 // integer keys, integer values
	kindStrings byte = 1 // string keys, integer values
	kindText    byte = 3 // string keys, string values
)

var (
	// castagnoli is CRC32C's table, which the platform's instruction computes.
	castagnoli = crc32.MakeTable(crc32.Castagnoli)
	// le is the image's byte order: every integer of an image is
	// little-endian, whatever the platform's.
	le = binary.LittleEndian
)

// shape is what a table holds: its kind, the widths of its integer keys and
// values in bytes (0 for strings), and whether each is signed.
type shape struct {
	kind   byte
	kw, vw byte
	signs  byte // bit 0: the key is signed, bit 1: the value is
}

// stride is a record's size: an integer key and its value, or a string
// entry's offset.
func (s shape) stride() uint32 {
	if s.kind == kindInts {
		return uint32(s.kw) + uint32(s.vw)
	}
	return uint32(width4)
}

// head is the size of a string entry's head: the key's length and, in a Dict,
// the value's.
func (s shape) head() uint32 {
	if s.kind == kindText {
		return uint32(width4 + width4)
	}
	return uint32(width4)
}

// table is a built or loaded image, read in place: every section is a slice of
// it, so a table holds the same few pointers whatever its size and the
// collector scans none of its bytes.
type table struct {
	image  []byte
	pilots []byte
	tags   []byte
	remap  []byte
	recs   []byte
	blob   string
	seed   uint64
	n      uint32
	nb     uint32
	m      uint32
	stride uint32
	kmask  uint64
	shape  shape
}

// loadw reads a w-byte little-endian integer at b[0:w], zero-extended.
func loadw(b []byte, w byte) uint64 {
	switch w {
	case width8:
		return le.Uint64(b)
	case width4:
		return uint64(le.Uint32(b))
	case width2:
		return uint64(le.Uint16(b))
	default:
		return uint64(b[0])
	}
}

// storew writes the low w bytes of v at b[0:w].
func storew(b []byte, w byte, v uint64) {
	switch w {
	case width8:
		le.PutUint64(b, v)
	case width4:
		le.PutUint32(b, uint32(v))
	case width2:
		le.PutUint16(b, uint16(v))
	default:
		b[0] = byte(v)
	}
}

// mask is the low w bytes set.
func mask(w byte) uint64 {
	if w >= width8 {
		return ^uint64(0)
	}
	return 1<<(uint64(width8)*uint64(w)) - 1
}

// slot is the slot of the key whose hash is h: its bucket's pilot, then the
// remap when the slot falls past n.
func (t *table) slot(h uint64) uint32 {
	x := slotOf(h, t.pilots[below(h, t.nb)], t.m)
	if x >= t.n {
		x = le.Uint32(t.remap[4*uint64(x-t.n):])
	}
	return x
}

// findInt returns the record of integer key k, read as its type's uint64. A
// record's offset is computed in 64 bits: n records of 16 bytes pass 4 GiB
// long before n reaches the slots' 32 bits.
func (t *table) findInt(k uint64) (uint64, bool) {
	if t.n == 0 {
		return 0, false
	}
	k &= t.kmask
	h := hashInt(t.seed, k)
	x := t.slot(h)
	if t.tags[x] != byte(h) {
		return 0, false
	}
	r := uint64(x) * uint64(t.stride)
	return r, loadw(t.recs[r:], t.shape.kw) == k
}

// findString returns the blob offset of string key k's entry.
func (t *table) findString(k string) (uint32, bool) {
	if t.n == 0 {
		return 0, false
	}
	h := hashString(t.seed, k)
	x := t.slot(h)
	if t.tags[x] != byte(h) {
		return 0, false
	}
	off := t.entry(x)
	e := t.blob[off:]
	if load32(e, 0) != uint64(len(k)) {
		return 0, false
	}
	hs := t.shape.head()
	return off, e[hs:hs+uint32(len(k))] == k
}

// intKey is record r's integer key.
func (t *table) intKey(r uint64) uint64 { return loadw(t.recs[r:], t.shape.kw) }

// intValue is record r's integer value, which follows its key.
func (t *table) intValue(r uint64) uint64 {
	return loadw(t.recs[r+uint64(t.shape.kw):], t.shape.vw)
}

// entry is the blob offset of slot x's string entry.
func (t *table) entry(x uint32) uint32 { return le.Uint32(t.recs[4*uint64(x):]) }

// stringKey is the key of the string entry at off.
func (t *table) stringKey(off uint32) string {
	at := off + t.shape.head()
	return t.blob[at : at+uint32(load32(t.blob, int(off)))]
}

// entryValue is the integer value of the string entry at off, after its key.
func (t *table) entryValue(off uint32) uint64 {
	at := int(off + t.shape.head() + uint32(load32(t.blob, int(off))))
	switch t.shape.vw {
	case width8:
		return load64(t.blob, at)
	case width4:
		return load32(t.blob, at)
	case width2:
		return uint64(t.blob[at]) | uint64(t.blob[at+1])<<8
	default:
		return uint64(t.blob[at])
	}
}

// textValue is the string value of the Dict entry at off, after its key.
func (t *table) textValue(off uint32) string {
	at := off + t.shape.head() + uint32(load32(t.blob, int(off)))
	return t.blob[at : at+uint32(load32(t.blob, int(off)+int(width4)))]
}

// layout is where an image's sections start, and its size, computed in 64
// bits so no header can overflow it.
type layout struct {
	tags, remap, recs, blob, size uint64
}

// pad4 rounds x up to a multiple of four.
func pad4(x uint64) uint64 { return (x + 3) &^ 3 }

// layoutOf places the sections of a table of n records, nb buckets, m slots
// and a blob of blob bytes.
func layoutOf(s shape, n, nb, m uint32, blob uint64) layout {
	var l layout
	l.tags = headerSize + pad4(uint64(nb))
	l.remap = l.tags + pad4(uint64(n))
	l.recs = l.remap + 4*(uint64(m)-uint64(n))
	l.blob = l.recs + uint64(n)*uint64(s.stride())
	l.size = l.blob + blob
	return l
}

// writeHeader fills image's header for t's fields and seals both CRCs.
func writeHeader(image []byte, t *table) {
	h := image[:headerSize]
	clear(h)
	copy(h, magic)
	le.PutUint16(h[4:], version)
	h[6], h[7], h[8], h[9] = t.shape.kind, t.shape.kw, t.shape.vw, t.shape.signs
	le.PutUint32(h[12:], t.n)
	le.PutUint32(h[16:], t.nb)
	le.PutUint32(h[20:], t.m)
	le.PutUint64(h[24:], t.seed)
	le.PutUint64(h[32:], uint64(len(t.blob)))
	le.PutUint64(h[40:], uint64(len(image)))
	le.PutUint32(h[48:], crc32.Checksum(image[headerSize:], castagnoli))
	le.PutUint32(h[headerSealed:], crc32.Checksum(h[:headerSealed], castagnoli))
}

// view points t's sections into image, whose header says the layout l.
func (t *table) view(image []byte, l layout) {
	t.image = image
	t.pilots = image[headerSize : headerSize+uint64(t.nb)]
	t.tags = image[l.tags : l.tags+uint64(t.n)]
	t.remap = image[l.remap:l.recs]
	t.recs = image[l.recs:l.blob]
	t.blob = stringOf(image[l.blob:l.size])
	t.stride = t.shape.stride()
	t.kmask = mask(t.shape.kw)
}
