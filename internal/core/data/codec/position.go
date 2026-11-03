package codec

import (
	"strconv"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The keys a decode refusal carries its position under (#1125): every codec
// writes them through PositionFields or by these names, so PositionOf reads
// any codec's position back.
const (
	// FieldOffset is the key of a refusal's 0-based byte offset.
	FieldOffset = "offset"
	// FieldLine is the key of a refusal's 1-based line.
	FieldLine = "line"
	// FieldColumn is the key of a refusal's 1-based column, counted as the format
	// counts it: characters for yaml and toml, bytes for csv.
	FieldColumn = "column"
)

// The slots of the three coordinates, in the order PositionFields writes them.
const (
	// slotOffset is the byte offset's slot.
	slotOffset int = iota
	// slotLine is the line's slot.
	slotLine
	// slotColumn is the column's slot.
	slotColumn
	// positionFieldCount is how many fields a position can carry.
	positionFieldCount
)

// PositionValue places a decode refusal in its input (#1125). Offset is the
// 0-based byte, Line and Column are 1-based, a zero Line or Column is unknown.
// A text format places by line and column — characters for yaml and toml,
// bytes for csv — and adds the offset when it knows a non-zero one; a binary
// format places by offset alone, so with no Line the Offset is meant even
// when zero: the input's first byte. json carries the offset and xml the line
// their standard library's errors give.
type PositionValue struct {
	// Offset is the 0-based byte offset of the problem in the input.
	Offset int
	// Line is the 1-based line of the problem, zero when unknown.
	Line int
	// Column is the 1-based column of the problem, zero when unknown.
	Column int
}

// PositionFields returns the fields a decode refusal carries p under, for
// errs.Wrap: FieldOffset, FieldLine and FieldColumn, each only when p knows
// it — Line and Column when not zero, Offset when not zero or when p has no
// line (see PositionValue). Every codec places a refusal through it or
// through those three keys, so PositionOf reads any codec's position back
// (#1125).
func PositionFields(p PositionValue) []errs.FieldValue {
	// room for the three, so a caller appending one more field does not copy.
	fields := make([]errs.FieldValue, 0, positionFieldCount)
	//: a binary position's only coordinate, the first byte included; beside a line, only when known.
	if p.Offset != 0 || p.Line == 0 {
		fields = append(fields, errs.Int(FieldOffset, p.Offset))
	}
	//: the line, when known.
	if p.Line != 0 {
		fields = append(fields, errs.Int(FieldLine, p.Line))
	}
	//: the column, when known.
	if p.Column != 0 {
		fields = append(fields, errs.Int(FieldColumn, p.Column))
	}
	//: what p knows, in a fixed order.
	return fields
}

// PositionOf reads back the position a codec's refusal carries, in err or an
// error it wraps: ok is false when it carries none. A key set twice along the
// chain keeps its first value — the origin's, as errs.Wrap keeps the origin.
// A key whose value is not an integer is not a position.
func PositionOf(err error) (p PositionValue, ok bool) {
	var seen [positionFieldCount]bool
	//: the chain's fields, the origin's first.
	for _, field := range errs.FieldsOf(err) {
		slot, dst := coordinate(&p, field.Key())
		//: another key, or a coordinate already read nearer the origin.
		if dst == nil || seen[slot] {
			continue
		}
		n, perr := strconv.Atoi(field.StringValue())
		//: a name under a coordinate's key places nothing.
		if perr != nil {
			continue
		}
		*dst = n
		seen[slot] = true
		ok = true
	}
	//: the coordinates found, if any.
	return p, ok
}

// coordinate returns the slot and the member of p that key names, or a nil
// member for a key that is not a coordinate.
func coordinate(p *PositionValue, key string) (slot int, dst *int) {
	//: one key per coordinate.
	switch key {
	//: the byte offset.
	case FieldOffset:
		//: its member.
		return slotOffset, &p.Offset
	//: the line.
	case FieldLine:
		//: its member.
		return slotLine, &p.Line
	//: the column.
	case FieldColumn:
		//: its member.
		return slotColumn, &p.Column
	//: any other field.
	default:
		//: not a coordinate.
		return 0, nil
	}
}
