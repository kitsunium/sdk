package http2

import (
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// The dynamic table's layout (RFC 7541 §4): mem[:capacity] is a byte ring of
// entries, mem[capacity:] a ring of two-byte offsets, one slot per entry. An
// entry's stored form is its five-byte header — a flag, the name's and the
// value's lengths — then the name and the value, wrapping the ring. RFC 7541
// §4.1 counts an entry at its name, its value and 32, so stored bytes never
// fill the ring: 5 of those 32 hold the header.
const (
	// entryFlagInvalid marks a stored field RFC 9113 §8.2.1 calls malformed: a
	// later reference to it is malformed too, with no second byte scan.
	entryFlagInvalid byte = 1
	// entryHeaderLen is the bytes of a stored entry's header.
	entryHeaderLen int32 = 5
	// entryOverhead is what RFC 7541 §4.1 adds to an entry's name and value.
	entryOverhead int32 = 32
	// slotLen is the bytes of one offset slot.
	slotLen int32 = 2
)

// Decoder is one connection's HPACK decoder: the dynamic table's bookkeeping,
// with no pointer in it, so a connection's state fits a pointer-free slice.
// The table's bytes are the mem a caller hands to Decode.
type Decoder struct {
	// maxList is the header list ceiling, RFC 9113 §6.5.2's measure.
	maxList int
	// capacity is mem's ring, the most the table can ever hold.
	capacity int32
	// limit is the largest table the peer may announce now: the protocol's
	// default until our SETTINGS is acknowledged, then what we advertised.
	limit int32
	// advertised is what our SETTINGS says limit becomes.
	advertised int32
	// maxSize is the table's current size limit, the peer's last size update.
	maxSize int32
	// size is the entries' total as RFC 7541 §4.1 counts it.
	size int32
	// head is the ring offset the next entry is written at.
	head int32
	// tail is the ring offset of the oldest entry.
	tail int32
	// count is how many entries the table holds.
	count int32
	// top is the slot of the newest entry.
	top int32
}

// NewDecoder returns a connection's decoder: table is the HEADER_TABLE_SIZE the
// server advertises, zero when it advertises none, and maxList the header list
// ceiling. The peer may use a table of the protocol's default size until it has
// acknowledged that advertisement.
func NewDecoder(table, maxList int) Decoder {
	capacity := max(table, corehttp2.DefaultHeaderTable)
	return Decoder{
		maxList:    maxList,
		capacity:   int32(capacity),
		limit:      int32(corehttp2.DefaultHeaderTable),
		advertised: int32(table),
		maxSize:    int32(corehttp2.DefaultHeaderTable),
	}
}

// TableMem returns the bytes of memory a table advertised at size table takes:
// the caller's Decode argument, one per connection, handed in only once the
// connection's first field block arrives.
func TableMem(table int) int {
	capacity := max(table, corehttp2.DefaultHeaderTable)
	return capacity + capacity/int(entryOverhead)*int(slotLen)
}

// acknowledge moves the peer's bound to what our SETTINGS advertised, the
// moment it has acknowledged them (RFC 7541 §4.2).
func (d *Decoder) acknowledge() {
	d.limit = d.advertised
}

// slots returns the table's number of offset slots.
func (d *Decoder) slots() int32 {
	return d.capacity / entryOverhead
}

// ringPut copies p into the ring at at and returns the offset past it.
func (d *Decoder) ringPut(mem []byte, at int32, p []byte) int32 {
	n := copy(mem[at:d.capacity], p)
	copy(mem, p[n:])
	return (at + int32(len(p))) % d.capacity
}

// ringGet appends n ring bytes starting at at to dst.
func (d *Decoder) ringGet(mem, dst []byte, at, n int32) []byte {
	//: the bytes may wrap the ring's end: two copies, or one.
	if at+n <= d.capacity {
		return append(dst, mem[at:at+n]...)
	}
	first := d.capacity - at
	dst = append(dst, mem[at:d.capacity]...)
	return append(dst, mem[:n-first]...)
}

// slotAt returns the ring offset the slot of age age holds: zero is the newest.
func (d *Decoder) slotAt(mem []byte, age int32) int32 {
	slots := d.slots()
	slot := (d.top - age + slots) % slots
	at := d.capacity + slot*slotLen
	return int32(uint16(mem[at]) | uint16(mem[at+1])<<8)
}

// storeSlot records an entry's ring offset as the newest slot.
func (d *Decoder) storeSlot(mem []byte, offset int32) {
	d.top = (d.top + 1) % d.slots()
	at := d.capacity + d.top*slotLen
	mem[at] = byte(offset)
	mem[at+1] = byte(offset >> 8)
}

// entryAt reads the entry of age age: appended to scratch, and returned.
func (d *Decoder) entryAt(mem, scratch []byte, age int32) (decodedField, []byte) {
	at := d.slotAt(mem, age)
	var hdr [entryHeaderLen]byte
	dst := d.ringGet(mem, hdr[:0], at, entryHeaderLen)
	flag, nameLen, valueLen := dst[0], d.u16(dst[1:]), d.u16(dst[3:])
	start := len(scratch)
	scratch = d.ringGet(mem, scratch, (at+entryHeaderLen)%d.capacity, int32(nameLen))
	mid := len(scratch)
	scratch = d.ringGet(mem, scratch, (at+entryHeaderLen+int32(nameLen))%d.capacity, int32(valueLen))
	return decodedField{
		name:    scratch[start:mid:mid],
		value:   scratch[mid:len(scratch):len(scratch)],
		invalid: flag&entryFlagInvalid != 0,
	}, scratch
}

// entrySize reads the stored name and value lengths of the entry of age age.
func (d *Decoder) entrySize(mem []byte, age int32) (nameLen, valueLen int32) {
	at := d.slotAt(mem, age)
	var hdr [entryHeaderLen]byte
	dst := d.ringGet(mem, hdr[:0], at, entryHeaderLen)
	return int32(d.u16(dst[1:])), int32(d.u16(dst[3:]))
}

// nameAt appends the name of the entry of age age to scratch and returns it.
func (d *Decoder) nameAt(mem, scratch []byte, age int32) (name, grown []byte) {
	at := d.slotAt(mem, age)
	var hdr [entryHeaderLen]byte
	dst := d.ringGet(mem, hdr[:0], at, entryHeaderLen)
	nameLen := int32(d.u16(dst[1:]))
	start := len(scratch)
	scratch = d.ringGet(mem, scratch, (at+entryHeaderLen)%d.capacity, nameLen)
	return scratch[start:len(scratch):len(scratch)], scratch
}

// u16 reads a stored little-endian length.
func (d *Decoder) u16(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}

// evictOldest drops the table's oldest entry, RFC 7541 §4.4.
func (d *Decoder) evictOldest(mem []byte) {
	nameLen, valueLen := d.entrySize(mem, d.count-1)
	stored := entryHeaderLen + nameLen + valueLen
	d.tail = (d.tail + stored) % d.capacity
	d.size -= nameLen + valueLen + entryOverhead
	d.count--
}

// shrinkTo evicts oldest entries until size is at most target.
func (d *Decoder) shrinkTo(mem []byte, target int32) {
	//: the oldest go first, RFC 7541 §4.3.
	for d.size > target {
		d.evictOldest(mem)
	}
}

// insert adds a field to the table, evicting first, RFC 7541 §4.4. A field
// bigger than the table empties it and is not added.
func (d *Decoder) insert(mem, name, value []byte, invalid bool) {
	size := int32(len(name)) + int32(len(value)) + entryOverhead
	//: an entry larger than the table empties it and takes no room.
	if size > d.maxSize {
		d.shrinkTo(mem, 0)
		return
	}
	d.shrinkTo(mem, d.maxSize-size)
	at := d.head
	var flag byte
	//: the flag keeps a malformed field's verdict for later references.
	if invalid {
		flag = entryFlagInvalid
	}
	hdr := [entryHeaderLen]byte{flag, byte(len(name)), byte(len(name) >> 8), byte(len(value)), byte(len(value) >> 8)}
	next := d.ringPut(mem, at, hdr[:])
	next = d.ringPut(mem, next, name)
	d.head = d.ringPut(mem, next, value)
	d.storeSlot(mem, at)
	d.count++
	d.size += size
}
