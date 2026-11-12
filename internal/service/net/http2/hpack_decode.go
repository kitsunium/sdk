package http2

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// The representation prefixes of RFC 7541 §6: which field a block's byte
// opens.
const (
	// indexedPrefix is the indexed field's high bit (§6.1), an integer of 7 bits.
	indexedPrefix byte = 0x80
	// incrementalMask and incrementalBits recognise a literal with
	// incremental indexing (§6.2.1), an integer of 6 bits.
	incrementalMask byte = 0xc0
	incrementalBits byte = 0x40
	// sizeUpdateMask and sizeUpdateBits recognise a dynamic table size update
	// (§6.3), an integer of 5 bits.
	sizeUpdateMask byte = 0xe0
	sizeUpdateBits byte = 0x20
	// integer prefix widths.
	prefix7 uint8 = 7
	prefix6 uint8 = 6
	prefix5 uint8 = 5
	prefix4 uint8 = 4
	// huffmanBit marks a Huffman-coded string, §5.2.
	huffmanBit byte = 0x80
	// continuationBit marks an integer's continuation byte, §5.1.
	continuationBit byte = 0x80
	// continuationShift is the bits one continuation byte adds to an integer.
	continuationShift uint = 7
	// maxIntBytes is how many continuation bytes an integer may take: past
	// them it overflows anything a table or a block can hold.
	maxIntBytes int = 5
	// maxHpackInt bounds a decoded integer: well over any table or block.
	maxHpackInt int = 1 << 28
	// firstDynamicIndex is the table index of the newest dynamic entry.
	firstDynamicIndex int = 62
	// staticOffsetSlots is how many staticOffsets entries one entry takes.
	staticOffsetSlots int = 4
	// listFieldOverhead is what RFC 9113 §6.5.2 adds to a field's name and
	// value: 32, the same as an entry's table overhead (RFC 7541 §4.1).
	listFieldOverhead int = 32
)

// decodeRun is one Decode call's cursor: the block, what it fills, and the
// running header list size. It lives on Decode's stack.
type decodeRun struct {
	// d is the connection's table.
	d *Decoder
	// mem is the table's memory.
	mem []byte
	// block is what is left of the field block.
	block []byte
	// scratch receives every byte a field is not aliased from.
	scratch []byte
	// req receives the fields.
	req *corenet.HTTP1RequestValue
	// list is the header list so far, RFC 9113 §6.5.2's measure.
	list int
	// invalid reports a field RFC 9113 §8.2.1 calls malformed: reported at
	// the block's end, so the table stays in step with the peer.
	invalid bool
	// updatesDone is set once a field was decoded: a table size update comes
	// first or not at all, RFC 7541 §4.2.
	updatesDone bool
}

// decodedField is one field while it is being built.
type decodedField struct {
	// name and value are its bytes.
	name, value []byte
	// invalid is the verdict on them; a table or static field carries
	// its verdict with it.
	invalid bool
}

// Decode decodes block into req.Fields, in arrival order, pseudo-fields
// included, at most corenet.HTTP1MaxFields of them. Values not aliased from
// block are written into scratch, which is returned grown; it allocates
// nothing when scratch has room, and every slice stays valid until the
// caller reuses block or scratch. mem is TableMem(table) bytes of the
// connection, or nil when the connection never needs one.
//
// A list past the ceiling, charged before a field is copied or inserted,
// refuses HeadersTooLarge; a block HPACK cannot decode refuses
// CompressionFailed; both leave the table undefined, and the connection
// ends. A malformed field refuses FieldMalformed after the block decoded
// to its end, the table intact.
func (d *Decoder) Decode(mem, block, scratch []byte, req *corenet.HTTP1RequestValue) ([]byte, error) {
	r := decodeRun{d: d, mem: mem, block: block, scratch: scratch, req: req}
	req.NFields = 0
	//: every representation of the block, until it is empty.
	for len(r.block) > 0 {
		//: the first refused representation ends the block.
		if err := r.step(); err != nil {
			return r.scratch, err
		}
	}
	//: the block decoded to its end: a malformed field was only recorded.
	if r.invalid {
		return r.scratch, corehttp2.FieldMalformed
	}
	return r.scratch, nil
}

// step decodes one representation, RFC 7541 §6.
func (r *decodeRun) step() error {
	first := r.block[0]
	//: the prefixes are mutually exclusive, tested from the longest.
	switch {
	case first&indexedPrefix != 0:
		return r.indexed()
	case first&incrementalMask == incrementalBits:
		return r.literal(prefix6, true)
	case first&sizeUpdateMask == sizeUpdateBits:
		return r.sizeUpdate()
	default:
		//: 0000xxxx and 0001xxxx: literal without indexing and never indexed read alike.
		return r.literal(prefix4, false)
	}
}

// readInt decodes an integer of the given prefix width off the block's front,
// RFC 7541 §5.1, and advances past it.
func (r *decodeRun) readInt(prefix uint8) (int, error) {
	mask := byte(1)<<prefix - 1
	value := int(r.block[0] & mask)
	r.block = r.block[1:]
	//: an integer that fits its prefix takes no continuation byte.
	if value < int(mask) {
		return value, nil
	}
	return r.readContinuation(value)
}

// readContinuation accumulates an integer's continuation bytes.
func (r *decodeRun) readContinuation(value int) (int, error) {
	shift := uint(0)
	//: at most maxIntBytes continuation bytes: beyond that it overflows.
	for range maxIntBytes {
		//: a block that ends inside an integer is not one HPACK can read.
		if len(r.block) == 0 {
			return 0, corehttp2.CompressionFailed
		}
		b := r.block[0]
		r.block = r.block[1:]
		value += int(b&^continuationBit) << shift
		//: the last byte has the continuation bit clear.
		if b&continuationBit == 0 {
			return checkedInt(value)
		}
		shift += continuationShift
	}
	return 0, corehttp2.CompressionFailed
}

// checkedInt refuses an integer past any table or block.
func checkedInt(value int) (int, error) {
	//: nothing the table or a block holds is this long.
	if value > maxHpackInt {
		return 0, corehttp2.CompressionFailed
	}
	return value, nil
}

// indexed decodes an indexed field, §6.1: the whole field is an index.
func (r *decodeRun) indexed() error {
	index, err := r.readInt(prefix7)
	//: a bad integer ends the block.
	if err != nil {
		return err
	}
	//: the indexed field's index is at least one.
	if index == 0 {
		return corehttp2.CompressionFailed
	}
	field, err := r.fieldByIndex(index)
	//: an index past both tables ends the block.
	if err != nil {
		return err
	}
	return r.emit(&field)
}

// fieldByIndex looks an index up in the static then the dynamic table.
func (r *decodeRun) fieldByIndex(index int) (decodedField, error) {
	//: the first sixty-one indices are the static table's.
	if index <= staticLen {
		return staticField(index), nil
	}
	return r.dynamicField(index)
}

// staticField returns a static entry's bytes, aliasing the immutable table.
func staticField(index int) decodedField {
	at := staticOffsetSlots * (index - 1)
	nameOff, nameLen := int(staticOffsets[at]), int(staticOffsets[at+1])
	valOff, valLen := int(staticOffsets[at+2]), int(staticOffsets[at+3])
	return decodedField{
		name:  staticBytes[nameOff : nameOff+nameLen : nameOff+nameLen],
		value: staticBytes[valOff : valOff+valLen : valOff+valLen],
	}
}

// dynamicField looks an index up in the dynamic table and copies the entry
// out, charged against the list before the copy: the entry could be evicted
// by a later insert of the same block, so it is never aliased.
func (r *decodeRun) dynamicField(index int) (decodedField, error) {
	age := index - firstDynamicIndex
	//: an index past the table's entries is a decoding error, RFC 7541 §2.3.3.
	if age >= int(r.d.count) {
		return decodedField{}, corehttp2.CompressionFailed
	}
	nameLen, valueLen := r.d.entrySize(r.mem, int32(age))
	//: refused before the copy and before any insert: the bomb costs the ceiling.
	if r.list+int(nameLen)+int(valueLen)+listFieldOverhead > r.d.maxList {
		return decodedField{}, corehttp2.HeadersTooLarge
	}
	field, scratch := r.d.entryAt(r.mem, r.scratch, int32(age))
	r.scratch = scratch
	return field, nil
}

// emit charges a field to the list and appends it to the request.
func (r *decodeRun) emit(f *decodedField) error {
	r.updatesDone = true
	r.list += len(f.name) + len(f.value) + listFieldOverhead
	//: the charge is what bounds the block, RFC 9113 §6.5.2's measure, and the
	//: request's fields are a fixed array: past either, the list is too large.
	if r.list > r.d.maxList || r.req.NFields == corenet.HTTP1MaxFields {
		return corehttp2.HeadersTooLarge
	}
	r.req.Fields[r.req.NFields] = corenet.HTTP1FieldValue{Name: f.name, Value: f.value}
	r.req.NFields++
	r.invalid = r.invalid || f.invalid
	return nil
}

// sizeUpdate applies a dynamic table size update, §6.3: only before the
// block's first field, and never past what the server advertised.
func (r *decodeRun) sizeUpdate() error {
	size, err := r.readInt(prefix5)
	//: a bad integer ends the block.
	if err != nil {
		return err
	}
	//: an update past the bound, or after a field, breaks RFC 7541 §4.2.
	if r.updatesDone || size > int(r.d.limit) {
		return corehttp2.CompressionFailed
	}
	r.d.maxSize = int32(size)
	r.d.shrinkTo(r.mem, r.d.maxSize)
	return nil
}

// literal decodes a literal field, §6.2: the name by index or as a string, the
// value as a string, added to the table when index is set.
func (r *decodeRun) literal(prefix uint8, index bool) error {
	nameIndex, err := r.readInt(prefix)
	//: a bad integer ends the block.
	if err != nil {
		return err
	}
	field, err := r.literalName(nameIndex)
	//: a bad name ends the block.
	if err != nil {
		return err
	}
	//: the value is a string in every literal form.
	if field.value, err = r.readString(r.budget(&field)); err != nil {
		return err
	}
	field.invalid = !validField(&field)
	//: charged before the insert: a refused field leaves no trace in the table.
	if err := r.emit(&field); err != nil {
		return err
	}
	//: only the incremental form adds to the table.
	if index {
		r.d.insert(r.mem, field.name, field.value, field.invalid)
	}
	return nil
}

// budget is how many decoded value bytes this field may still take.
func (r *decodeRun) budget(f *decodedField) int {
	return max(r.d.maxList-r.list-len(f.name)-listFieldOverhead, 0)
}

// literalName returns a literal field's name: by index, or as a string. A
// name found by index is judged again with its value, since the table's
// verdict covers a whole field and a literal reuses only its name.
func (r *decodeRun) literalName(nameIndex int) (decodedField, error) {
	//: zero: the name follows as a string.
	if nameIndex == 0 {
		name, err := r.readString(r.d.maxList - r.list)
		return decodedField{name: name}, err
	}
	//: a static name is the table's own bytes.
	if nameIndex <= staticLen {
		return decodedField{name: staticField(nameIndex).name}, nil
	}
	return r.dynamicName(nameIndex)
}

// dynamicName copies the name of a dynamic entry into the scratch, charging
// its bytes first: a literal reuses only the entry's name.
func (r *decodeRun) dynamicName(index int) (decodedField, error) {
	age := index - firstDynamicIndex
	//: an index past the table's entries is a decoding error, RFC 7541 §2.3.3.
	if age >= int(r.d.count) {
		return decodedField{}, corehttp2.CompressionFailed
	}
	nameLen, _ := r.d.entrySize(r.mem, int32(age))
	//: refused before the copy, RFC 9113 §6.5.2's measure.
	if r.list+int(nameLen)+listFieldOverhead > r.d.maxList {
		return decodedField{}, corehttp2.HeadersTooLarge
	}
	name, scratch := r.d.nameAt(r.mem, r.scratch, int32(age))
	r.scratch = scratch
	return decodedField{name: name}, nil
}

// readString decodes a string literal, §5.2: aliased from the block when plain,
// decoded into scratch when Huffman-coded, its decoded size at most budget.
func (r *decodeRun) readString(budget int) ([]byte, error) {
	//: a block that ends before its string is not one HPACK can read.
	if len(r.block) == 0 {
		return nil, corehttp2.CompressionFailed
	}
	huffman := r.block[0]&huffmanBit != 0
	length, err := r.readInt(prefix7)
	//: a bad length ends the block.
	if err != nil {
		return nil, err
	}
	//: a length past what is left of the block is refused before any byte is read.
	if length > len(r.block) {
		return nil, corehttp2.CompressionFailed
	}
	raw := r.block[:length:length]
	r.block = r.block[length:]
	//: a plain string is only aliased; its size is charged by emit.
	if !huffman {
		return raw, nil
	}
	return r.huffmanString(raw, budget)
}
