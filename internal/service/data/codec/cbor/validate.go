// Package cbor — the validator: one pass over the bytes, before anything is
// decoded, that accepts exactly one well-formed and valid data item within
// the bounds. A document it refuses never touches the caller's value, and no
// length, count or depth read from the input is acted on until it has passed.
package cbor

import (
	"math"
	"unicode/utf8"
)

// The bounds every decode enforces before it allocates anything. They are
// the values the codec has always enforced: an array or a map may hold a
// million entries and nest thirty-two deep.
const (
	// maxCBORArrayElements caps the element count of any single array.
	maxCBORArrayElements int = 1 << 20
	// maxCBORMapPairs caps the pair count of any single map.
	maxCBORMapPairs int = 1 << 20
	// maxCBORNestedLevels caps how deeply arrays, maps and tags may nest, each
	// counting one level. The encoder refuses to write deeper than it.
	maxCBORNestedLevels int = 32
	// maxCBORStringChunks caps the chunks of one indefinite-length string, the
	// same bound as an array's elements.
	maxCBORStringChunks int = 1 << 20
)

// What a frame of the validator's stack holds open.
const (
	// frameArray is an array, definite or indefinite.
	frameArray frameKind = iota
	// frameMap is a map, its items counted one per key and one per value.
	frameMap
	// frameChunks is an indefinite-length string awaiting its chunks.
	frameChunks
	// frameTag is a tag awaiting the one data item it encloses.
	frameTag
)

// frameKind says what a validator frame holds open.
type frameKind uint8

// validFrame is one item the validator has opened and not yet closed. It is
// sixteen bytes, because every Unmarshal zeroes a stack of them.
type validFrame struct {
	// count is how many more items a definite array or map (two per pair) or
	// a tag (one) is owed — or, for an indefinite array, map or string, how
	// many items or chunks it has held so far, against their bounds. Only a
	// break closes an indefinite frame, so it is never owed anything.
	count uint64
	// tag is the number of a frameTag, saturated at math.MaxUint8: only tags
	// 0 to 3 constrain their content (RFC 8949 §5.3.2), so every larger
	// number means the same thing here.
	tag uint8
	// kind is what was opened.
	kind frameKind
	// major is the major type of a frameChunks string, which its chunks share.
	major majorType
	// indefinite is true when only a break closes the frame.
	indefinite bool
}

// validator checks one data item. It is resumable: when the bytes run out
// inside the item it says so and keeps its place, and a later call with more
// bytes — always starting at the item's first byte — goes on from there. The
// stream decoder relies on that to read each byte once however the item
// arrives; Unmarshal, which holds the whole item, calls it once.
type validator struct {
	// stack holds the open frames: one slot more than the nesting bound,
	// because an indefinite string is a frame that cannot hold another.
	stack [maxCBORNestedLevels + 1]validFrame
	// depth is the number of open frames.
	depth int
	// limit is the nesting bound this walk enforces, at most
	// maxCBORNestedLevels.
	limit int
	// off is the offset of the next unread head, from the item's first byte.
	off int
	// complete is set once the item's last byte has been read.
	complete bool
}

// validateItem checks that data opens with one complete, well-formed and
// valid data item nesting at most limit levels, and returns its length.
// Bytes after the item are the caller's to judge.
func validateItem(data []byte, limit int) (n int, err error) {
	walk := validator{limit: limit}
	complete, err := walk.resume(data)
	//: a malformed or out-of-bounds item.
	if err != nil {
		//: already UNMARSHAL_FAILED, with the offset.
		return 0, err
	}
	//: the whole item is in hand, so running out of bytes is a defect.
	if !complete {
		//: truncated.
		return 0, malformed(len(data), "the input ends inside a data item")
	}
	//: the item's length.
	return walk.off, nil
}

// resume validates as much of the item as data holds. complete reports
// whether the item has been read to its end; false with a nil error means
// data ends inside it.
func (v *validator) resume(data []byte) (complete bool, err error) {
	//: one head (and, for a definite string, its payload) per step.
	for !v.complete {
		progressed, stepErr := v.step(data)
		//: a malformed item stops the walk for good.
		if stepErr != nil {
			//: the refusal.
			return false, stepErr
		}
		//: the bytes ran out; the place is kept for the next call.
		if !progressed {
			//: not complete yet.
			return false, nil
		}
	}
	//: the item ends at v.off.
	return true, nil
}

// step reads the next head. progressed is false, with a nil error, when
// data ends before the head or before a definite string's payload does —
// in which case nothing was consumed.
func (v *validator) step(data []byte) (progressed bool, err error) {
	h, ok := readHead(data, v.off)
	//: not even a whole head.
	if !ok {
		//: wait for more.
		return false, nil
	}
	//: RFC 8949 §3: 28, 29 and 30 are reserved in every major type.
	if h.info >= infoReservedMin && h.info <= infoReservedMax {
		//: not well-formed.
		return false, malformed(v.off, "reserved additional information")
	}
	//: the break code closes the innermost indefinite-length item.
	if h.major == majorSimple && h.info == infoIndefinite {
		//: consumed, or refused where nothing is open to close.
		return true, v.closeIndefinite()
	}
	//: the head on its own, then the head where it stands.
	if headErr := v.admissible(h); headErr != nil {
		//: not well-formed, or not valid.
		return false, headErr
	}
	//: read it.
	return v.accept(data, h)
}

// admissible applies the rules a head is subject to, on its own and in the
// frame it opens into.
func (v *validator) admissible(h itemHead) error {
	//: integers and tag numbers have no indefinite form.
	if h.info == infoIndefinite && (h.major == majorUnsigned || h.major == majorNegative || h.major == majorTag) {
		//: not well-formed.
		return malformed(v.off, "an integer or tag head with the indefinite-length marker")
	}
	//: RFC 8949 §3.3: the extension byte only carries simple values 32..255.
	if h.major == majorSimple && h.info == info1Byte && h.arg < minExtendedSimple {
		//: not well-formed.
		return malformed(v.off, "a two-byte simple value below 32")
	}
	//: a top-level item, or one inside an array or a map, answers to nothing
	//: else.
	if v.depth == 0 || !v.stack[v.depth-1].kind.constrains() {
		//: admissible.
		return nil
	}
	//: the enclosing string or tag constrains what it holds.
	return v.admissibleIn(&v.stack[v.depth-1], h)
}

// constrains reports whether a frame of this kind constrains the items it
// holds: an indefinite string its chunks, a tag its content.
func (k frameKind) constrains() bool {
	//: arrays and maps hold anything.
	return k == frameChunks || k == frameTag
}

// admissibleIn applies the rules the open frame top places on the head h of
// the next item inside it.
func (v *validator) admissibleIn(top *validFrame, h itemHead) error {
	//: RFC 8949 §3.2.3: chunks are definite strings of the string's own type.
	if top.kind == frameChunks && (h.major != top.major || h.info == infoIndefinite) {
		//: not well-formed.
		return malformed(v.off, "a chunk of an indefinite-length string that is not a definite string of its type")
	}
	//: RFC 8949 §5.3.2: tags 0 to 3 admit one content type each.
	if top.kind == frameTag && !tagAdmits(uint64(top.tag), h) {
		//: not valid.
		return malformed(v.off, "a tag 0, 1, 2 or 3 enclosing a data item of the wrong type")
	}
	//: admissible.
	return nil
}

// tagAdmits reports whether a tag numbered tag may enclose the item whose
// head is h: a text string for tag 0, an integer or a float for tag 1, a byte
// string for the bignum tags 2 and 3, and anything for every other tag.
func tagAdmits(tag uint64, h itemHead) bool {
	//: only the four tags RFC 8949 §3.4 types are checked.
	switch tag {
	case tagDateTime:
		//: an RFC 3339 text string.
		return h.major == majorText
	case tagEpoch:
		//: seconds, as an integer or a floating-point number.
		return h.major == majorUnsigned || h.major == majorNegative || isFloatHead(h)
	case tagPositiveBignum, tagNegativeBignum:
		//: the magnitude, as a byte string.
		return h.major == majorBytes
	default:
		//: an unrecognised tag constrains nothing.
		return true
	}
}

// isFloatHead reports whether h opens a half-, single- or double-precision
// floating-point number.
func isFloatHead(h itemHead) bool {
	//: major type 7 with additional information 25, 26 or 27.
	return h.major == majorSimple && h.info >= info2Bytes && h.info <= info8Bytes
}

// accept consumes the item whose head is h: the head, and the payload of a
// definite string once all of it is present.
func (v *validator) accept(data []byte, h itemHead) (progressed bool, err error) {
	var payload int
	//: only a definite byte or text string carries bytes after its head.
	if isDefiniteString(h) {
		var ready bool
		payload, ready, err = v.payload(data, h)
		//: an invalid string, or one whose payload has not all arrived.
		if err != nil || !ready {
			//: nothing consumed.
			return false, err
		}
	}
	//: an item counts against the bound of the indefinite frame holding it
	//: before it is placed; a definite frame's count was bounded at its head.
	if v.depth > 0 && v.stack[v.depth-1].indefinite {
		//: the bound may refuse it.
		if countErr := v.countChild(&v.stack[v.depth-1]); countErr != nil {
			//: refused.
			return false, countErr
		}
	}
	v.off += h.size + payload
	//: the head opens a frame, closes nothing, or completes a child.
	return true, v.place(h)
}

// isDefiniteString reports whether h opens a byte or text string of definite
// length, the only items whose payload follows their head.
func isDefiniteString(h itemHead) bool {
	//: major type 2 or 3, without the indefinite-length marker.
	return (h.major == majorBytes || h.major == majorText) && h.info != infoIndefinite
}

// payload returns the length of the payload of the definite string whose
// head is h, once all of it is present, after checking that a text string is
// UTF-8.
func (v *validator) payload(data []byte, h itemHead) (length int, ready bool, err error) {
	start := v.off + h.size
	//: compared as uint64 so a length beyond int is not truncated first.
	if h.arg > uint64(len(data)-start) {
		//: the payload is not all here.
		return 0, false, nil
	}
	length = int(h.arg)
	//: RFC 8949 §5.3.1: a text string, and each of its chunks, is UTF-8.
	if h.major == majorText && !utf8.Valid(data[start:start+length]) {
		//: not valid.
		return 0, false, malformed(v.off, "a text string that is not valid UTF-8")
	}
	//: present and valid.
	return length, true, nil
}

// countChild counts one more item against the bound of top, the open
// indefinite frame that holds it.
func (v *validator) countChild(top *validFrame) error {
	top.count++
	//: each kind of frame has its own bound.
	switch top.kind {
	case frameArray:
		//: elements.
		return v.bounded(top.count, maxCBORArrayElements, "an indefinite-length array of more than 1048576 elements")
	case frameMap:
		//: pairs, counting a key on its own as a pair begun.
		return v.bounded((top.count+1)/2, maxCBORMapPairs, "an indefinite-length map of more than 1048576 pairs")
	default:
		//: chunks of a string.
		return v.bounded(top.count, maxCBORStringChunks, "an indefinite-length string of more than 1048576 chunks")
	}
}

// bounded returns the refusal described by detail when count exceeds bound.
func (v *validator) bounded(count uint64, bound int, detail string) error {
	//: within the bound.
	if count <= uint64(bound) {
		//: nothing to refuse.
		return nil
	}
	//: over it.
	return malformed(v.off, detail)
}

// place records the item whose head h was just consumed: an indefinite
// string, an array, a map or a tag opens a frame; anything else is a whole
// item.
func (v *validator) place(h itemHead) error {
	//: the major type decides.
	switch h.major {
	case majorArray, majorMap:
		//: a container, possibly empty.
		return v.openContainer(h)
	case majorTag:
		//: a tag awaits exactly one item.
		return v.push(validFrame{kind: frameTag, count: 1, tag: uint8(min(h.arg, math.MaxUint8))}, true)
	case majorBytes, majorText:
		//: an indefinite string awaits its chunks.
		if h.info == infoIndefinite {
			//: the frame does not count as nesting: it holds strings only.
			return v.push(validFrame{kind: frameChunks, major: h.major, indefinite: true}, false)
		}
		//: a definite string is whole.
		v.finishChild()
		//: placed.
		return nil
	default:
		//: an integer or a simple value is whole.
		v.finishChild()
		//: placed.
		return nil
	}
}

// openContainer opens the array or map whose head is h, after bounding its
// count and its depth. An empty definite container is whole at once.
func (v *validator) openContainer(h itemHead) error {
	kind, bound, perEntry, detail := frameArray, maxCBORArrayElements, uint64(1), "an array of more than 1048576 elements"
	//: a map owes two items per pair and has its own bound.
	if h.major == majorMap {
		kind, bound, perEntry, detail = frameMap, maxCBORMapPairs, 2, "a map of more than 1048576 pairs"
	}
	//: an indefinite container closes on its break.
	if h.info == infoIndefinite {
		//: opened.
		return v.push(validFrame{kind: kind, indefinite: true}, true)
	}
	//: the declared count is bounded before anything is done with it.
	if boundErr := v.bounded(h.arg, bound, detail); boundErr != nil {
		//: refused.
		return boundErr
	}
	//: an empty container still sits at a level of nesting.
	if h.arg == 0 {
		//: bounded like any other, then whole.
		return v.nestEmpty()
	}
	//: opened, owing its items.
	return v.push(validFrame{kind: kind, count: h.arg * perEntry}, true)
}

// nestEmpty accounts for an empty definite array or map: it nests one level
// like any container, and is whole as soon as it is read.
func (v *validator) nestEmpty() error {
	//: the level it would occupy must exist.
	if v.depth >= v.limit {
		//: too deep.
		return v.tooDeep()
	}
	v.finishChild()
	//: placed.
	return nil
}

// push opens a frame. A counted frame — an array, a map or a tag — is one
// level of nesting and is refused past the limit.
func (v *validator) push(f validFrame, counted bool) error {
	//: arrays, maps and tags nest; a chunked string does not.
	if counted && v.depth >= v.limit {
		//: too deep.
		return v.tooDeep()
	}
	v.stack[v.depth] = f
	v.depth++
	//: opened.
	return nil
}

// tooDeep is the refusal of an item nesting past the limit.
func (v *validator) tooDeep() error {
	//: one message for every kind of level.
	return malformed(v.off, "arrays, maps and tags nested more than 32 levels deep")
}

// finishChild records that one item has been read to its end, closing every
// definite frame that item completes, and the whole walk when it was the
// top-level item.
func (v *validator) finishChild() {
	//: a completed frame is itself a completed item of the frame below it.
	for v.depth > 0 {
		top := &v.stack[v.depth-1]
		//: only a break closes an indefinite frame.
		if top.indefinite {
			//: it stays open.
			return
		}
		top.count--
		//: the frame is still owed items.
		if top.count > 0 {
			//: it stays open.
			return
		}
		v.depth--
	}
	v.complete = true
}

// closeIndefinite handles a break code: it closes the innermost frame, which
// must be an indefinite-length one, and a map only after a whole pair.
func (v *validator) closeIndefinite() error {
	//: a break needs an indefinite-length item to close.
	if v.depth == 0 || !v.stack[v.depth-1].indefinite {
		//: not well-formed.
		return malformed(v.off, "a break code outside an indefinite-length item")
	}
	top := &v.stack[v.depth-1]
	//: a map cannot end between a key and its value.
	if top.kind == frameMap && top.count%2 == 1 {
		//: not well-formed.
		return malformed(v.off, "an indefinite-length map closed after a key with no value")
	}
	v.off++
	v.depth--
	v.finishChild()
	//: closed.
	return nil
}
